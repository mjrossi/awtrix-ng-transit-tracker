// Package transitland reads departures from the Transitland v2 REST API,
// which merges static GTFS and GTFS-RT for thousands of agencies. It needs
// an API key from https://www.transit.land/.
//
// Every request counts against a fixed monthly quota (10,000 on the free
// plan, 50,000 on the Hobbyist/Academic plan), so the provider fetches each
// stop and window once per refresh interval and answers polls in between
// from that response. Boards still tick at their own poll interval, so
// minutes count down between fetches.
package transitland

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// MaxRefreshInterval bounds how old a prediction shown as live can be: a
	// cached answer counts as a fresh fetch to the scheduler.
	MaxRefreshInterval = 10 * time.Minute
	// DefaultMonthlyQuota is the free plan's REST quota.
	DefaultMonthlyQuota = 10_000

	// retryInterval spaces out requests after a failure. It must be short: a
	// retry answered with the remembered failure doubles the caller's backoff,
	// and a board goes blank if that outlasts its max_stale.
	retryInterval = time.Minute

	// lateLookback is how late a vehicle can run and still be shown.
	lateLookback = 30 * time.Minute
	// offsetMaxAge limits how far a daylight saving change can make a stop's
	// offset wrong.
	offsetMaxAge = 12 * time.Hour

	// departureLimit caps departures per stop and per child platform. It is
	// the server's maximum, since filters only see what it returns.
	departureLimit = 1000
	searchLimit    = 50
	maxBody        = 16 << 20
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
	quota   int

	mu sync.Mutex
	// stops holds one cache per stop and window, so boards polling the same
	// stop share each request.
	stops map[stopWindow]*upstream.Cache[timetable]
	// offsets holds each stop key's service-day offset.
	offsets map[string]serviceOffset
}

// serviceOffset is how far a stop's GTFS times are ahead of UTC, which is
// the agency's zone rather than the stop's.
type serviceOffset struct {
	ahead time.Duration
	at    time.Time
}

// timetable is one fetched answer for a stop.
type timetable struct {
	deps []transit.Departure
	// unknown records that the stop does not exist, so a mistyped stop is
	// not asked for on every poll.
	unknown bool
	// until is the last departure of a list cut off at departureLimit, or
	// zero when the answer is complete.
	until time.Time
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
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
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
	c := *client
	// net/http drops Authorization on a redirect to another host, but not
	// custom headers such as apikey.
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
			return errors.New("redirect to another host refused")
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &Provider{
		base:    u,
		http:    &c,
		key:     key,
		feed:    strings.TrimSpace(cfg.Feed),
		refresh: refresh,
		quota:   quota,
		stops:   map[stopWindow]*upstream.Cache[timetable]{},
		offsets: map[string]serviceOffset{},
	}, nil
}

// RefreshInterval implements transit.Metered.
func (p *Provider) RefreshInterval() time.Duration { return p.refresh }

// MonthlyQuota implements transit.Metered.
func (p *Provider) MonthlyQuota() int { return p.quota }

