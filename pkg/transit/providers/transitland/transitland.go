// Package transitland reads departures from the Transitland v2 REST API,
// which merges static GTFS and GTFS-RT for thousands of agencies. It needs
// an API key from https://www.transit.land/.
//
// Every request counts against a fixed monthly quota (10,000 on the free
// plan, 50,000 on the Hobbyist/Academic plan), so the provider fetches each
// stop once per refresh interval and answers polls in between from that
// response. Boards still tick at their own poll interval, so minutes count
// down between fetches.
package transitland

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/internal/upstream"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/registry"
)

// Name is the provider's name in configuration.
const Name = "transitland"

// DefaultBaseURL is the public REST API.
const DefaultBaseURL = "https://transit.land/api/v2/rest"

// keyHeader carries the API key.
const keyHeader = "apikey"

const (
	// DefaultRefreshInterval keeps one stop polled all day within the free
	// plan's 10,000 requests a month.
	DefaultRefreshInterval = 5 * time.Minute
	// MinRefreshInterval stops a typo from spending a month's quota in a
	// day.
	MinRefreshInterval = time.Minute
	// MaxRefreshInterval bounds how old a prediction shown as live can be,
	// together with the board's poll interval: answers do not carry their
	// age, and the next fetch waits for a poll.
	MaxRefreshInterval = 10 * time.Minute
	// DefaultMonthlyQuota is the free plan's REST quota.
	DefaultMonthlyQuota = 10_000

	// failureShared lets boards on one stop share a failed request. It must
	// end before the scheduler's first retry, two poll intervals later, or
	// that retry gets the remembered failure and backs off further.
	failureShared = 10 * time.Second

	searchLimit = 50
	// maxStopLines is how many stops one StopLines call asks about, each
	// costing a request.
	maxStopLines = 10
	maxBody      = 16 << 20
)

// Config is the provider's configuration block.
type Config struct {
	// APIKey is required; use ${NAME} to read it from the environment.
	APIKey string `toml:"api_key"`
	// Feed is a feed Onestop ID, such as "f-drt-mbta". When set, stops
	// are that feed's GTFS stop IDs and search is limited to the feed;
	// otherwise stops are full Transitland stop keys ("<feed>:<stop_id>").
	Feed string `toml:"feed"`
	// RefreshInterval is the shortest time between requests for one stop,
	// such as "2m", from MinRefreshInterval to MaxRefreshInterval. Defaults
	// to DefaultRefreshInterval.
	RefreshInterval string `toml:"refresh_interval"`
	// MonthlyQuota is the plan's monthly REST quota, used to warn about
	// configurations that would exceed it. Defaults to DefaultMonthlyQuota.
	MonthlyQuota int `toml:"monthly_quota"`
	// BaseURL overrides the API address, for tests or a mirror.
	BaseURL string `toml:"base_url"`
}

// Provider reads departures from Transitland. It is safe for concurrent
// use.
type Provider struct {
	base    *url.URL
	http    *http.Client
	key     string
	feed    string
	refresh time.Duration
	// quota is zero when the configuration does not set it.
	quota int

	mu sync.Mutex
	// stops holds one cache per stop key, so boards polling the same stop
	// share each request.
	stops map[string]stopCache
	// offsets holds each stop key's service-day offset.
	offsets map[string]serviceOffset
	// names holds each stop key's name, as departures answers give it.
	names map[string]string
	// lines holds each stop key's lines, as StopLines found them.
	lines map[string][]transit.Line
}

// stopCache caches a stop's answers for window, the widest asked for, which
// cover the narrower ones.
type stopCache struct {
	*upstream.Cache[timetable]
	window time.Duration
}

// Register adds the provider to r.
func Register(r *registry.Registry) {
	registry.RegisterConfig(r, Name, func(cfg Config) (*Provider, error) { return New(cfg, nil) })
}

// New returns a provider. A nil client gets a default with a timeout.
func New(cfg Config, client *http.Client) (*Provider, error) {
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return nil, errors.New("api_key is required")
	}
	u, err := upstream.HTTPURL("base_url", strings.TrimRight(cmp.Or(cfg.BaseURL, DefaultBaseURL), "/"))
	if err != nil {
		return nil, err
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("base_url must not have credentials, a query or a fragment")
	}
	refresh := DefaultRefreshInterval
	if cfg.RefreshInterval != "" {
		d, err := time.ParseDuration(cfg.RefreshInterval)
		if err != nil {
			return nil, fmt.Errorf("refresh_interval %q: %w", cfg.RefreshInterval, err)
		}
		refresh = d
	}
	if refresh < MinRefreshInterval {
		return nil, fmt.Errorf("refresh_interval must be at least %s", MinRefreshInterval)
	}
	if refresh > MaxRefreshInterval {
		return nil, fmt.Errorf("refresh_interval must be at most %s", MaxRefreshInterval)
	}
	if cfg.MonthlyQuota < 0 {
		return nil, errors.New("monthly_quota must not be negative")
	}
	return &Provider{
		base:    u,
		http:    upstream.KeepHeadersOnHost(upstream.ClientOrDefault(client), keyHeader),
		key:     key,
		feed:    strings.TrimSpace(cfg.Feed),
		refresh: refresh,
		quota:   cfg.MonthlyQuota,
		stops:   map[string]stopCache{},
		names:   map[string]string{},
		lines:   map[string][]transit.Line{},
		offsets: map[string]serviceOffset{},
	}, nil
}

