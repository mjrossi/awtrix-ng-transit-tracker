// Package gtfsrt is the generic provider for any agency that publishes a
// GTFS schedule feed, GTFS-Realtime TripUpdates, or both. It serves
// agencies through configuration alone, so they need no code of their own.
//
// The static feed supplies line names and colors, station structure,
// headsigns and the timetable; the realtime feeds supply predictions. Trips
// in the timetable are shown with their prediction when a feed has one and
// at their scheduled time otherwise, so a board is not empty while no
// vehicle is near. Either feed may be left out: realtime only, or a
// schedule alone for agencies that publish no TripUpdates.
package gtfsrt

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
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
	if len(cfg.RealtimeURLs) == 0 && cfg.Timetable != nil && !*cfg.Timetable {
		return nil, errors.New("timetable = false needs realtime_urls, or nothing is left to show")
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
		p.schedule = upstream.NewCacheAt(upstream.Policy{TTL: staticTTL, Retry: staticRetry, Background: true, Timeout: staticTimeout}, p.loadStatic).
			ExpireWith(scheduleFreshUntil)
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
	feeds, problems, servedByOK, servedByFailed, err := p.readFeeds(ctx, st, q)
	if err != nil {
		return nil, err
	}
	st, plans, hasTimes, ttErr := p.timetabled(ctx, st, q)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o := outcome{
		realtime: len(p.cfg.RealtimeURLs) > 0, schedule: st != nil, scheduleErr: staticErr,
		feedsLoaded: len(feeds), feedProblems: problems, servedByOK: servedByOK, servedByFailed: servedByFailed,
		hasTimes: hasTimes, timetableErr: ttErr,
	}
	if st != nil {
		_, o.stopKnown = st.stops[q.StopID]
		o.noTimetable = st.tt == nil
	}
	if err := o.failure(q.StopID); err != nil {
		return nil, err
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
	if partial := o.problems(); len(partial) > 0 {
		return out, &transit.PartialError{Err: errors.Join(partial...)}
	}
	return out, nil
}

// outcome is what Departures learned while reading a query's sources.
type outcome struct {
	realtime     bool // realtime_urls are configured
	schedule     bool // a schedule, current or earlier, is loaded
	scheduleErr  error
	feedsLoaded  int
	feedProblems []error
	// servedByOK and servedByFailed report whether a feed that loaded, or
	// one that failed, is known to serve the stop.
	servedByOK, servedByFailed bool
	hasTimes                   bool // the timetable has stop times at the stop
	stopKnown                  bool // the schedule has the stop
	timetableErr               error
	noTimetable                bool // the schedule has no timetable
}

// failure returns the error to give instead of departures, or nil when
// departures can be shown. Where a source that could not be read is the only
// one for the stop, an empty board would claim there are no departures.
func (o outcome) failure(stop string) error {
	switch {
	case !o.schedule && o.scheduleErr != nil && !o.realtime:
		return o.scheduleErr
	case !o.hasTimes && o.realtime && (o.feedsLoaded == 0 || o.servedByFailed && !o.servedByOK):
		return errors.Join(append(slices.Clone(o.feedProblems), o.timetableErr)...)
	// Only a complete answer can tell a stop does not exist: the schedule
	// and every feed loaded, the schedule lacks it and no trip stops there.
	case o.schedule && o.scheduleErr == nil && len(o.feedProblems) == 0 && !o.servedByOK && !o.hasTimes && !o.stopKnown:
		return fmt.Errorf("%w %q", transit.ErrUnknownStop, stop)
	case o.schedule && !o.realtime && o.timetableErr != nil:
		return errors.Join(o.timetableErr, o.scheduleErr)
	case o.schedule && !o.realtime && o.noTimetable:
		return errors.Join(fmt.Errorf("%s: %w", Name, errNoTimetable), o.scheduleErr)
	}
	return nil
}

// problems lists what went wrong without stopping departures being shown.
func (o outcome) problems() []error {
	out := slices.Clone(o.feedProblems)
	for _, err := range []error{o.scheduleErr, o.timetableErr} {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}

// readFeeds gets every realtime feed, and reports whether one that loaded,
// or one that failed, is known to serve q.StopID. It fails only when ctx
// ends; a feed's failure is one of the problems.
func (p *Provider) readFeeds(ctx context.Context, st *static, q transit.Query) (feeds []feed, problems []error, servedByOK, servedByFailed bool, err error) {
	for _, u := range p.cfg.RealtimeURLs {
		// On failure, f is the last feed that decoded, or empty if none has.
		f, err := p.feeds[u].Get(ctx, q.Now)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, false, false, ctx.Err()
			}
			problems = append(problems, err)
			servedByFailed = servedByFailed || f.serves(st, q.StopID)
			continue
		}
		servedByOK = servedByOK || f.serves(st, q.StopID)
		feeds = append(feeds, f)
	}
	return feeds, problems, servedByOK, servedByFailed, nil
}

