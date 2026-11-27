// Package tfl reads arrival predictions from the Transport for London
// Unified API: Tube, DLR, London Overground, Elizabeth line, Tram, buses and
// river buses.
//
// It is a custom provider because TfL publishes no GTFS-Realtime feed. The
// API works without a key at a lower rate limit; an app key from
// https://api-portal.tfl.gov.uk/ raises it. Data is Powered by TfL Open
// Data, under TfL's version of the OGL v2.0.
package tfl

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/internal/upstream"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/registry"
)

// Name is the provider's name in configuration.
const Name = "tfl"

// DefaultBaseURL is the Unified API.
const DefaultBaseURL = "https://api.tfl.gov.uk"

const (
	// arrivalsTTL matches the API's Cache-Control max-age, so boards that
	// share a stop share one request per poll.
	arrivalsTTL = 30 * time.Second
	// nameTTL and nameRetry bound how long stop names are cached.
	nameTTL   = 24 * time.Hour
	nameRetry = 15 * time.Minute
	// lookupBatch is how many stops one StopPoint request names.
	lookupBatch = 20
	// maxBody guards against unexpectedly large responses. A StopPoint
	// lookup returns the stop's whole interchange, about 150 KB for a large
	// one.
	maxBody = 8 << 20
)

// searchModes are the modes that have arrival predictions.
const searchModes = "tube,dlr,overground,elizabeth-line,tram,bus,river-bus"

const busStop = "NaptanPublicBusCoachTram"

// busGroupPrefix starts the ID of a group of bus stops. The API returns no
// arrivals for a group, only for its stops. Groups come in several stop
// types (a cluster, a pair across a road, even a ferry port), so the ID is
// what marks one.
const busGroupPrefix = "490G"

// naptanID is the shape of a NaPTAN stop ID, such as 940GZZLUOXC or
// 490013766E, so a search for one looks it up instead.
var naptanID = regexp.MustCompile(`^[0-9]{3}[0-9A-Z]+$`)

// colors are TfL's line colors; the API serves none. Buses have none.
var colors = map[string]string{
	"bakerloo":         "#B36305",
	"central":          "#E32017",
	"circle":           "#FFD300",
	"district":         "#00782A",
	"hammersmith-city": "#F3A9BB",
	"jubilee":          "#A0A5A9",
	"metropolitan":     "#9B0056",
	"northern":         "#000000",
	"piccadilly":       "#003688",
	"victoria":         "#0098D4",
	"waterloo-city":    "#95CDBA",
	"dlr":              "#00A4A7",
	"elizabeth":        "#6950A1",
	"tram":             "#84B817",
	"liberty":          "#5D6061",
	"lioness":          "#FAA61A",
	"mildmay":          "#0077AD",
	"suffragette":      "#5BBD72",
	"weaver":           "#823A62",
	"windrush":         "#ED1B00",
}

var modes = map[string]transit.Mode{
	"tube":           transit.ModeMetro,
	"dlr":            transit.ModeMetro,
	"overground":     transit.ModeRail,
	"elizabeth-line": transit.ModeRail,
	"tram":           transit.ModeTram,
	"bus":            transit.ModeBus,
	"river-bus":      transit.ModeFerry,
}

// Config is the provider's configuration block.
type Config struct {
	// AppKey is optional; without it the API applies a lower rate limit.
	AppKey string `toml:"app_key"`
	// BaseURL overrides the API address, for tests.
	BaseURL string `toml:"base_url"`
}

// Provider reads arrival predictions. It is safe for concurrent use.
type Provider struct {
	base   *url.URL
	appKey string
	http   *http.Client
	clock  func() time.Time

	mu       sync.Mutex
	arrivals map[string]*upstream.Cache[[]prediction]
	names    map[string]*upstream.Cache[string]
}

// Register adds the provider to r.
func Register(r *registry.Registry) {
	registry.RegisterConfig(r, Name, func(cfg Config) (*Provider, error) { return New(cfg, nil) })
}

// New validates cfg and returns a provider. A nil client gets a default
// with a timeout.
func New(cfg Config, client *http.Client) (*Provider, error) {
	base, err := upstream.HTTPURL("base_url", cmp.Or(cfg.BaseURL, DefaultBaseURL))
	if err != nil {
		return nil, err
	}
	return &Provider{
		base:     base,
		appKey:   cfg.AppKey,
		http:     upstream.ClientOrDefault(client),
		clock:    time.Now,
		arrivals: map[string]*upstream.Cache[[]prediction]{},
		names:    map[string]*upstream.Cache[string]{},
	}, nil
}

// prediction is one entry of GET /StopPoint/{id}/Arrivals.
type prediction struct {
	NaptanID            string    `json:"naptanId"`
	LineID              string    `json:"lineId"`
	LineName            string    `json:"lineName"`
	ModeName            string    `json:"modeName"`
	DestinationNaptanID string    `json:"destinationNaptanId"`
	DestinationName     string    `json:"destinationName"`
	Towards             string    `json:"towards"`
	ExpectedArrival     time.Time `json:"expectedArrival"`
}

