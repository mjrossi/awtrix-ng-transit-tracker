// Package gtfsrt is the generic provider for any agency that publishes a
// GTFS schedule feed and GTFS-Realtime TripUpdates. It serves agencies
// through configuration alone, so they need no code of their own.
//
// The static feed supplies line names and colors, station structure,
// headsigns and the timetable; the realtime feeds supply predictions. Trips
// in the timetable are shown with their prediction when a feed has one and
// at their scheduled time otherwise, so a board is not empty while no
// vehicle is near. Either feed may be left out: realtime only, or a
// schedule alone for agencies that publish no TripUpdates.
package gtfsrt

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/internal/upstream"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/registry"
)

// Name is the provider type in configuration.
const Name = "gtfsrt"

const (
	staticTTL = 24 * time.Hour
	// staticRetry spaces out schedule downloads after a failure; each one
	// can be hundreds of megabytes.
	staticRetry = 15 * time.Minute
	// feedTTL lets boards on the same feed share one download per poll.
	feedTTL = 15 * time.Second
	// The schedule is far larger than a feed: about 100 MB for large
	// agencies, which slow links need minutes to download.
	staticTimeout = 10 * time.Minute
	feedTimeout   = 30 * time.Second
	maxStaticBody = 512 << 20
	maxFeedBody   = 64 << 20
)

// Config is the provider's configuration block.
type Config struct {
	// StaticURL is the GTFS schedule zip. Optional, but without it lines
	// show as bare route IDs and destinations as stop IDs.
	StaticURL string `toml:"static_url"`
	// RealtimeURLs are GTFS-RT TripUpdates feeds. Agencies that split
	// their network across feeds, like the New York subway, list several.
	// Optional with a StaticURL, which then gives scheduled times only.
	RealtimeURLs []string `toml:"realtime_urls"`
	// Timetable shows trips from the schedule that no feed predicts, at
	// their scheduled time. Unset means true. Turn it off for agencies whose
	// realtime trips do not match their schedule's; otherwise their lines
	// show realtime trips only, with a warning.
	Timetable *bool `toml:"timetable"`
	// Headers are sent with every request, for feeds that need an API key.
	Headers map[string]string `toml:"headers"`
}

// Provider reads one agency's feeds. It is safe for concurrent use: boards
// sharing a provider share one download of each feed.
type Provider struct {
	cfg   Config
	http  *http.Client
	clock func() time.Time

	cacheDir string
	log      *slog.Logger
	// schedule is nil without a static_url.
	schedule *upstream.Cache[*static]
	// feeds holds one cache per realtime URL. Failures are cached for
	// feedTTL too, so boards sharing a failing feed do not each retry it.
	feeds map[string]*upstream.Cache[feed]

	mu sync.Mutex
	// current is the latest schedule, whose timetable is closed when a newer
	// one replaces it.
	current *static
	// warnedUnmatched is set once a mismatch between the feeds' trips and
	// the schedule's has been logged.
	warnedUnmatched atomic.Bool
}

// Register adds the provider to r, built with opts.
func Register(r *registry.Registry, opts ...Option) {
	registry.RegisterConfig(r, Name, func(cfg Config) (*Provider, error) { return New(cfg, nil, opts...) })
}

