// Package carrismetropolitana reads arrivals from the Carris Metropolitana
// API, the regional bus network in 15 of the 18 municipalities of the Lisbon
// metropolitan area. The API is open and needs no key.
//
// It is a custom provider because the operator publishes realtime arrivals
// as JSON, not as GTFS-RT TripUpdates. API reference:
// https://github.com/carrismetropolitana/api. Data is CC BY 4.0.
package carrismetropolitana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// Name is the provider's name in configuration.
const Name = "carrismetropolitana"

// DefaultBaseURL is the public API.
const DefaultBaseURL = "https://api.carrismetropolitana.pt/v2"

const (
	// linesTTL bounds how long line names and colors are cached; they
	// change with timetable updates, not during the day.
	linesTTL = 24 * time.Hour
	// linesRetry spaces out line downloads after a failure, so a failing
	// endpoint does not add a request, and its timeout, to every poll.
	linesRetry = 15 * time.Minute
	// maxBody guards against unexpectedly large responses. The full stops
	// list is the largest at about 7 MB.
	maxBody = 32 << 20
)

// ErrUnknownStop is returned when the API does not know a stop ID.
var ErrUnknownStop = errors.New("unknown stop")

// Config is the provider's configuration block.
type Config struct {
	// BaseURL overrides the API address, for tests or a mirror.
	BaseURL string `toml:"base_url"`
}

// Provider reads arrivals from the API. It is safe for concurrent use.
type Provider struct {
	base *url.URL
	http *http.Client

	mu            sync.Mutex
	lines         map[string]transit.Line
	linesAt       time.Time
	linesFailedAt time.Time
	stops         []transit.Stop
}

// Register adds the provider to r.
func Register(r *transit.Registry) {
	r.Register(Name, func(opts transit.Options) (transit.Provider, error) {
		var cfg Config
		if err := opts.Decode(&cfg); err != nil {
			return nil, err
		}
		return New(cfg, nil)
	})
}

// New returns a provider. A nil client gets a default with a timeout.
func New(cfg Config, client *http.Client) (*Provider, error) {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("base_url %q must be an http(s) URL", base)
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Provider{base: u, http: client}, nil
}

// arrival is one entry of GET /arrivals/by_stop/{id}.
type arrival struct {
	Headsign      string `json:"headsign"`
	LineID        string `json:"line_id"`
	ScheduledUnix *int64 `json:"scheduled_arrival_unix"`
	EstimatedUnix *int64 `json:"estimated_arrival_unix"`
	ObservedUnix  *int64 `json:"observed_arrival_unix"`
}

// Departures implements transit.Provider.
//
// The API returns the whole service day for a stop, so departures are
// windowed here. Near midnight a window that runs into the next service day
// shows only the current day's trips until the API rolls over.
func (p *Provider) Departures(ctx context.Context, q transit.Query) ([]transit.Departure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lines := p.linesFor(ctx, q.Now)

	var arrivals []arrival
	if err := p.get(ctx, "arrivals/by_stop/"+url.PathEscape(q.StopID), &arrivals); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("%w %q", ErrUnknownStop, q.StopID)
		}
		return nil, err
	}

	end := q.Now.Add(q.Window)
	out := make([]transit.Departure, 0, len(arrivals))
	for _, a := range arrivals {
		// An observed arrival means the bus has already been at the stop.
		if a.ObservedUnix != nil || a.ScheduledUnix == nil {
			continue
		}
		d := transit.Departure{
			Line:      lineFor(lines, a.LineID),
			Headsign:  a.Headsign,
			StopID:    q.StopID,
			Scheduled: time.Unix(*a.ScheduledUnix, 0).In(q.Now.Location()),
			Status:    transit.StatusScheduled,
		}
		if a.EstimatedUnix != nil {
			t := time.Unix(*a.EstimatedUnix, 0).In(q.Now.Location())
			d.Predicted = &t
		}
		if t := d.Time(); t.Before(q.Now) || t.After(end) {
			continue
		}
		out = append(out, d)
	}
	return transit.SortAndLimit(out, q.Limit), nil
}

