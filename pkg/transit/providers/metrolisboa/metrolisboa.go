// Package metrolisboa reads train wait times from Metro de Lisboa's
// EstadoServicoML API.
//
// It is a custom provider because Metro publishes realtime data only
// through this API, not as GTFS-RT. The API needs an OAuth client: register
// at https://api.metrolisboa.pt/store/, subscribe an application to
// EstadoServicoML, and configure its consumer key and secret. Data is
// CC BY 4.0.
//
// The API's response schemas are undocumented; the behavior below follows
// recorded responses (see testdata/README.md).
package metrolisboa

import (
	"cmp"
	"context"
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

	// Wait times are stamped in Lisbon local time, so the zone must load
	// even where the system has no zoneinfo.
	_ "time/tzdata"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/internal/upstream"
)

// Name is the provider's name in configuration.
const Name = "metrolisboa"

// Defaults for Config.
const (
	DefaultBaseURL  = "https://api.metrolisboa.pt:8243/estadoServicoML/1.0.1"
	DefaultTokenURL = "https://api.metrolisboa.pt:8243/token"
)

const (
	// metaTTL bounds how long the station and destination lists are cached.
	metaTTL = 24 * time.Hour
	// metaRetry is how soon the station and destination lists are fetched
	// again after a failure, so headsigns do not show bare destination
	// codes for a day.
	metaRetry = 15 * time.Minute
	// lineTTL lets boards that share a line reuse one response per poll.
	lineTTL = 15 * time.Second
	// maxAge drops platform entries whose timestamp is this far from now.
	// Recorded responses include platforms frozen since 2024.
	maxAge = 5 * time.Minute
	// tokenSlack renews the token this long before it expires.
	tokenSlack = time.Minute
	maxBody    = 4 << 20
)

// Line names as the API spells them, with colors chosen to read well on
// an LED matrix.
var lines = map[string]transit.Line{
	"Azul":     {ID: "azul", ShortName: "Azul", Color: "#0072CE", Mode: transit.ModeMetro},
	"Amarela":  {ID: "amarela", ShortName: "Amarela", Color: "#FFC20E", Mode: transit.ModeMetro},
	"Verde":    {ID: "verde", ShortName: "Verde", Color: "#00A651", Mode: transit.ModeMetro},
	"Vermelha": {ID: "vermelha", ShortName: "Vermelha", Color: "#E4002B", Mode: transit.ModeMetro},
}

// Config is the provider's configuration block.
type Config struct {
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
	// BaseURL and TokenURL override the API addresses, for tests.
	BaseURL  string `toml:"base_url"`
	TokenURL string `toml:"token_url"`
}

type station struct {
	name  string
	lines []string
}

// Provider reads wait times. It is safe for concurrent use.
type Provider struct {
	base     *url.URL
	tokenURL string
	id       string
	secret   string
	http     *http.Client
	lisbon   *time.Location
	clock    func() time.Time

	stations *upstream.Cache[map[string]station]
	dests    *upstream.Cache[map[string]string]
	// lineEntries holds one cache per line in lines.
	lineEntries map[string]*upstream.Cache[[]entry]

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// Register adds the provider to r.
func Register(r *transit.Registry) {
	transit.RegisterConfig(r, Name, func(cfg Config) (*Provider, error) { return New(cfg, nil) })
}

// New validates cfg and returns a provider. A nil client gets a default
// with a timeout.
func New(cfg Config, client *http.Client) (*Provider, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("client_id and client_secret are required; see https://api.metrolisboa.pt/store/")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = DefaultTokenURL
	}
	base, ok := upstream.HTTPURL(cfg.BaseURL)
	if !ok {
		return nil, fmt.Errorf("base_url %q must be an http(s) URL", cfg.BaseURL)
	}
	if _, ok := upstream.HTTPURL(cfg.TokenURL); !ok {
		return nil, fmt.Errorf("token_url %q must be an http(s) URL", cfg.TokenURL)
	}
	lisbon, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	p := &Provider{
		base:        base,
		tokenURL:    cfg.TokenURL,
		id:          cfg.ClientID,
		secret:      cfg.ClientSecret,
		http:        client,
		lisbon:      lisbon,
		clock:       time.Now,
		lineEntries: map[string]*upstream.Cache[[]entry]{},
	}
	// Without a station list the provider can answer nothing, so a first
	// load that fails is retried on the next poll rather than after
	// metaRetry.
	p.stations = upstream.NewCache(upstream.Policy{TTL: metaTTL, Retry: metaRetry, RetryOnlyWithData: true}, p.fetchStations)
	p.dests = upstream.NewCache(upstream.Policy{TTL: metaTTL, Retry: metaRetry}, p.fetchDests)
	for name := range lines {
		p.lineEntries[name] = upstream.NewCache(upstream.Policy{TTL: lineTTL}, func(ctx context.Context) ([]entry, error) {
			var entries []entry
			if err := p.get(ctx, "tempoEspera/Linha/"+url.PathEscape(name), &entries); err != nil {
				return nil, err
			}
			return entries, nil
		})
	}
	return p, nil
}