// New validates cfg and returns a provider. A nil client gets a default;
// requests are bounded by per-request timeouts either way.
func New(cfg Config, client *http.Client, opts ...Option) (*Provider, error) {
	if len(cfg.RealtimeURLs) == 0 && cfg.StaticURL == "" {
		return nil, errors.New("give static_url, realtime_urls (GTFS-RT TripUpdates), or both")
	}
	for i, u := range cfg.RealtimeURLs {
		if _, err := upstream.HTTPURL(fmt.Sprintf("realtime_urls[%d]", i), u); err != nil {
			return nil, err
		}
	}
	if cfg.StaticURL != "" {
		if _, err := upstream.HTTPURL("static_url", cfg.StaticURL); err != nil {
			return nil, err
		}
	}
	if client == nil {
		client = &http.Client{}
	}
	client = upstream.KeepHeadersOnHost(client, slices.Collect(maps.Keys(cfg.Headers))...)
	p := &Provider{cfg: cfg, http: client, clock: time.Now, log: slog.New(slog.DiscardHandler), feeds: map[string]*upstream.Cache[feed]{}}
	for _, o := range opts {
		o(p)
	}
	if cfg.StaticURL != "" {
		// A large schedule can take minutes to download, longer than a
		// board's app lifetime, so stale data is refreshed in the
		// background.
		p.schedule = upstream.NewCacheAt(upstream.Policy{TTL: staticTTL, Retry: staticRetry, Background: true, Timeout: staticTimeout}, p.loadStatic)
	}
	for _, u := range cfg.RealtimeURLs {
		p.feeds[u] = upstream.NewCache(upstream.Policy{TTL: feedTTL, Retry: feedTTL, Timeout: feedTimeout}, func(ctx context.Context) (feed, error) {
			return p.fetchFeed(ctx, u)
		})
	}
	return p, nil
}

// Departures implements transit.Provider. q.StopID may be a platform or a
// parent station, in which case departures from all its platforms are
// returned.
// When the schedule and every feed have loaded, a stop in none of them is
// transit.ErrUnknownStop.
//
// When some realtime feeds fail, departures from the rest come back with a
// *transit.PartialError, unless a failed feed is the only one known to
// serve the stop and the timetable cannot stand in for it; showing its
// departures as missing would mislead. A failed schedule download also
// gives a partial result, with bare IDs if no earlier schedule is cached.
func (p *Provider) Departures(ctx context.Context, q transit.Query) ([]transit.Departure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, staticErr := p.staticFor(ctx, q.Now)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if st == nil && staticErr != nil && len(p.cfg.RealtimeURLs) == 0 {
		return nil, staticErr
	}

	var (
		feeds          []feed
		problems       []error
		servedByOK     bool
		servedByFailed bool
	)
	for _, u := range p.cfg.RealtimeURLs {
		// On failure, f is the last feed that decoded, or empty if none has.
		f, err := p.feeds[u].Get(ctx, q.Now)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			problems = append(problems, err)
			servedByFailed = servedByFailed || f.serves(st, q.StopID)
			continue
		}
		servedByOK = servedByOK || f.serves(st, q.StopID)
		feeds = append(feeds, f)
	}

	var (
		plans    []planned
		hasTimes bool
		ttErr    error
	)
	if st != nil && st.tt != nil {
		// Trips are looked for from a while before q.Now, since a late one
		// can still be ahead, and a little after the window for early ones.
		from, to := q.Now.Add(-maxDelay), q.Now.Add(q.Window+maxEarly)
		sts, err := st.tt.stopTimes(ctx, q.StopID)
		if errors.Is(err, errTimetableClosed) {
			// A refresh replaced the schedule during the query.
			if cur := p.currentStatic(); cur != st && cur.tt != nil {
				st = cur
				sts, err = st.tt.stopTimes(ctx, q.StopID)
			}
		}
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			ttErr = fmt.Errorf("timetable: %w", err)
		default:
			hasTimes = len(sts) > 0
			plans = st.planned(sts, from, to)
		}
	}

	if !hasTimes && len(p.cfg.RealtimeURLs) > 0 && (len(feeds) == 0 || servedByFailed && !servedByOK) {
		return nil, errors.Join(append(problems, ttErr)...)
	}
	// Only a complete answer can tell a stop does not exist: the schedule
	// and every feed loaded, the schedule lacks it and no trip stops there.
	if st != nil && staticErr == nil && len(problems) == 0 && !servedByOK && !hasTimes {
		if _, ok := st.stops[q.StopID]; !ok {
			return nil, fmt.Errorf("%w %q", transit.ErrUnknownStop, q.StopID)
		}
	}

	out, used := merge(st, plans, feeds, q)
	var live []transit.Departure
	for _, f := range feeds {
		live = append(live, departures(st, f, q, used)...)
	}
	if dup := duplicatedLines(out, live); len(dup) > 0 {
		out = slices.DeleteFunc(out, func(d transit.Departure) bool { return dup[d.Line.ID] && !d.Realtime() })
		if !p.warnedUnmatched.Swap(true) {
			p.log.WarnContext(ctx, "realtime trips do not match the schedule's, so lines with realtime trips show only those; set timetable = false to show realtime trips only",
				slog.String("stop", q.StopID), slog.Any("lines", slices.Sorted(maps.Keys(dup))))
		}
	}
	out = append(out, live...)
	transit.SortByTime(out)
	if staticErr != nil {
		problems = append(problems, staticErr)
	}
	if ttErr != nil {
		problems = append(problems, ttErr)
	}
	if len(problems) > 0 {
		return out, &transit.PartialError{Err: errors.Join(problems...)}
	}
	return out, nil
}