// Departures implements transit.Provider.
//
// Every departure is a live prediction; Scheduled is set to the same time
// because the API exposes no timetable.
func (p *Provider) Departures(ctx context.Context, q transit.Query) ([]transit.Departure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.HasPrefix(q.StopID, busGroupPrefix) {
		return nil, fmt.Errorf("%w %q: a group of bus stops has no arrivals of its own; use one of its stops, which a stop search for the group lists", transit.ErrUnknownStop, q.StopID)
	}
	preds, err := p.arrivalsFor(q.StopID).Get(ctx, q.Now)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("%w %q", transit.ErrUnknownStop, q.StopID)
		}
		return nil, err
	}
	out := make([]transit.Departure, 0, len(preds))
	for _, pr := range preds {
		// Arrivals include trains that end their trip here and take no one
		// on.
		if pr.DestinationNaptanID != "" && pr.DestinationNaptanID == pr.NaptanID {
			continue
		}
		t := pr.ExpectedArrival.In(q.Now.Location())
		if pr.ExpectedArrival.IsZero() || !q.Contains(t) {
			continue
		}
		out = append(out, transit.Departure{
			Line:      lineFor(pr),
			Headsign:  headsign(pr),
			StopID:    q.StopID,
			Scheduled: t,
			Predicted: &t,
			Status:    transit.StatusScheduled,
		})
	}
	transit.SortByTime(out)
	return out, nil
}

func (p *Provider) arrivalsFor(id string) *upstream.Cache[[]prediction] {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.arrivals[id]
	if !ok {
		c = upstream.NewCache(upstream.Policy{TTL: arrivalsTTL}, func(ctx context.Context) ([]prediction, error) {
			var preds []prediction
			if err := p.get(ctx, "StopPoint/"+url.PathEscape(id)+"/Arrivals", nil, &preds); err != nil {
				return nil, err
			}
			return preds, nil
		})
		p.arrivals[id] = c
	}
	return c
}

func lineFor(pr prediction) transit.Line {
	mode, ok := modes[pr.ModeName]
	if !ok {
		mode = transit.ModeOther
	}
	return transit.Line{
		ID:        pr.LineID,
		ShortName: cmp.Or(pr.LineName, pr.LineID),
		Color:     colors[pr.LineID],
		Mode:      mode,
	}
}

// stationSuffixes are dropped from destination names, which repeat the
// kind of station: "Brixton Underground Station".
var stationSuffixes = []string{" Underground Station", " DLR Station", " Rail Station"}

func headsign(pr prediction) string {
	h := strings.Join(strings.Fields(cmp.Or(pr.DestinationName, pr.Towards)), " ")
	for _, s := range stationSuffixes {
		h = strings.TrimSuffix(h, s)
	}
	// Placeholder the Tube sends when a train's destination is unknown.
	if strings.EqualFold(h, "Check Front of Train") {
		return ""
	}
	return h
}