// QuotaAccount implements transit.Metered. Quotas belong to API keys, so it
// identifies the key without revealing it.
func (p *Provider) QuotaAccount() string {
	sum := sha256.Sum256([]byte(p.base.String() + "\x00" + p.key))
	return Name + ":" + hex.EncodeToString(sum[:])
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
	if tt.unknown {
		return nil, fmt.Errorf("%w %q", transit.ErrUnknownStop, p.stopKey(q.StopID))
	}
	end := q.Now.Add(q.Window)
	out := make([]transit.Departure, 0, len(tt.deps))
	for _, d := range tt.deps {
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
	transit.SortByTime(out)
	if tt.until.IsZero() || !tt.until.Before(end) {
		return out, nil
	}
	err = fmt.Errorf("%s: %s has more departures than one request returns; shorten window or refresh_interval", Name, p.stopKey(q.StopID))
	// Empty because the list was cut off does not mean there is no service.
	if len(out) == 0 {
		return nil, err
	}
	return out, &transit.PartialError{Err: err}
}

func (p *Provider) cacheFor(q transit.Query) *upstream.Cache[timetable] {
	k := stopWindow{key: p.stopKey(q.StopID), window: q.Window}
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.stops[k]
	if !ok {
		// Fetching one refresh interval past the window keeps the end of
		// the window filled until the next fetch.
		span := q.Window + p.refresh
		c = upstream.NewCacheAt(upstream.Policy{
			TTL:               p.refresh,
			Retry:             retryInterval,
			RetryOnlyWithData: true,
		}, func(ctx context.Context, now time.Time) (timetable, error) {
			return p.fetch(ctx, k.key, now, span)
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
	ServiceDate          string    `json:"service_date"`
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
			RouteType      *int   `json:"route_type"`
		} `json:"route"`
	} `json:"trip"`
}

type wireEvent struct {
	// Scheduled is the GTFS time of the service day, which passes 24:00.
	Scheduled string `json:"scheduled"`
	// ScheduledUTC is null for trips added in realtime.
	ScheduledUTC *time.Time `json:"scheduled_utc"`
	EstimatedUTC *time.Time `json:"estimated_utc"`
}

// lists returns every departure list in the answer: each stop's own and its
// platforms'.
func (r departuresResponse) lists() [][]wireDeparture {
	var out [][]wireDeparture
	for _, s := range r.Stops {
		out = append(out, s.Departures)
		for _, c := range s.Children {
			out = append(out, c.Departures)
		}
	}
	return out
}

func (p *Provider) fetch(ctx context.Context, key string, now time.Time, span time.Duration) (timetable, error) {
	var resp departuresResponse
	err := p.get(ctx, "stops/"+url.PathEscape(key)+"/departures", p.departureParams(key, now, span), &resp)
	switch {
	case errors.Is(err, errNotFound), err == nil && len(resp.Stops) == 0:
		return timetable{unknown: true}, nil
	case err != nil:
		return timetable{}, err
	}
	lists := resp.lists()
	p.learnOffset(key, lists, now)
	return newTimetable(lists), nil
}

// departureParams asks for departures in the span from now, and from
// lateLookback before it once the stop's offset is known. The server filters
// by scheduled time, so without the lookback a late vehicle that has not come
// yet is left out.
func (p *Provider) departureParams(key string, now time.Time, span time.Duration) url.Values {
	params := url.Values{
		"limit": {strconv.Itoa(departureLimit)},
		// The default falls back to a typical day's timetable when the feed
		// has expired, which would show trips that do not run.
		"use_service_window": {"false"},
	}
	p.mu.Lock()
	offset, ok := p.offsets[key]
	p.mu.Unlock()
	if !ok || now.Sub(offset.at).Abs() > offsetMaxAge {
		params.Set("next", strconv.Itoa(int(span/time.Second)))
		return params
	}
	from := now.Add(offset.ahead - lateLookback).UTC()
	start := from.Sub(from.Truncate(24 * time.Hour))
	params.Set("date", from.Format(time.DateOnly))
	params.Set("start_time", gtfsTime(start))
	params.Set("end_time", gtfsTime(start+lateLookback+span))
	return params
}

// learnOffset records the stop's service-day offset for later requests.
func (p *Provider) learnOffset(key string, lists [][]wireDeparture, now time.Time) {
	for _, list := range lists {
		for _, w := range list {
			if ahead, ok := offsetOf(w); ok {
				p.mu.Lock()
				p.offsets[key] = serviceOffset{ahead, now}
				p.mu.Unlock()
				return
			}
		}
	}
}

// newTimetable converts an answer's departure lists, dropping trips listed
// twice.
func newTimetable(lists [][]wireDeparture) timetable {
	type seen struct {
		trip string
		at   time.Time
	}
	dup := map[seen]bool{}
	var out timetable
	for _, list := range lists {
		// The server lists added trips on top of the limit.
		scheduled := 0
		var last time.Time
		for _, w := range list {
			if at := w.Departure.ScheduledUTC; at != nil {
				scheduled++
				if at.After(last) {
					last = *at
				}
			}
			d, ok := convert(w)
			if !ok {
				continue
			}
			k := seen{w.Trip.TripID, d.Scheduled}
			if w.Trip.TripID != "" && dup[k] {
				continue
			}
			dup[k] = true
			out.deps = append(out.deps, d)
		}
		if scheduled >= departureLimit && (out.until.IsZero() || last.Before(out.until)) {
			out.until = last
		}
	}
	return out
}

func gtfsTime(d time.Duration) string {
	s := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}

// offsetOf derives the service-day offset from a departure's GTFS time and
// the instant it stands for.
func offsetOf(w wireDeparture) (time.Duration, bool) {
	if w.Departure.ScheduledUTC == nil {
		return 0, false
	}
	day, err := time.Parse(time.DateOnly, w.ServiceDate)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(w.Departure.Scheduled, ":")
	if len(parts) != 3 {
		return 0, false
	}
	var secs int
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, false
		}
		secs = secs*60 + n
	}
	ahead := day.Add(time.Duration(secs) * time.Second).Sub(*w.Departure.ScheduledUTC)
	// Zones run from UTC-12 to UTC+14.
	return ahead, ahead >= -12*time.Hour && ahead <= 14*time.Hour
}

// convert maps one departure, reporting false for ones riders cannot board.
func convert(w wireDeparture) (transit.Departure, bool) {
	var predicted *time.Time
	if e := w.Departure.EstimatedUTC; e != nil && !e.IsZero() {
		t := *e
		predicted = &t
	}
	scheduled := w.Departure.ScheduledUTC
	if scheduled == nil {
		scheduled = predicted
	}
	if scheduled == nil {
		return transit.Departure{}, false
	}
	// pickup_type 1: no pickup, as at a trip's last stop.
	if w.PickupType != nil && *w.PickupType == 1 {
		return transit.Departure{}, false
	}
	status := transit.StatusScheduled
	for _, rel := range []string{w.ScheduleRelationship, w.Trip.ScheduleRelationship} {
		switch rel {
		case "SKIPPED", "DELETED":
			return transit.Departure{}, false
		case "CANCELED":
			status = transit.StatusCanceled
		case "ADDED", "NEW", "DUPLICATED":
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
	mode := transit.ModeOther
	if r.RouteType != nil {
		mode = transit.ModeForRouteType(*r.RouteType)
	}
	d := transit.Departure{
		Line: transit.Line{
			ID:        id,
			ShortName: short,
			Color:     transit.NormalizeColor(r.RouteColor),
			Mode:      mode,
		},
		Headsign:  headsign,
		Scheduled: *scheduled,
		Predicted: predicted,
		Status:    status,
	}
	return d, d.Line.ID != ""
}

// SearchStops implements transit.StopSearcher with Transitland's full-text
// stop search, limited to the configured feed if there is one. Each search
// counts against the quota.
func (p *Provider) SearchStops(ctx context.Context, search string) ([]transit.Stop, error) {
	search = strings.TrimSpace(search)
	if search == "" {
		return nil, nil
	}
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