// entry is one platform in a wait-time response. Each lists the next three
// trains with seconds from Hora until each arrives.
type entry struct {
	StopID      string  `json:"stop_id"`
	Hora        string  `json:"hora"`
	Train1      string  `json:"comboio"`
	Train2      string  `json:"comboio2"`
	Train3      string  `json:"comboio3"`
	Secs1       seconds `json:"tempoChegada1"`
	Secs2       seconds `json:"tempoChegada2"`
	Secs3       seconds `json:"tempoChegada3"`
	Destino     string  `json:"destino"`
	SairServico int     `json:"sairServico"`
}

// seconds accepts a number, a numeric string, or a placeholder such as
// "--", which the API sends for platforms with no data.
type seconds struct {
	n  int
	ok bool
}

func (s *seconds) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*s = seconds{n: n, ok: true}
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return fmt.Errorf("tempoChegada: %s", b)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(str)); err == nil {
		*s = seconds{n: n, ok: true}
		return nil
	}
	*s = seconds{}
	return nil
}

// Departures implements transit.Provider.
//
// Wait times carry no line, so they are read per line
// (/tempoEspera/Linha/{line}) for each line serving the station. Every
// departure is a live prediction; Scheduled is set to the same time because
// the API exposes no timetable. When some of a station's lines fail, the
// rest come back with a *transit.PartialError.
func (p *Provider) Departures(ctx context.Context, q transit.Query) ([]transit.Departure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stations, err := p.stationList(ctx, q.Now)
	if err != nil {
		return nil, err
	}
	st, ok := stations[q.StopID]
	if !ok {
		return nil, fmt.Errorf("%w %q", transit.ErrUnknownStop, q.StopID)
	}

	// A failed destination list keeps the previous one; without one,
	// headsigns fall back to destination codes.
	dests, _ := p.dests.Get(ctx, q.Now)

	end := q.Now.Add(q.Window)
	// Terminals report the same train on more than one platform entry, so
	// departures are keyed by train and destination. A train ID alone is not
	// enough: trains turn around and pass again in the other direction.
	type trainKey struct{ line, train, dest string }
	seen := map[trainKey]int{}
	var (
		out      []transit.Departure
		problems []error
		loaded   int
	)
	for _, lineName := range st.lines {
		line, ok := lines[lineName]
		if !ok {
			continue
		}
		entries, err := p.lineEntries[lineName].Get(ctx, q.Now)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			problems = append(problems, err)
			continue
		}
		loaded++
		for _, e := range entries {
			if e.StopID != q.StopID {
				continue
			}
			headsign := cmp.Or(dests[e.Destino], e.Destino)
			for _, tr := range p.trains(e, q.Now, end) {
				t := tr.at
				d := transit.Departure{
					Line:      line,
					Headsign:  headsign,
					StopID:    q.StopID,
					Scheduled: t,
					Predicted: &t,
					Status:    transit.StatusScheduled,
				}
				k := trainKey{lineName, tr.id, e.Destino}
				if i, dup := seen[k]; dup {
					if t.Before(out[i].Time()) {
						out[i] = d
					}
					continue
				}
				seen[k] = len(out)
				out = append(out, d)
			}
		}
	}
	if loaded == 0 && len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	transit.SortByTime(out)
	if len(problems) > 0 {
		return out, &transit.PartialError{Err: errors.Join(problems...)}
	}
	return out, nil
}

type train struct {
	id string
	at time.Time
}

// trains lists the trains in e that take passengers and arrive in
// [now, end], or none when e's timestamp is too far from now.
func (p *Provider) trains(e entry, now, end time.Time) []train {
	stamp, err := time.ParseInLocation("20060102150405", e.Hora, p.lisbon)
	if err != nil || now.Sub(stamp).Abs() > maxAge {
		return nil
	}
	all := []struct {
		id   string
		secs seconds
	}{{e.Train1, e.Secs1}, {e.Train2, e.Secs2}, {e.Train3, e.Secs3}}
	var out []train
	for i, tr := range all {
		// sairServico is undocumented; read as the first train leaving
		// service here, so it takes no passengers.
		if tr.id == "" || !tr.secs.ok || (i == 0 && e.SairServico != 0) {
			continue
		}
		t := stamp.Add(time.Duration(tr.secs.n) * time.Second).In(now.Location())
		if t.Before(now) || t.After(end) {
			continue
		}
		out = append(out, train{id: tr.id, at: t})
	}
	return out
}