const (
	// maxDelay is how late a timetabled trip can run and still be found.
	maxDelay = 3 * time.Hour
	// maxEarly is how early it can run.
	maxEarly = 30 * time.Minute
)

// merge turns timetabled trips into departures, with the prediction of the
// matching trip update where a feed has one. It returns the updates it
// used, which departures then leaves out.
func merge(st *static, plans []planned, feeds []feed, q transit.Query) ([]transit.Departure, map[*tripUpdate]bool) {
	used := map[*tripUpdate]bool{}
	if len(plans) == 0 {
		return nil, used
	}
	byTrip := map[string][]*tripUpdate{}
	byStart := map[tripStart]*tripUpdate{}
	for _, f := range feeds {
		for i := range f.trips {
			tu := &f.trips[i]
			if _, ok := st.trips[tu.tripID]; ok {
				byTrip[tu.tripID] = append(byTrip[tu.tripID], tu)
			} else if k, ok := startOf(tu); ok {
				byStart[k] = tu
			}
		}
	}
	var out []transit.Departure
	for _, pl := range plans {
		d := transit.Departure{
			Line:      st.routeLine(pl.route),
			Headsign:  st.plannedHeadsign(pl),
			StopID:    q.StopID,
			Scheduled: pl.at,
			Status:    transit.StatusScheduled,
		}
		if tu := matchUpdate(byTrip, byStart, pl); tu != nil {
			used[tu] = true
			switch tu.relation {
			case tripDeleted:
				continue
			case tripCanceled:
				d.Status = transit.StatusCanceled
			default:
				at, skipped, ok := predict(st, tu, pl, q.StopID)
				if skipped {
					continue
				}
				if ok {
					at = at.In(q.Now.Location())
					d.Predicted = &at
				}
			}
		}
		d.Scheduled = d.Scheduled.In(q.Now.Location())
		if q.Contains(d.Time()) {
			out = append(out, d)
		}
	}
	return out, used
}

// tripStart identifies a trip without its ID, as GTFS-RT allows: route,
// direction (-1 when not given), first departure and service day.
type tripStart struct {
	route string
	dir   int
	start int32
	day   string
}

func startOf(tu *tripUpdate) (tripStart, bool) {
	secs, ok := parseClock(tu.startTime)
	if !ok || tu.routeID == "" || tu.startDate == "" {
		return tripStart{}, false
	}
	return tripStart{route: tu.routeID, dir: tu.direction, start: secs, day: tu.startDate}, true
}

// matchUpdate finds the update for a timetabled trip: by trip ID, where an
// update without a start date matches any day, or else by route, direction
// and start.
func matchUpdate(byTrip map[string][]*tripUpdate, byStart map[tripStart]*tripUpdate, pl planned) *tripUpdate {
	day := pl.day.String()
	for _, tu := range byTrip[pl.trip] {
		if tu.startDate == "" || tu.startDate == day {
			return tu
		}
	}
	if len(byStart) == 0 {
		return nil
	}
	k := tripStart{route: pl.route, dir: int(pl.dir), start: pl.start, day: day}
	if tu, ok := byStart[k]; ok {
		return tu
	}
	k.dir = -1
	return byStart[k]
}

