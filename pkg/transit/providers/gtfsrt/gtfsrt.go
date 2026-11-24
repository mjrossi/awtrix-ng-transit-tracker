// Package gtfsrt is the generic provider for any agency that publishes a
// GTFS schedule feed and GTFS-Realtime TripUpdates. It serves agencies
// through configuration alone, so they need no code of their own.
//
// The static feed supplies line names and colors, station structure and
// headsigns; the realtime feeds supply departure times. Only departures
// with realtime predictions are shown, and stop time updates that carry a
// delay but no absolute time are skipped, because resolving them needs the
// full timetable.
package gtfsrt

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/internal/upstream"
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
	RealtimeURLs []string `toml:"realtime_urls"`
	// Headers are sent with every request, for feeds that need an API key.
	Headers map[string]string `toml:"headers"`
}

// Provider reads one agency's feeds. It is safe for concurrent use: boards
// sharing a provider share one download of each feed.
type Provider struct {
	cfg   Config
	http  *http.Client
	clock func() time.Time

	// schedule is nil without a static_url.
	schedule *upstream.Cache[*static]
	// feeds holds one cache per realtime URL. Failures are cached for
	// feedTTL too, so boards sharing a failing feed do not each retry it.
	feeds map[string]*upstream.Cache[feed]
}

// Register adds the provider to r.
func Register(r *transit.Registry) {
	transit.RegisterConfig(r, Name, func(cfg Config) (*Provider, error) { return New(cfg, nil) })
}

// New validates cfg and returns a provider. A nil client gets a default;
// requests are bounded by per-request timeouts either way.
func New(cfg Config, client *http.Client) (*Provider, error) {
	if len(cfg.RealtimeURLs) == 0 {
		return nil, errors.New("realtime_urls: at least one GTFS-RT TripUpdates URL is required")
	}
	for i, u := range cfg.RealtimeURLs {
		if _, ok := upstream.HTTPURL(u); !ok {
			return nil, fmt.Errorf("realtime_urls[%d] must be an http(s) URL", i)
		}
	}
	if _, ok := upstream.HTTPURL(cfg.StaticURL); cfg.StaticURL != "" && !ok {
		return nil, errors.New("static_url must be an http(s) URL")
	}
	if client == nil {
		client = &http.Client{}
	}
	p := &Provider{cfg: cfg, http: client, clock: time.Now, feeds: map[string]*upstream.Cache[feed]{}}
	if cfg.StaticURL != "" {
		// A large schedule can take minutes to download, longer than a
		// board's app lifetime, so stale data is refreshed in the
		// background.
		p.schedule = upstream.NewCache(upstream.Policy{TTL: staticTTL, Retry: staticRetry, Background: true, Timeout: staticTimeout}, p.loadStatic)
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
//
// When some realtime feeds fail, departures from the rest come back with a
// *transit.PartialError, unless a failed feed is the only one known to
// serve the stop; showing its departures as missing would mislead. A failed
// schedule download also gives a partial result, with bare IDs if no
// earlier schedule is cached.
func (p *Provider) Departures(ctx context.Context, q transit.Query) ([]transit.Departure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, staticErr := p.staticFor(ctx, q.Now)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var (
		out            []transit.Departure
		problems       []error
		loaded         int
		servedByOK     bool
		servedByFailed bool
	)
	for _, u := range p.cfg.RealtimeURLs {
		f, last, err := p.feedFor(ctx, u, q.Now)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			problems = append(problems, err)
			servedByFailed = servedByFailed || last.serves(st, q.StopID)
			continue
		}
		loaded++
		servedByOK = servedByOK || f.serves(st, q.StopID)
		out = append(out, departures(st, f, q)...)
	}
	if loaded == 0 || servedByFailed && !servedByOK {
		return nil, errors.Join(problems...)
	}

	out = transit.SortAndLimit(out, q.Limit)
	if staticErr != nil {
		problems = append(problems, staticErr)
	}
	if len(problems) > 0 {
		return out, &transit.PartialError{Err: errors.Join(problems...)}
	}
	return out, nil
}

func departures(st *static, f feed, q transit.Query) []transit.Departure {
	end := q.Now.Add(q.Window)
	var out []transit.Departure
	for _, tu := range f.trips {
		if tu.relation == tripDeleted {
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
			if t.Before(q.Now) || t.After(end) {
				continue
			}
			out = append(out, transit.Departure{
				Line:      st.line(tu),
				Headsign:  st.headsign(tu),
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
	if s != nil {
		if routeID == "" {
			routeID = s.trips[tu.tripID].routeID
		}
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
	info, ok := s.stops[last]
	if !ok {
		return last
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

func (p *Provider) loadStatic(ctx context.Context) (*static, error) {
	st, err := p.downloadStatic(ctx)
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	return st, nil
}

// downloadStatic fetches and parses the schedule zip. The zip goes to a
// temporary file rather than memory: large feeds are around 100 MB, and
// only a small part of them is kept. Where no temporary file can be
// created, such as a read-only filesystem, it falls back to memory.
func (p *Provider) downloadStatic(ctx context.Context) (*static, error) {
	f, err := os.CreateTemp("", "gtfsrt-static-*.zip")
	if err != nil {
		b, err := p.fetch(ctx, p.cfg.StaticURL, maxStaticBody)
		if err != nil {
			return nil, err
		}
		return parseStatic(bytes.NewReader(b), int64(len(b)))
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	size, err := p.fetchTo(ctx, p.cfg.StaticURL, f, maxStaticBody)
	if err != nil {
		return nil, err
	}
	return parseStatic(f, size)
}

// feedFor returns the decoded feed at u, fetching it at most once per
// feedTTL however many boards ask. On failure it returns the error and the
// last feed that decoded, which is empty if none has.
func (p *Provider) feedFor(ctx context.Context, u string, now time.Time) (f, last feed, err error) {
	last, err = p.feeds[u].Get(ctx, now)
	if err != nil {
		return feed{}, last, err
	}
	return last, last, nil
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
	if _, err := p.fetchTo(ctx, u, &buf, limit); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fetchTo copies the body of a GET to w, failing above limit bytes. Errors
// name the URL without its query string, which may hold an API key.
func (p *Provider) fetchTo(ctx context.Context, u string, w io.Writer, limit int64) (int64, error) {
	shown := redact(u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("%s: GET %s: invalid request", Name, shown)
	}
	req.Header.Set("User-Agent", "urbanist-timetable")
	for k, v := range p.cfg.Headers {
		req.Header.Set(k, v)
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
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return 0, fmt.Errorf("%s: GET %s: %s", Name, shown, resp.Status)
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