// timetabled returns the trips the timetable plans at q.StopID, with the
// schedule they came from, which a refresh may have replaced, and whether
// the stop has any stop times at all.
func (p *Provider) timetabled(ctx context.Context, st *static, q transit.Query) (_ *static, plans []planned, hasTimes bool, err error) {
	if st == nil || st.tt == nil || !p.timetableOn() {
		return st, nil, false, nil
	}
	// Trips are looked for from a while before q.Now, since a late one can
	// still be ahead, and a little after the window for early ones.
	from, to := q.Now.Add(-maxDelay), q.Now.Add(q.Window+maxEarly)
	st, sts, err := p.stopTimes(ctx, st, q.StopID)
	if err != nil {
		return st, nil, false, fmt.Errorf("timetable: %w", err)
	}
	plans = st.planned(sts, from, to)
	return st, plans, len(sts) > 0, nil
}

// stopTimes reads id's stop times from st's timetable, or from the current
// schedule's when a refresh replaced st during the read, and returns the
// schedule they came from.
func (p *Provider) stopTimes(ctx context.Context, st *static, id string) (*static, []stopTime, error) {
	sts, err := st.tt.stopTimes(ctx, id)
	if errors.Is(err, errTimetableClosed) {
		if cur := p.currentStatic(); cur != st && cur.tt != nil {
			st = cur
			sts, err = st.tt.stopTimes(ctx, id)
		}
	}
	return st, sts, err
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
		// Download errors name the provider already, as "schedule: gtfsrt:
		// GET ...".
		return nil, fmt.Errorf("stop search: %w", err)
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

// StopLines implements transit.StopLiner from the timetable. It fails
// without one: no static_url, a schedule without stop times, one that could
// not be kept in a temporary file, or timetable = false without the
// schedule cache.
func (p *Provider) StopLines(ctx context.Context, ids []string, now time.Time) (map[string][]transit.StopLine, error) {
	st, err := p.staticFor(ctx, now)
	if st == nil {
		return nil, cmp.Or(err, ctx.Err(), errNoTimetable)
	}
	if st.tt == nil {
		if !p.timetableOn() {
			return nil, fmt.Errorf("%s: with timetable = false, stop lines need the schedule cache", Name)
		}
		return nil, fmt.Errorf("%s: stop lines need the schedule's timetable", Name)
	}
	var (
		mu   sync.Mutex
		out  = make(map[string][]transit.StopLine, len(ids))
		errs []error
		wg   sync.WaitGroup
	)
	// The timetable gathers stops asked for within scanGather into one scan
	// of stop_times.txt, so these usually share it.
	for _, id := range ids {
		wg.Go(func() {
			from, sts, err := p.stopTimes(ctx, st, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if lines := from.stopLines(sts); len(lines) > 0 {
				out[id] = lines
			}
		})
	}
	wg.Wait()
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// stopLines lists the lines of sts's trips once each, with where they go.
func (s *static) stopLines(sts []stopTime) []transit.StopLine {
	var lines []transit.StopLine
	for _, t := range sts {
		if routeID := s.trips[t.trip].routeID; routeID != "" {
			lines = transit.AddStopLine(lines, s.routeLine(routeID), s.tripHeadsign(t.trip, t.last))
		}
	}
	slices.SortFunc(lines, func(a, b transit.StopLine) int { return transit.CompareLines(a.Line, b.Line) })
	return lines
}