// predict returns a timetabled departure's predicted time from its trip
// update: the update for the stop, or the delay of the latest earlier stop,
// which GTFS-RT says carries forward. skipped is true when the update says
// the trip will not stop there.
func predict(st *static, tu *tripUpdate, pl planned, stop string) (at time.Time, skipped, ok bool) {
	var (
		delay    int32
		hasDelay bool
	)
	for _, stu := range tu.stops {
		here := stu.hasSeq && stu.seq == pl.seq || !stu.hasSeq && st.atStop(stu.stopID, stop)
		if !here {
			if stu.hasSeq && stu.seq < pl.seq && stu.relation != stopSkipped {
				delay, hasDelay = carriedDelay(stu)
				if stu.relation == stopNoData {
					hasDelay = false
				}
			}
			continue
		}
		switch stu.relation {
		case stopSkipped:
			return time.Time{}, true, false
		case stopNoData:
			return time.Time{}, false, false
		}
		ev := stu.departure
		if !ev.hasTime && !ev.hasDelay {
			ev = stu.arrival
		}
		switch {
		case ev.hasTime:
			return time.Unix(ev.time, 0), false, true
		case ev.hasDelay:
			return pl.at.Add(time.Duration(ev.delay) * time.Second), false, true
		}
		return time.Time{}, false, false
	}
	if hasDelay {
		return pl.at.Add(time.Duration(delay) * time.Second), false, true
	}
	return time.Time{}, false, false
}

// carriedDelay is the delay a stop's update passes on to later stops: its
// departure delay, or else its arrival delay.
func carriedDelay(stu stopTimeUpdate) (int32, bool) {
	if stu.departure.hasDelay {
		return stu.departure.delay, true
	}
	return stu.arrival.delay, stu.arrival.hasDelay
}

// duplicatedLines returns the lines on which realtime trips that should be
// in the schedule mostly matched none of its trips, so their timetabled
// trips are probably the same trips again. New York's subway, whose
// realtime trip IDs differ from its schedule's, is one such feed. Trips
// added in realtime are not expected in the schedule and do not count.
func duplicatedLines(merged, live []transit.Departure) map[string]bool {
	planned := map[string]bool{}
	matched := map[string]int{}
	for _, d := range merged {
		planned[d.Line.ID] = true
		if d.Realtime() {
			matched[d.Line.ID]++
		}
	}
	unmatched := map[string]int{}
	for _, d := range live {
		if planned[d.Line.ID] && d.Status != transit.StatusAdded {
			unmatched[d.Line.ID]++
		}
	}
	dup := map[string]bool{}
	for line, n := range unmatched {
		if n >= minUnmatched && n > matched[line] {
			dup[line] = true
		}
	}
	return dup
}

// minUnmatched is the fewest unmatched realtime trips on a line taken as a
// sign of a mismatch rather than a trip the schedule lacks.
const minUnmatched = 2

// departures returns the realtime departures in f at q's stop, leaving out
// the updates in used, which merge has already shown.
func departures(st *static, f feed, q transit.Query, used map[*tripUpdate]bool) []transit.Departure {
	var out []transit.Departure
	for ti := range f.trips {
		tu := &f.trips[ti]
		if tu.relation == tripDeleted || used[tu] {
			continue
		}
		for i, stu := range tu.stops {
			if !st.atStop(stu.stopID, q.StopID) || stu.relation == stopSkipped || stu.relation == stopNoData {
				continue
			}
			ev := stu.departure
			if !ev.hasTime {
				if i == len(tu.stops)-1 && len(tu.stops) > 1 {
					// The trip ends here: an arrival, not a departure. An
					// update listing a single stop, as feeds that publish
					// only the next stop do, says nothing about where the
					// trip ends, so its arrival is shown.
					continue
				}
				// Some feeds give only arrival times.
				ev = stu.arrival
			}
			if !ev.hasTime {
				continue
			}
			t := time.Unix(ev.time, 0).In(q.Now.Location())
			if !q.Contains(t) {
				continue
			}
			out = append(out, transit.Departure{
				Line:      st.line(*tu),
				Headsign:  st.headsign(*tu),
				StopID:    q.StopID,
				Scheduled: t.Add(-time.Duration(ev.delay) * time.Second),
				Predicted: &t,
				Status:    status(tu.relation),
			})
		}
	}
	return out
}

