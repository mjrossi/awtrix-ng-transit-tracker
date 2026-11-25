// Package transitland reads departures from the Transitland v2 REST API,
// which merges static GTFS and GTFS-RT for thousands of agencies. It needs
// an API key from https://www.transit.land/.
//
// Every request counts against a fixed monthly quota (10,000 on the free
// plan, 50,000 on the Hobbyist/Academic plan), so the provider fetches each
// stop at most once per refresh interval and answers polls in between from
// that response. Boards still tick at their own poll interval, so minutes
// count down between fetches; predictions are up to one interval old.
package transitland

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/internal/upstream"
)

// Name is the provider's name in configuration.
const Name = "transitland"

// DefaultBaseURL is the public REST API.
const DefaultBaseURL = "https://transit.land/api/v2/rest"

const (
	// DefaultRefreshInterval keeps one stop polled all day within the free
	// plan's 10,000 requests a month.
	DefaultRefreshInterval = 5 * time.Minute
	// MinRefreshInterval stops a typo from spending a month's quota in a
	// day.
	MinRefreshInterval = time.Minute
	// DefaultMonthlyQuota is the free plan's REST quota.
	DefaultMonthlyQuota = 10_000

	// departureLimit caps departures per stop and per child platform. It
	// bounds the response, not what boards show.
	departureLimit = 50
	searchLimit    = 50
	maxBody        = 16 << 20
)

// ErrUnknownStop is returned when Transitland does not know a stop key.
var ErrUnknownStop = errors.New("unknown stop")

// Config is the provider's configuration block.
type Config struct {
	// APIKey is required; use ${NAME} to read it from the environment.
	APIKey string `toml:"api_key"`
	// Feed is a feed Onestop ID, such as "f-9q9-bart". When set, stops
	// are that feed's GTFS stop IDs and search is limited to the feed;
	// otherwise stops are full Transitland stop keys ("<feed>:<stop_id>").
	Feed string `toml:"feed"`
	// RefreshInterval is the shortest time between requests for one stop,
	// such as "2m". Defaults to DefaultRefreshInterval.
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
	quota   int

	mu sync.Mutex
	// stops holds one cache per stop and window, so boards polling the same
	// stop share each request.
	stops map[stopWindow]*upstream.Cache[[]transit.Departure]
}

type stopWindow struct {
	key    string
	window time.Duration
}

// Register adds the provider to r.
func Register(r *transit.Registry) {
	transit.RegisterConfig(r, Name, func(cfg Config) (*Provider, error) { return New(cfg, nil) })
}