// Quota implements transit.Metered. Quotas belong to API keys, so Account
// identifies the key.
func (p *Provider) Quota() transit.Quota {
	sum := sha256.Sum256([]byte(p.base.String() + "\x00" + p.key))
	return transit.Quota{
		Account:         Name + ":" + hex.EncodeToString(sum[:]),
		Limit:           p.quota,
		DefaultLimit:    DefaultMonthlyQuota,
		RefreshInterval: p.refresh,
	}
}

// Departures implements transit.Provider.
func (p *Provider) Departures(ctx context.Context, q transit.Query) ([]transit.Departure, error) {
	// A fresh cache would answer without touching ctx.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tt, err := p.cacheFor(q).Get(ctx, q.Now)
	if err != nil {
		return nil, err
	}
	out := make([]transit.Departure, 0, len(tt.deps))
	for _, d := range tt.deps {
		if !q.Contains(d.Time()) {
			continue
		}
		d.StopID = q.StopID
		d.Scheduled = d.Scheduled.In(q.Now.Location())
		if d.Predicted != nil {
			t := d.Predicted.In(q.Now.Location())
			d.Predicted = &t
		}
		out = append(out, d)
	}
	transit.SortByTime(out)
	if tt.until.IsZero() || !tt.until.Before(q.Now.Add(q.Window)) {
		return out, nil
	}
	err = fmt.Errorf("%s: %s has more departures than one request returns; shorten window or refresh_interval", Name, p.stopKey(q.StopID))
	// Empty because the list was cut off does not mean there is no service.
	if len(out) == 0 {
		return nil, err
	}
	return out, &transit.PartialError{Err: err}
}