func status(tripRelation int) transit.Status {
	switch tripRelation {
	case tripCanceled:
		return transit.StatusCanceled
	case tripAdded, tripDuplicated, tripNew:
		return transit.StatusAdded
	default:
		return transit.StatusScheduled
	}
}

// serves reports whether any trip in f stops at stop.
func (f feed) serves(st *static, stop string) bool {
	for _, tu := range f.trips {
		for _, stu := range tu.stops {
			if st.atStop(stu.stopID, stop) {
				return true
			}
		}
	}
	return false
}

// atStop reports whether stopID is want or one of its platforms.
func (s *static) atStop(stopID, want string) bool {
	if stopID == want {
		return true
	}
	return s != nil && s.stops[stopID].parent == want
}

// line resolves a trip's route from the update, or from the schedule when
// the feed leaves route_id out.
func (s *static) line(tu tripUpdate) transit.Line {
	routeID := tu.routeID
	if s != nil && routeID == "" {
		routeID = s.trips[tu.tripID].routeID
	}
	return s.routeLine(routeID)
}

// routeLine is the schedule's line for a route ID, or a bare one.
func (s *static) routeLine(routeID string) transit.Line {
	if s != nil {
		if l, ok := s.routes[routeID]; ok {
			return l
		}
	}
	if routeID == "" {
		// Unscheduled trips may name no route; group them as one line.
		return transit.Line{ID: "unknown", Mode: transit.ModeOther}
	}
	return transit.Line{ID: routeID, ShortName: routeID, Mode: transit.ModeOther}
}

// headsign uses the schedule's trip headsign, or the name of the last stop
// in the update. Some agencies, like the New York subway, use realtime trip
// IDs that do not match their schedule.
func (s *static) headsign(tu tripUpdate) string {
	if s != nil {
		if h := s.trips[tu.tripID].headsign; h != "" {
			return h
		}
	}
	if len(tu.stops) == 0 {
		return ""
	}
	last := tu.stops[len(tu.stops)-1].stopID
	if s == nil {
		return last
	}
	return s.stopTitle(last)
}

// plannedHeadsign is a timetabled trip's headsign, or the name of its last
// stop, as for realtime trips.
func (s *static) plannedHeadsign(pl planned) string {
	if h := s.trips[pl.trip].headsign; h != "" {
		return h
	}
	return s.stopTitle(pl.last)
}

// stopTitle names a stop after its parent station when it has one.
func (s *static) stopTitle(id string) string {
	info, ok := s.stops[id]
	if !ok {
		return id
	}
	if parent, ok := s.stops[info.parent]; ok && parent.name != "" {
		return parent.name
	}
	return info.name
}

// staticFor returns the schedule data and the error of the latest
// download if it failed, in which case the previous data is kept. While
// earlier data exists every caller gets it at once; with none yet, callers
// wait for the download.
func (p *Provider) staticFor(ctx context.Context, now time.Time) (*static, error) {
	if p.schedule == nil {
		return nil, nil
	}
	return p.schedule.Get(ctx, now)
}