// New returns a provider. A nil client gets a default with a timeout.
func New(cfg Config, client *http.Client) (*Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("api_key is required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, ok := upstream.HTTPURL(base)
	if !ok {
		return nil, fmt.Errorf("base_url %q must be an http(s) URL", base)
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
	quota := cfg.MonthlyQuota
	switch {
	case quota < 0:
		return nil, errors.New("monthly_quota must not be negative")
	case quota == 0:
		quota = DefaultMonthlyQuota
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Provider{
		base:    u,
		http:    client,
		key:     cfg.APIKey,
		feed:    strings.TrimSpace(cfg.Feed),
		refresh: refresh,
		quota:   quota,
		stops:   map[stopWindow]*upstream.Cache[[]transit.Departure]{},
	}, nil
}

// RefreshInterval implements transit.Metered.
func (p *Provider) RefreshInterval() time.Duration { return p.refresh }

// MonthlyQuota implements transit.Metered.
func (p *Provider) MonthlyQuota() int { return p.quota }

// Departures implements transit.Provider.
func (p *Provider) Departures(ctx context.Context, q transit.Query) ([]transit.Departure, error) {
	// A fresh cache would answer without touching ctx.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	all, err := p.cacheFor(q).Get(ctx, q.Now)
	if err != nil {
		return nil, err
	}
	end := q.Now.Add(q.Window)
	out := make([]transit.Departure, 0, len(all))
	for _, d := range all {
		if t := d.Time(); t.Before(q.Now) || t.After(end) {
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
	return transit.SortAndLimit(out, q.Limit), nil
}

func (p *Provider) cacheFor(q transit.Query) *upstream.Cache[[]transit.Departure] {
	k := stopWindow{key: p.stopKey(q.StopID), window: q.Window}
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.stops[k]
	if !ok {
		// Fetching one refresh interval past the window keeps the end of
		// the window filled until the next fetch.
		span := q.Window + p.refresh
		c = upstream.NewCache(upstream.Policy{
			TTL:               p.refresh,
			Retry:             p.refresh,
			RetryOnlyWithData: true,
		}, func(ctx context.Context) ([]transit.Departure, error) {
			return p.fetch(ctx, k.key, span)
		})
		p.stops[k] = c
	}
	return c
}

// stopKey turns a configured stop into a Transitland stop key.
func (p *Provider) stopKey(stop string) string {
	if p.feed == "" || strings.HasPrefix(stop, p.feed+":") {
		return stop
	}
	return p.feed + ":" + stop
}

type departuresResponse struct {
	Stops []wireStop `json:"stops"`
}

type wireStop struct {
	Departures []wireDeparture `json:"departures"`
	// Children are a station's platforms; a station's departures are
	// listed under them.
	Children []struct {
		Departures []wireDeparture `json:"departures"`
	} `json:"children"`
}

type wireDeparture struct {
	StopHeadsign         string    `json:"stop_headsign"`
	PickupType           *int      `json:"pickup_type"`
	ScheduleRelationship string    `json:"schedule_relationship"`
	Departure            wireEvent `json:"departure"`
	Trip                 struct {
		TripID               string `json:"trip_id"`
		TripHeadsign         string `json:"trip_headsign"`
		ScheduleRelationship string `json:"schedule_relationship"`
		Route                struct {
			OnestopID      string `json:"onestop_id"`
			RouteID        string `json:"route_id"`
			RouteShortName string `json:"route_short_name"`
			RouteLongName  string `json:"route_long_name"`
			RouteColor     string `json:"route_color"`
			RouteType      int    `json:"route_type"`
		} `json:"route"`
	} `json:"trip"`
}

type wireEvent struct {
	ScheduledUTC *time.Time `json:"scheduled_utc"`
	EstimatedUTC *time.Time `json:"estimated_utc"`
}

func (p *Provider) fetch(ctx context.Context, key string, span time.Duration) ([]transit.Departure, error) {
	params := url.Values{
		"next":  {strconv.Itoa(int(span / time.Second))},
		"limit": {strconv.Itoa(departureLimit)},
		// The default falls back to a typical day's timetable when the feed
		// has expired, which would show trips that do not run.
		"use_service_window": {"false"},
	}
	var resp departuresResponse
	if err := p.get(ctx, "stops/"+url.PathEscape(key)+"/departures", params, &resp); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("%w %q", ErrUnknownStop, key)
		}
		return nil, err
	}
	if len(resp.Stops) == 0 {
		return nil, fmt.Errorf("%w %q", ErrUnknownStop, key)
	}

	type seen struct {
		trip string
		at   time.Time
	}
	dup := map[seen]bool{}
	var out []transit.Departure
	add := func(w wireDeparture) {
		d, ok := convert(w)
		if !ok {
			return
		}
		k := seen{w.Trip.TripID, d.Scheduled}
		if w.Trip.TripID != "" && dup[k] {
			return
		}
		dup[k] = true
		out = append(out, d)
	}
	for _, s := range resp.Stops {
		for _, w := range s.Departures {
			add(w)
		}
		for _, c := range s.Children {
			for _, w := range c.Departures {
				add(w)
			}
		}
	}
	return out, nil
}

// convert maps one departure, reporting false for ones riders cannot board.
func convert(w wireDeparture) (transit.Departure, bool) {
	if w.Departure.ScheduledUTC == nil {
		return transit.Departure{}, false
	}
	// pickup_type 1: no pickup, as at a trip's last stop.
	if w.PickupType != nil && *w.PickupType == 1 {
		return transit.Departure{}, false
	}
	status := transit.StatusScheduled
	for _, rel := range []string{w.ScheduleRelationship, w.Trip.ScheduleRelationship} {
		switch rel {
		case "SKIPPED":
			return transit.Departure{}, false
		case "CANCELED", "DELETED":
			status = transit.StatusCanceled
		case "ADDED", "NEW", "REPLACEMENT", "DUPLICATED":
			if status == transit.StatusScheduled {
				status = transit.StatusAdded
			}
		}
	}

	r := w.Trip.Route
	id := r.OnestopID
	if id == "" {
		id = r.RouteID
	}
	short := r.RouteShortName
	if short == "" {
		short = r.RouteLongName
	}
	if short == "" {
		short = id
	}
	headsign := w.StopHeadsign
	if headsign == "" {
		headsign = w.Trip.TripHeadsign
	}
	d := transit.Departure{
		Line: transit.Line{
			ID:        id,
			ShortName: short,
			Color:     transit.NormalizeColor(r.RouteColor),
			Mode:      transit.ModeForRouteType(r.RouteType),
		},
		Headsign:  headsign,
		Scheduled: *w.Departure.ScheduledUTC,
		Status:    status,
	}
	if e := w.Departure.EstimatedUTC; e != nil && !e.IsZero() {
		t := *e
		d.Predicted = &t
	}
	return d, d.Line.ID != ""
}

// SearchStops implements transit.StopSearcher with Transitland's full-text
// stop search, limited to the configured feed if there is one. Each search
// counts against the quota.
func (p *Provider) SearchStops(ctx context.Context, search string) ([]transit.Stop, error) {
	params := url.Values{
		"search": {strings.TrimSpace(search)},
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

var errNotFound = errors.New("not found")

// get requests path, which must already be escaped, below the base URL.
func (p *Provider) get(ctx context.Context, path string, params url.Values, v any) error {
	u, err := url.Parse(strings.TrimSuffix(p.base.String(), "/") + "/" + path)
	if err != nil {
		return err
	}
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "urbanist-timetable")
	req.Header.Set("apikey", p.key)
	resp, err := p.http.Do(req)
	if err != nil {
		// The URL holds no secret: the key travels in a header.
		return fmt.Errorf("%s: %w", Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return errNotFound
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