func (p *Provider) cacheFor(q transit.Query) stopCache {
	key := p.stopKey(q.StopID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.stops[key]; ok && c.window >= q.Window {
		return c
	}
	// Fetching one refresh interval past the window keeps the end of the
	// window filled until the next fetch.
	span := q.Window + p.refresh
	c := stopCache{
		Cache: upstream.NewCacheAt(upstream.Policy{TTL: p.refresh, Retry: failureShared}, func(ctx context.Context, now time.Time) (timetable, error) {
			return p.fetch(ctx, key, now, span)
		}),
		window: q.Window,
	}
	p.stops[key] = c
	return c
}

// stopKey turns a configured stop into a Transitland stop key.
func (p *Provider) stopKey(stop string) string {
	if p.feed == "" || strings.HasPrefix(stop, p.feed+":") {
		return stop
	}
	return p.feed + ":" + stop
}

func (p *Provider) fetch(ctx context.Context, key string, now time.Time, span time.Duration) (timetable, error) {
	var resp departuresResponse
	if err := p.get(ctx, "stops/"+url.PathEscape(key)+"/departures", p.departureParams(key, now, span), &resp); err != nil {
		return timetable{}, err
	}
	// The API answers an unknown stop with 200 and no stops.
	if len(resp.Stops) == 0 {
		return timetable{}, fmt.Errorf("%w %q", transit.ErrUnknownStop, key)
	}
	if name := resp.Stops[0].StopName; name != "" {
		p.mu.Lock()
		p.names[key] = name
		p.mu.Unlock()
	}
	lists := resp.lists()
	p.learnOffset(key, lists, now)
	return newTimetable(lists), nil
}

// SearchStops implements transit.StopSearcher with Transitland's full-text
// stop search, limited to the configured feed if there is one. Each request
// counts against the quota.
//
// Transitland matches several words as one phrase, so "clark lake" misses
// "Clark/Lake", and it keeps apostrophes, so "kings cross" misses "King's
// Cross". When the whole search finds nothing, its longest words are tried
// alone, at most two more requests, keeping the stops that match every word
// as other providers' searches do.
func (p *Provider) SearchStops(ctx context.Context, search string) ([]transit.Stop, error) {
	search = strings.TrimSpace(search)
	if search == "" {
		return nil, nil
	}
	stops, err := p.searchStops(ctx, search)
	words := strings.Fields(search)
	if err != nil || len(stops) > 0 || len(words) < 2 {
		return stops, err
	}
	slices.SortStableFunc(words, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, w := range words[:2] {
		found, err := p.searchStops(ctx, w)
		if err != nil {
			return nil, err
		}
		found = slices.DeleteFunc(found, func(s transit.Stop) bool { return !transit.MatchesSearch(s.Name, search) })
		if len(found) > 0 {
			return found, nil
		}
	}
	return nil, nil
}

func (p *Provider) searchStops(ctx context.Context, search string) ([]transit.Stop, error) {
	params := url.Values{
		"search": {search},
		"limit":  {strconv.Itoa(searchLimit)},
	}
	if p.feed != "" {
		params.Set("feed_onestop_id", p.feed)
	}
	var resp struct {
		Stops []struct {
			StopID      string `json:"stop_id"`
			StopName    string `json:"stop_name"`
			FeedVersion struct {
				Feed struct {
					OnestopID string `json:"onestop_id"`
				} `json:"feed"`
			} `json:"feed_version"`
		} `json:"stops"`
	}
	if err := p.get(ctx, "stops", params, &resp); err != nil {
		return nil, err
	}
	out := make([]transit.Stop, 0, len(resp.Stops))
	for _, s := range resp.Stops {
		id := s.StopID
		if feed := s.FeedVersion.Feed.OnestopID; p.feed == "" && feed != "" {
			id = feed + ":" + id
		}
		out = append(out, transit.Stop{ID: id, Name: s.StopName})
	}
	return out, nil
}

// StopName implements transit.StopNamer with the names departures answers
// gave, so it never spends a request: a stop not yet fetched is unknown.
func (p *Provider) StopName(_ context.Context, id string, _ time.Time) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	name, ok := p.names[p.stopKey(id)]
	return name, ok
}

// StopLines implements transit.StopLiner with one request for each stop
// not asked about before, each counting against the quota, for the first
// maxStopLines stops. Transitland lists no lines for a station, only for
// its platforms.
func (p *Provider) StopLines(ctx context.Context, ids []string, _ time.Time) (map[string][]transit.Line, error) {
	var (
		mu   sync.Mutex
		out  = make(map[string][]transit.Line, len(ids))
		errs []error
		wg   sync.WaitGroup
	)
	for _, id := range ids[:min(len(ids), maxStopLines)] {
		key := p.stopKey(id)
		p.mu.Lock()
		lines, ok := p.lines[key]
		p.mu.Unlock()
		if ok {
			if len(lines) > 0 {
				out[id] = lines
			}
			continue
		}
		wg.Go(func() {
			lines, err := p.stopLines(ctx, key)
			if err == nil {
				p.mu.Lock()
				p.lines[key] = lines
				p.mu.Unlock()
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				errs = append(errs, err)
			case len(lines) > 0:
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

// stopLines asks for key's lines. Routes have no onestop_id in this answer,
// and a key can match the stop in more than one feed, so lines are told
// apart by name.
func (p *Provider) stopLines(ctx context.Context, key string) ([]transit.Line, error) {
	var resp struct {
		Stops []struct {
			RouteStops []struct {
				Route struct {
					RouteID   string `json:"route_id"`
					ShortName string `json:"route_short_name"`
					LongName  string `json:"route_long_name"`
				} `json:"route"`
			} `json:"route_stops"`
		} `json:"stops"`
	}
	if err := p.get(ctx, "stops/"+url.PathEscape(key), nil, &resp); err != nil {
		return nil, err
	}
	lines := []transit.Line{}
	for _, st := range resp.Stops {
		for _, rs := range st.RouteStops {
			r := rs.Route
			name := cmp.Or(r.ShortName, r.LongName)
			if name != "" && !slices.ContainsFunc(lines, func(l transit.Line) bool { return l.ShortName == name }) {
				lines = append(lines, transit.Line{ID: r.RouteID, ShortName: name})
			}
		}
	}
	slices.SortFunc(lines, transit.CompareLines)
	return lines, nil
}

// get requests path, which must already be escaped, below the base URL.
func (p *Provider) get(ctx context.Context, path string, params url.Values, v any) error {
	u := p.base.JoinPath(path)
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", upstream.UserAgent)
	req.Header.Set(keyHeader, p.key)
	resp, err := p.http.Do(req)
	if err != nil {
		// The URL holds no secret: the key travels in a header.
		return fmt.Errorf("%s: %w", Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		upstream.Drain(resp.Body)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: %s: check api_key", Name, resp.Status)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s: %s: rate limit or monthly quota reached; raise refresh_interval or narrow active_hours", Name, resp.Status)
	default:
		return fmt.Errorf("%s: GET %s: %s", Name, path, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v); err != nil {
		return fmt.Errorf("%s: decode %s: %w", Name, path, err)
	}
	return nil
}