func (p *Provider) loadStatic(ctx context.Context, now time.Time) (*static, error) {
	var (
		st  *static
		err error
	)
	if p.cacheDir != "" {
		st, err = p.loadCachedStatic(ctx, now)
	} else {
		st, err = p.downloadStatic(ctx)
	}
	if st != nil {
		p.adopt(st)
	}
	if err != nil {
		var stale *upstream.StaleError
		if errors.As(err, &stale) {
			return st, &upstream.StaleError{Err: fmt.Errorf("schedule: %w", stale.Err)}
		}
		return nil, fmt.Errorf("schedule: %w", err)
	}
	return st, nil
}

func (p *Provider) currentStatic() *static {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

// timetableOn reports whether the configuration wants timetabled trips.
func (p *Provider) timetableOn() bool { return p.cfg.Timetable == nil || *p.cfg.Timetable }

// adopt makes st the current schedule: it gives st a timetable that keeps
// the schedule's file open, or closes the file, and closes the timetable of
// the schedule st replaces.
func (p *Provider) adopt(st *static) {
	if p.timetableOn() && st.file != nil && st.hasTimetable() {
		dir := ""
		if st.cached {
			dir = p.cacheDir
		}
		st.tt = newTimetable(st, st.file, dir, p.cfg.StaticURL, p.clock, p.log)
	} else {
		st.release()
		st.zr = nil
	}
	st.file = nil
	p.mu.Lock()
	old := p.current
	p.current = st
	p.mu.Unlock()
	if old != nil && old != st && old.tt != nil {
		old.tt.close()
	}
}

// downloadStatic fetches and parses the schedule zip. The zip goes to a
// temporary file rather than memory: large feeds are around 100 MB, and
// only a small part of them is kept. The file stays open for the timetable
// to read stop times from. Where no temporary file can be created, such as
// a read-only filesystem, it falls back to memory, without a timetable.
func (p *Provider) downloadStatic(ctx context.Context) (*static, error) {
	p.logDownloadStart(ctx)
	start := p.clock()
	f, err := os.CreateTemp("", "gtfsrt-static-*.zip")
	if err != nil {
		b, err := p.fetch(ctx, p.cfg.StaticURL, maxStaticBody)
		if err != nil {
			return nil, err
		}
		p.logDownloaded(ctx, int64(len(b)), start)
		st, err := parseStatic(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, err
		}
		if p.timetableOn() && st.hasTimetable() {
			p.log.WarnContext(ctx, "no temporary file for the GTFS schedule, so only realtime departures are shown", slog.String("url", redact(p.cfg.StaticURL)))
		}
		// Keeping the zip to read stop times would keep all of it in memory.
		st.zr = nil
		return st, nil
	}
	tf := newTempFile(f)
	size, err := p.fetchTo(ctx, p.cfg.StaticURL, nil, maxStaticBody, to(f))
	if err != nil {
		_ = tf.Close()
		return nil, err
	}
	p.logDownloaded(ctx, size, start)
	st, err := parseStatic(f, size)
	if err != nil {
		_ = tf.Close()
		return nil, err
	}
	st.file = tf
	return st, nil
}

// tempFile is an open temporary file, deleted at once where the system
// allows deleting an open file, so it cannot outlive the process, and
// otherwise when closed.
type tempFile struct {
	*os.File
	removed bool
}

func newTempFile(f *os.File) *tempFile {
	return &tempFile{File: f, removed: os.Remove(f.Name()) == nil}
}

func (t *tempFile) Close() error {
	err := t.File.Close()
	if !t.removed {
		_ = os.Remove(t.Name())
	}
	return err
}

func (p *Provider) logDownloadStart(ctx context.Context) {
	p.log.InfoContext(ctx, "downloading GTFS schedule", slog.String("url", redact(p.cfg.StaticURL)))
}

func (p *Provider) logDownloaded(ctx context.Context, size int64, start time.Time) {
	p.log.InfoContext(ctx, "downloaded GTFS schedule",
		slog.String("url", redact(p.cfg.StaticURL)),
		slog.Int64("bytes", size),
		slog.Duration("took", p.clock().Sub(start).Round(time.Millisecond)))
}

func (p *Provider) fetchFeed(ctx context.Context, u string) (feed, error) {
	b, err := p.fetch(ctx, u, maxFeedBody)
	if err != nil {
		return feed{}, err
	}
	f, err := decodeFeed(b)
	if err != nil {
		return feed{}, fmt.Errorf("%s: %s: %w", Name, redact(u), err)
	}
	return f, nil
}

func (p *Provider) fetch(ctx context.Context, u string, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := p.fetchTo(ctx, u, nil, limit, to(&buf)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// to sends every 200 response's body to w.
func to(w io.Writer) func(int, http.Header) (io.Writer, error) {
	return func(int, http.Header) (io.Writer, error) { return w, nil }
}

// fetchTo copies the body of a GET, sent with the extra headers, to the
// writer that dest picks from the response's status and headers, failing
// above limit bytes. dest sees 200 responses, and 304 ones when extra makes
// the request conditional. Errors name the URL without its query string, which may hold
// an API key.
func (p *Provider) fetchTo(ctx context.Context, u string, extra http.Header, limit int64, dest func(status int, h http.Header) (io.Writer, error)) (int64, error) {
	shown := redact(u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("%s: GET %s: invalid request", Name, shown)
	}
	req.Header.Set("User-Agent", upstream.UserAgent)
	for k, v := range p.cfg.Headers {
		req.Header.Set(k, v)
	}
	for k := range extra {
		req.Header.Set(k, extra.Get(k))
	}
	resp, err := p.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, fmt.Errorf("%s: GET %s: %w", Name, shown, err)
	}
	defer resp.Body.Close()
	conditional := len(extra) > 0 && resp.StatusCode == http.StatusNotModified
	if resp.StatusCode != http.StatusOK && !conditional {
		upstream.Drain(resp.Body)
		return 0, fmt.Errorf("%s: GET %s: %s", Name, shown, resp.Status)
	}
	w, err := dest(resp.StatusCode, resp.Header)
	if err != nil {
		upstream.Drain(resp.Body)
		return 0, fmt.Errorf("%s: GET %s: %w", Name, shown, err)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, fmt.Errorf("%s: GET %s: %w", Name, shown, err)
	}
	if n > limit {
		return 0, fmt.Errorf("%s: GET %s: response over %d MB", Name, shown, limit>>20)
	}
	return n, nil
}

// redact drops the credentials and query string from u for messages.
func redact(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return "(invalid URL)"
	}
	p.User = nil
	if p.RawQuery != "" {
		p.RawQuery = "..."
	}
	p.Fragment = ""
	return p.String()
}

// SearchStops implements transit.StopSearcher using the schedule's stops.
// Platforms with a parent station are left out unless searched by ID, so a
// station appears once; boards on a station see all its platforms.
func (p *Provider) SearchStops(ctx context.Context, search string) ([]transit.Stop, error) {
	if p.cfg.StaticURL == "" {
		return nil, fmt.Errorf("%s: stop search needs static_url", Name)
	}
	st, err := p.staticFor(ctx, p.clock())
	if st == nil {
		if err == nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("%s: stop search: %w", Name, err)
	}
	search = strings.TrimSpace(search)
	if info, ok := st.stops[search]; ok {
		return []transit.Stop{{ID: search, Name: info.name}}, nil
	}
	var out []transit.Stop
	for id, info := range st.stops {
		if info.parent != "" && st.stops[info.parent].name != "" {
			continue
		}
		if transit.MatchesSearch(info.name, search) {
			out = append(out, transit.Stop{ID: id, Name: info.name})
		}
	}
	slices.SortFunc(out, func(a, b transit.Stop) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return out, nil
}

// StopName implements transit.StopNamer from the schedule, loading it if
// needed.
func (p *Provider) StopName(ctx context.Context, id string, now time.Time) (string, bool) {
	st, _ := p.staticFor(ctx, now)
	if st == nil {
		return "", false
	}
	info, ok := st.stops[id]
	return info.name, ok && info.name != ""
}