// stopPoint is a node of GET /StopPoint/{ids}: a stop and the stops it
// groups.
type stopPoint struct {
	NaptanID             string `json:"naptanId"`
	CommonName           string `json:"commonName"`
	StopType             string `json:"stopType"`
	Indicator            string `json:"indicator"`
	AdditionalProperties []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"additionalProperties"`
	Children []stopPoint `json:"children"`
}

// find returns the node with id in the tree under n.
func (n *stopPoint) find(id string) (*stopPoint, bool) {
	if n.NaptanID == id {
		return n, true
	}
	for i := range n.Children {
		if f, ok := n.Children[i].find(id); ok {
			return f, true
		}
	}
	return nil, false
}

// stops lists what n stands for as stops with arrivals: a bus stop group's
// stops, or n itself.
func (n *stopPoint) stops() []transit.Stop {
	if !strings.HasPrefix(n.NaptanID, busGroupPrefix) {
		return []transit.Stop{{ID: n.NaptanID, Name: n.name()}}
	}
	var out []transit.Stop
	for _, c := range n.Children {
		if c.StopType == busStop {
			out = append(out, transit.Stop{ID: c.NaptanID, Name: c.name()})
		}
	}
	return out
}

// name is the stop's name, with a bus stop's letter and direction, since a
// street's stops share one name: "Trafalgar Square (Stop B, towards
// Aldwych)".
func (n *stopPoint) name() string {
	name := strings.Join(strings.Fields(n.CommonName), " ")
	if n.StopType != busStop {
		return name
	}
	var extra []string
	if n.Indicator != "" {
		extra = append(extra, n.Indicator)
	}
	for _, a := range n.AdditionalProperties {
		if a.Key == "Towards" && a.Value != "" {
			extra = append(extra, "towards "+a.Value)
			break
		}
	}
	if len(extra) == 0 {
		return name
	}
	return name + " (" + strings.Join(extra, ", ") + ")"
}

// lookup fetches the stops with ids, as the trees the API returns them in.
func (p *Provider) lookup(ctx context.Context, ids []string) ([]stopPoint, error) {
	var out []stopPoint
	for chunk := range slices.Chunk(ids, lookupBatch) {
		escaped := make([]string, len(chunk))
		for i, id := range chunk {
			escaped[i] = url.PathEscape(id)
		}
		path := "StopPoint/" + strings.Join(escaped, ",")
		// One ID gets an object, several an array.
		if len(chunk) == 1 {
			var sp stopPoint
			if err := p.get(ctx, path, nil, &sp); err != nil {
				return nil, err
			}
			out = append(out, sp)
			continue
		}
		var sps []stopPoint
		if err := p.get(ctx, path, nil, &sps); err != nil {
			return nil, err
		}
		out = append(out, sps...)
	}
	return out, nil
}

// findIn returns the node with id in trees.
func findIn(trees []stopPoint, id string) (*stopPoint, bool) {
	for i := range trees {
		if n, ok := trees[i].find(id); ok {
			return n, true
		}
	}
	return nil, false
}

// SearchStops implements transit.StopSearcher. A NaPTAN ID is looked up
// directly; other text goes to the API's search. Groups of bus stops are
// listed as their stops, because only those have arrivals.
func (p *Provider) SearchStops(ctx context.Context, search string) ([]transit.Stop, error) {
	search = strings.TrimSpace(search)
	if search == "" {
		return nil, nil
	}
	if id := strings.ToUpper(search); naptanID.MatchString(id) {
		trees, err := p.lookup(ctx, []string{id})
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		n, ok := findIn(trees, id)
		if !ok {
			return nil, nil
		}
		return n.stops(), nil
	}

	var resp struct {
		Matches []struct {
			ID    string   `json:"id"`
			Name  string   `json:"name"`
			Modes []string `json:"modes"`
		} `json:"matches"`
	}
	params := url.Values{"modes": {searchModes}, "includeHubs": {"false"}}
	if err := p.get(ctx, "StopPoint/Search/"+url.PathEscape(search), params, &resp); err != nil {
		return nil, err
	}
	var out []transit.Stop
	// Bus results carry no stop letter, so they are looked up for it.
	var bus []string
	for _, m := range resp.Matches {
		if slices.Contains(m.Modes, "bus") && strings.HasPrefix(m.ID, "490") {
			bus = append(bus, m.ID)
			continue
		}
		out = append(out, transit.Stop{ID: m.ID, Name: strings.Join(strings.Fields(m.Name), " ")})
	}
	if len(bus) > 0 {
		trees, err := p.lookup(ctx, bus)
		if err != nil {
			return nil, err
		}
		for _, id := range bus {
			if n, ok := findIn(trees, id); ok {
				out = append(out, n.stops()...)
			}
		}
	}
	slices.SortFunc(out, func(a, b transit.Stop) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return slices.CompactFunc(out, func(a, b transit.Stop) bool { return a.ID == b.ID }), nil
}

// StopName implements transit.StopNamer, looking the stop up once a day.
func (p *Provider) StopName(ctx context.Context, id string, now time.Time) (string, bool) {
	name, err := p.nameFor(id).Get(ctx, now)
	return name, err == nil && name != ""
}

func (p *Provider) nameFor(id string) *upstream.Cache[string] {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.names[id]
	if !ok {
		c = upstream.NewCache(upstream.Policy{TTL: nameTTL, Retry: nameRetry}, func(ctx context.Context) (string, error) {
			trees, err := p.lookup(ctx, []string{id})
			if err != nil {
				return "", err
			}
			n, ok := findIn(trees, id)
			if !ok {
				return "", fmt.Errorf("%w %q", transit.ErrUnknownStop, id)
			}
			return n.name(), nil
		})
		p.names[id] = c
	}
	return c
}

var errNotFound = errors.New("not found")

// get calls an API path and decodes its JSON into v. Errors name the path
// but never the query, which holds the app key.
func (p *Provider) get(ctx context.Context, path string, params url.Values, v any) error {
	u := p.base.JoinPath(path)
	q := url.Values{}
	maps.Copy(q, params)
	if p.appKey != "" {
		q.Set("app_key", p.appKey)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("%s: GET %s: invalid URL", Name, path)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", upstream.UserAgent)
	resp, err := p.http.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return fmt.Errorf("%s: GET %s: %w", Name, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests && p.appKey != "" {
		// A wrong key is refused with 429 too, told apart only by the body.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		if strings.Contains(string(b), "Invalid app_key") {
			return fmt.Errorf("%s: GET %s: %s: app_key is not valid", Name, path, resp.Status)
		}
	}
	if resp.StatusCode != http.StatusOK {
		upstream.Drain(resp.Body)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return fmt.Errorf("%s: GET %s: %w", Name, path, errNotFound)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: GET %s: %s: check app_key", Name, path, resp.Status)
	case http.StatusTooManyRequests:
		if p.appKey == "" {
			return fmt.Errorf("%s: GET %s: %s: rate limited; an app_key from https://api-portal.tfl.gov.uk/ raises the limit", Name, path, resp.Status)
		}
		return fmt.Errorf("%s: GET %s: %s: rate limited", Name, path, resp.Status)
	default:
		return fmt.Errorf("%s: GET %s: %s", Name, path, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%s: read %s: %w", Name, path, err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s: decode %s: %w", Name, path, err)
	}
	return nil
}