// stationList returns the station list, refreshing it daily. A failed
// refresh keeps the previous list and is retried after metaRetry.
func (p *Provider) stationList(ctx context.Context, now time.Time) (map[string]station, error) {
	stations, err := p.stations.Get(ctx, now)
	if stations == nil {
		return nil, err
	}
	return stations, nil
}

func (p *Provider) fetchStations(ctx context.Context) (map[string]station, error) {
	var wire []struct {
		ID    string `json:"stop_id"`
		Name  string `json:"stop_name"`
		Linha string `json:"linha"`
	}
	if err := p.get(ctx, "infoEstacao/todos", &wire); err != nil {
		return nil, err
	}
	stations := make(map[string]station, len(wire))
	for _, s := range wire {
		stations[s.ID] = station{name: s.Name, lines: parseList(s.Linha)}
	}
	return stations, nil
}

func (p *Provider) fetchDests(ctx context.Context) (map[string]string, error) {
	var wire []struct {
		ID   string `json:"id_destino"`
		Name string `json:"nome_destino"`
	}
	if err := p.get(ctx, "infoDestinos/todos", &wire); err != nil {
		return nil, err
	}
	dests := make(map[string]string, len(wire))
	for _, d := range wire {
		dests[d.ID] = d.Name
	}
	return dests, nil
}

// parseList reads the API's "[Verde, Vermelha]" list strings.
func parseList(s string) []string {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "["), "]"))
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// SearchStops implements transit.StopSearcher, matching a station ID
// exactly or every word of search against station names.
func (p *Provider) SearchStops(ctx context.Context, search string) ([]transit.Stop, error) {
	stations, err := p.stationList(ctx, p.clock())
	if err != nil {
		return nil, err
	}
	search = strings.TrimSpace(search)
	var out []transit.Stop
	for id, s := range stations {
		if strings.EqualFold(id, search) {
			return []transit.Stop{{ID: id, Name: s.name}}, nil
		}
		if transit.MatchesSearch(s.name, search) {
			out = append(out, transit.Stop{ID: id, Name: s.name})
		}
	}
	slices.SortFunc(out, func(a, b transit.Stop) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// get calls an API path and decodes its resposta into v. The API reports
// errors in the body ("codigo") with HTTP 200, so both are checked.
func (p *Provider) get(ctx context.Context, path string, v any) error {
	for attempt := 0; ; attempt++ {
		token, err := p.accessToken(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base.JoinPath(path).String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		resp, err := p.http.Do(req)
		if err != nil {
			return fmt.Errorf("%s: %w", Name, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("%s: read %s: %w", Name, path, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			// The token was revoked or expired early; get a new one once.
			p.mu.Lock()
			p.token = ""
			p.mu.Unlock()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: GET %s: %s", Name, path, resp.Status)
		}
		var wire struct {
			Codigo   string          `json:"codigo"`
			Resposta json.RawMessage `json:"resposta"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			return fmt.Errorf("%s: decode %s: %w", Name, path, err)
		}
		if wire.Codigo != "200" {
			var msg string
			_ = json.Unmarshal(wire.Resposta, &msg)
			return fmt.Errorf("%s: GET %s: codigo %s: %s", Name, path, wire.Codigo, msg)
		}
		if err := json.Unmarshal(wire.Resposta, v); err != nil {
			return fmt.Errorf("%s: decode %s: %w", Name, path, err)
		}
		return nil
	}
}

// accessToken returns a cached client-credentials token, fetching a new
// one when it is missing or about to expire. The lock is not held during
// the request, so other boards keep reading cached data; callers that
// arrive meanwhile may fetch a token of their own, which is harmless.
func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	now := p.clock()
	token, exp := p.token, p.tokenExp
	p.mu.Unlock()
	if token != "" && now.Before(exp) {
		return token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(p.id, p.secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: token: %w", Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("%s: token: %s (check client_id and client_secret)", Name, resp.Status)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&tok); err != nil {
		return "", fmt.Errorf("%s: token: %w", Name, err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("%s: token: empty access_token", Name)
	}
	p.mu.Lock()
	p.token = tok.AccessToken
	p.tokenExp = now.Add(time.Duration(tok.ExpiresIn)*time.Second - tokenSlack)
	p.mu.Unlock()
	return tok.AccessToken, nil
}