// feedPrefix matches the "[A2L1N]" namespace the live API puts in front of
// line IDs in arrivals. GET /lines uses the bare ID ("4701"), and the
// published API examples show no prefix.
var feedPrefix = regexp.MustCompile(`^\[[^\]]*\]`)

func lineFor(lines map[string]transit.Line, rawID string) transit.Line {
	id := feedPrefix.ReplaceAllString(rawID, "")
	if l, ok := lines[id]; ok {
		return l
	}
	return transit.Line{ID: id, ShortName: id, Mode: transit.ModeBus}
}

// linesFor returns cached line metadata, refreshing it when stale. A failed
// refresh keeps the previous data, and with none, departures still show
// with the bare line number and no color; it is retried after linesRetry.
// The lock is not held during the download.
func (p *Provider) linesFor(ctx context.Context, now time.Time) map[string]transit.Line {
	p.mu.Lock()
	lines, at, failedAt := p.lines, p.linesAt, p.linesFailedAt
	p.mu.Unlock()
	if lines != nil && now.Sub(at) < linesTTL {
		return lines
	}
	if since := now.Sub(failedAt); !failedAt.IsZero() && since >= 0 && since < linesRetry {
		return lines
	}

	fetched, err := p.fetchLines(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			p.linesFailedAt = now
		}
		return p.lines
	}
	p.lines, p.linesAt, p.linesFailedAt = fetched, now, time.Time{}
	return fetched
}

func (p *Provider) fetchLines(ctx context.Context) (map[string]transit.Line, error) {
	var wire []struct {
		ID        string `json:"id"`
		ShortName string `json:"short_name"`
		Color     string `json:"color"`
	}
	if err := p.get(ctx, "lines", &wire); err != nil {
		return nil, err
	}
	lines := make(map[string]transit.Line, len(wire))
	for _, l := range wire {
		short := l.ShortName
		if short == "" {
			short = l.ID
		}
		lines[l.ID] = transit.Line{ID: l.ID, ShortName: short, Color: transit.NormalizeColor(l.Color), Mode: transit.ModeBus}
	}
	return lines, nil
}

// SearchStops implements transit.StopSearcher. It matches a stop ID
// exactly, or every word of search against the stop name (see
// transit.MatchesSearch). The stop list is downloaded once.
func (p *Provider) SearchStops(ctx context.Context, search string) ([]transit.Stop, error) {
	stops, err := p.allStops(ctx)
	if err != nil {
		return nil, err
	}
	search = strings.TrimSpace(search)
	var out []transit.Stop
	for _, s := range stops {
		if s.ID == search {
			return []transit.Stop{s}, nil
		}
		if transit.MatchesSearch(s.Name, search) {
			out = append(out, s)
		}
	}
	return out, nil
}

// allStops downloads the stop list once. The lock is not held during the
// download, which is about 7 MB.
func (p *Provider) allStops(ctx context.Context) ([]transit.Stop, error) {
	p.mu.Lock()
	cached := p.stops
	p.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	var wire []struct {
		ID       string `json:"id"`
		LongName string `json:"long_name"`
	}
	if err := p.get(ctx, "stops", &wire); err != nil {
		return nil, err
	}
	stops := make([]transit.Stop, 0, len(wire))
	for _, s := range wire {
		stops = append(stops, transit.Stop{ID: s.ID, Name: s.LongName, Modes: []transit.Mode{transit.ModeBus}})
	}
	slices.SortFunc(stops, func(a, b transit.Stop) int { return strings.Compare(a.ID, b.ID) })
	p.mu.Lock()
	p.stops = stops
	p.mu.Unlock()
	return stops, nil
}

var errNotFound = errors.New("not found")

func (p *Provider) get(ctx context.Context, path string, v any) error {
	u := p.base.JoinPath(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "urbanist-timetable")
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", Name, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_, _ = io.Copy(io.Discard, resp.Body)
		return errNotFound
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%s: GET %s: %s", Name, path, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v); err != nil {
		return fmt.Errorf("%s: decode %s: %w", Name, path, err)
	}
	return nil
}
