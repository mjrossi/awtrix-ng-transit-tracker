package transitland_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/transitland"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/transittest"
)

// now is the time testdata is written for; see testdata/README.md.
var now = time.Date(2026, 11, 25, 16, 4, 34, 0, time.UTC)

const feed = "f-example-harbor"

type server struct {
	*httptest.Server
	calls atomic.Int32
	// status, when set, is returned instead of data.
	status atomic.Int32

	mu      sync.Mutex
	lastReq *http.Request
}

// newServer serves testdata for "<feed>:<stop>" keys whose file exists and
// an empty list for anything else, as the API does for an unknown stop key.
func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{}
	serve := func(w http.ResponseWriter, r *http.Request, file string) {
		s.calls.Add(1)
		s.mu.Lock()
		s.lastReq = r
		s.mu.Unlock()
		if code := s.status.Load(); code != 0 {
			http.Error(w, "refused", int(code))
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			b = []byte(`{"stops":[]}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/rest/stops/{key}/departures", func(w http.ResponseWriter, r *http.Request) {
		_, stop, _ := strings.Cut(r.PathValue("key"), ":")
		serve(w, r, "departures_"+stop+".json")
	})
	mux.HandleFunc("GET /api/v2/rest/stops", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, "stops_search.json")
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *server) last() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastReq
}

func newProvider(t *testing.T, s *server, cfg transitland.Config) *transitland.Provider {
	t.Helper()
	cfg.BaseURL = s.URL + "/api/v2/rest"
	if cfg.APIKey == "" {
		cfg.APIKey = "test-key"
	}
	p, err := transitland.New(cfg, s.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func query(stop string, window time.Duration) transit.Query {
	return transit.Query{StopID: stop, Now: now, Window: window}
}

func TestContract(t *testing.T) {
	p := newProvider(t, newServer(t), transitland.Config{Feed: feed})
	transittest.Run(t, p, []transittest.Case{
		{Name: "station with platforms", Query: query("HC", time.Hour)},
		{Name: "short window", Query: query("HC", 10*time.Minute)},
		{Name: "no service", Query: query("QUIET", time.Hour), WantEmpty: true},
	})
}

func TestDepartures(t *testing.T) {
	p := newProvider(t, newServer(t), transitland.Config{Feed: feed})
	got, err := p.Departures(t.Context(), query("HC", time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	at := func(clock string) time.Time {
		v, err := time.Parse(time.RFC3339, "2026-11-25T"+clock+"Z")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	crosstown := transit.Line{ID: "r-example-crosstown", ShortName: "Crosstown", Color: "#2E7D32", Mode: transit.ModeMetro}
	h1 := transit.Line{ID: "r-example-h1", ShortName: "H1", Color: "#5D4037", Mode: transit.ModeBus}
	valley := transit.Line{ID: "r-example-valley", ShortName: "Valley Line", Color: "#1A237E", Mode: transit.ModeRail}
	dep := func(l transit.Line, headsign, scheduled, predicted string, st transit.Status) transit.Departure {
		d := transit.Departure{Line: l, Headsign: headsign, StopID: "HC", Scheduled: at(scheduled), Status: st}
		if predicted != "" {
			p := at(predicted)
			d.Predicted = &p
		}
		return d
	}
	// The canceled trip at 15:52 and the added one at 14:10 have gone.
	want := []transit.Departure{
		dep(crosstown, "Orchard", "16:07:00", "16:08:15", transit.StatusScheduled),
		dep(crosstown, "Quarry", "16:10:00", "16:12:30", transit.StatusScheduled),
		dep(crosstown, "Westgate", "16:19:00", "", transit.StatusCanceled),
		// Scheduled before now, but late.
		dep(crosstown, "Westgate", "15:58:00", "16:21:40", transit.StatusScheduled),
		dep(crosstown, "", "16:23:05", "16:23:05", transit.StatusAdded),
		dep(valley, "Millbrook via Fen", "16:24:00", "16:24:00", transit.StatusScheduled),
		dep(h1, "", "16:26:00", "16:26:00", transit.StatusAdded),
		dep(crosstown, "Orchard", "16:37:00", "", transit.StatusScheduled),
		dep(valley, "Millbrook via Heath", "16:41:00", "", transit.StatusScheduled),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("departures (-want +got):\n%s", diff)
	}
}

func TestRequest(t *testing.T) {
	tests := map[string]struct {
		cfg     transitland.Config
		stop    string
		wantKey string
	}{
		"feed set":           {transitland.Config{Feed: feed}, "HC", feed + ":HC"},
		"feed set, full key": {transitland.Config{Feed: feed}, feed + ":HC", feed + ":HC"},
		"no feed, full key":  {transitland.Config{}, feed + ":HC", feed + ":HC"},
		"custom refresh":     {transitland.Config{Feed: feed, RefreshInterval: "2m"}, "HC", feed + ":HC"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			p := newProvider(t, s, tt.cfg)
			if _, err := p.Departures(t.Context(), query(tt.stop, time.Hour)); err != nil {
				t.Fatal(err)
			}
			r := s.last()
			if got := r.PathValue("key"); got != tt.wantKey {
				t.Errorf("stop key %q, want %q", got, tt.wantKey)
			}
			if got := r.Header.Get("apikey"); got != "test-key" {
				t.Errorf("apikey header %q", got)
			}
			// The window plus one refresh interval, so the end of the window
			// stays filled until the next fetch.
			wantNext := (time.Hour + p.Quota().RefreshInterval) / time.Second
			want := url.Values{"next": {strconv.Itoa(int(wantNext))}, "limit": {"1000"}, "use_service_window": {"false"}}
			if diff := cmp.Diff(want, r.URL.Query()); diff != "" {
				t.Errorf("query (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRefreshInterval(t *testing.T) {
	s := newServer(t)
	p := newProvider(t, s, transitland.Config{Feed: feed, RefreshInterval: "5m"})
	poll := func(after, window time.Duration) {
		t.Helper()
		q := query("HC", window)
		q.Now = now.Add(after)
		deps, err := p.Departures(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		// Answers from the cache are windowed at the poll's own time.
		transittest.CheckDepartures(t, q, deps)
	}
	steps := []struct {
		after  time.Duration
		window time.Duration
		calls  int32
	}{
		{0, time.Hour, 1},
		{time.Minute, time.Hour, 1},
		{4*time.Minute + 58*time.Second, time.Hour, 1},
		// A ticker can deliver the poll due now slightly early.
		{5*time.Minute - time.Millisecond, time.Hour, 2},
		// The answer covers narrower windows; a wider one needs a request.
		{6 * time.Minute, 30 * time.Minute, 2},
		{7 * time.Minute, 90 * time.Minute, 3},
		{8 * time.Minute, time.Hour, 3},
		{8 * time.Minute, 30 * time.Minute, 3},
	}
	for _, st := range steps {
		poll(st.after, st.window)
		if got := s.calls.Load(); got != st.calls {
			t.Fatalf("after %s (window %s): %d requests, want %d", st.after, st.window, got, st.calls)
		}
	}
}

func TestBoardsShareRequests(t *testing.T) {
	s := newServer(t)
	p := newProvider(t, s, transitland.Config{Feed: feed})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := p.Departures(t.Context(), query("HC", time.Hour)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := s.calls.Load(); got != 1 {
		t.Errorf("%d requests for 8 concurrent polls, want 1", got)
	}
}

func TestErrors(t *testing.T) {
	tests := map[string]struct {
		status int
		stop   string
		want   string
		is     error
	}{
		"bad key":       {status: http.StatusUnauthorized, stop: "HC", want: "check api_key"},
		"forbidden":     {status: http.StatusForbidden, stop: "HC", want: "check api_key"},
		"quota":         {status: http.StatusTooManyRequests, stop: "HC", want: "quota"},
		"server error":  {status: http.StatusBadGateway, stop: "HC", want: "502"},
		"not found":     {status: http.StatusNotFound, stop: "HC", want: "transitland: GET stops/f-example-harbor:HC/departures: 404"},
		"unknown stop":  {stop: "NOPE", is: transit.ErrUnknownStop},
		"stop not sent": {stop: "", is: transit.ErrUnknownStop},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			s.status.Store(int32(tt.status))
			p := newProvider(t, s, transitland.Config{Feed: feed})
			_, err := p.Departures(t.Context(), query(tt.stop, time.Hour))
			if err == nil {
				t.Fatal("no error")
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("err = %v, want %v", err, tt.is)
			}
			if tt.is == nil && errors.Is(err, transit.ErrUnknownStop) {
				t.Errorf("err = %v, want it not to blame the stop", err)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "test-key") {
				t.Errorf("err = %v leaks the API key", err)
			}
		})
	}
}

func TestNew(t *testing.T) {
	tests := map[string]struct {
		cfg         transitland.Config
		wantErr     string
		wantRefresh time.Duration
	}{
		"defaults":      {cfg: transitland.Config{APIKey: "k"}, wantRefresh: 5 * time.Minute},
		"hobbyist":      {cfg: transitland.Config{APIKey: "k", RefreshInterval: "2m", MonthlyQuota: 50_000}, wantRefresh: 2 * time.Minute},
		"no key":        {cfg: transitland.Config{}, wantErr: "api_key"},
		"blank key":     {cfg: transitland.Config{APIKey: "  "}, wantErr: "api_key"},
		"too fast":      {cfg: transitland.Config{APIKey: "k", RefreshInterval: "30s"}, wantErr: "at least 1m"},
		"too slow":      {cfg: transitland.Config{APIKey: "k", RefreshInterval: "11m"}, wantErr: "at most 10m"},
		"base query":    {cfg: transitland.Config{APIKey: "k", BaseURL: "https://x/?token=1"}, wantErr: "base_url"},
		"bad duration":  {cfg: transitland.Config{APIKey: "k", RefreshInterval: "5"}, wantErr: "refresh_interval"},
		"neg quota":     {cfg: transitland.Config{APIKey: "k", MonthlyQuota: -1}, wantErr: "monthly_quota"},
		"bad base url":  {cfg: transitland.Config{APIKey: "k", BaseURL: "ftp://x"}, wantErr: "base_url"},
		"base secret":   {cfg: transitland.Config{APIKey: "k", BaseURL: "ftp://user:hunter2@x"}, wantErr: "base_url"},
		"explicit base": {cfg: transitland.Config{APIKey: "k", BaseURL: "https://mirror.example/rest"}, wantRefresh: 5 * time.Minute},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p, err := transitland.New(tt.cfg, nil)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Errorf("err = %v leaks credentials", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var m transit.Metered = p
			want := transit.Quota{Limit: tt.cfg.MonthlyQuota, DefaultLimit: 10_000, RefreshInterval: tt.wantRefresh}
			if diff := cmp.Diff(want, m.Quota(), cmpopts.IgnoreFields(transit.Quota{}, "Account")); diff != "" {
				t.Errorf("quota (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSearchStops(t *testing.T) {
	tests := map[string]struct {
		cfg      transitland.Config
		wantFeed string
		wantIDs  []string
	}{
		"within a feed": {transitland.Config{Feed: feed}, feed, []string{"HC", "HP"}},
		"every feed":    {transitland.Config{}, "", []string{feed + ":HC", feed + ":HP"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			p := newProvider(t, s, tt.cfg)
			stops, err := p.SearchStops(t.Context(), " harbor ")
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, st := range stops {
				ids = append(ids, st.ID)
			}
			if diff := cmp.Diff(tt.wantIDs, ids); diff != "" {
				t.Errorf("IDs (-want +got):\n%s", diff)
			}
			q := s.last().URL.Query()
			if q.Get("search") != "harbor" || q.Get("feed_onestop_id") != tt.wantFeed {
				t.Errorf("query %v", q)
			}
		})
	}
}

// Transitland matches several words as one phrase; a search it misses is
// retried word by word and filtered here.
func TestSearchStopsWordByWord(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "stops_search.json"))
	if err != nil {
		t.Fatal(err)
	}
	var searches []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("search")
		mu.Lock()
		searches = append(searches, q)
		mu.Unlock()
		if strings.Contains(q, " ") {
			_, _ = w.Write([]byte(`{"stops":[]}`))
			return
		}
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(srv.Close)
	p, err := transitland.New(transitland.Config{Feed: feed, APIKey: "k", BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	stops, err := p.SearchStops(t.Context(), "central harbor")
	if err != nil {
		t.Fatal(err)
	}
	if len(stops) != 1 || stops[0].ID != "HC" {
		t.Errorf("stops %v, want Harbor Central alone", stops)
	}
	if diff := cmp.Diff([]string{"central harbor", "central"}, searches); diff != "" {
		t.Errorf("searches (-want +got):\n%s", diff)
	}
}

func TestSearchNamesTheFailedRequest(t *testing.T) {
	s := newServer(t)
	s.status.Store(http.StatusNotFound)
	p := newProvider(t, s, transitland.Config{})
	_, err := p.SearchStops(t.Context(), "harbor")
	if err == nil || !strings.Contains(err.Error(), "transitland: GET stops: 404") {
		t.Errorf("err = %v, want it to name the request and status", err)
	}
}

func TestQuotaAccount(t *testing.T) {
	account := func(key, base string) string {
		p, err := transitland.New(transitland.Config{APIKey: key, BaseURL: base}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return p.Quota().Account
	}
	want := account("k", "")
	if got := account("k", transitland.DefaultBaseURL+"/"); got != want {
		t.Error("a trailing slash in base_url makes another account")
	}
	if got := account("k2", ""); got == want {
		t.Error("different keys share an account")
	}
}

// Earlier data is the scheduler's to reuse, so a failure is only an error.
func TestFailedRefresh(t *testing.T) {
	s := newServer(t)
	p := newProvider(t, s, transitland.Config{Feed: feed, RefreshInterval: "10m"})
	poll := func(after time.Duration) ([]transit.Departure, error) {
		q := query("HC", 2*time.Hour)
		q.Now = now.Add(after)
		return p.Departures(t.Context(), q)
	}
	if _, err := poll(0); err != nil {
		t.Fatal(err)
	}
	s.status.Store(http.StatusBadGateway)
	steps := []struct {
		after time.Duration
		calls int32
	}{
		{10 * time.Minute, 2},
		// Another board's poll shares the failure; the scheduler's first
		// retry does not.
		{10*time.Minute + 5*time.Second, 2},
		{10*time.Minute + 30*time.Second, 3},
	}
	for _, st := range steps {
		deps, err := poll(st.after)
		var partial *transit.PartialError
		if err == nil || errors.As(err, &partial) || deps != nil {
			t.Fatalf("after %s: %d departures, err %v; want only an error", st.after, len(deps), err)
		}
		if got := s.calls.Load(); got != st.calls {
			t.Fatalf("after %s: %d requests, want %d", st.after, got, st.calls)
		}
	}
	s.status.Store(0)
	if _, err := poll(12 * time.Minute); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestFailureWithNoDataIsShared(t *testing.T) {
	tests := map[string]struct {
		status int
		stop   string
	}{
		"outage":       {http.StatusBadGateway, "HC"},
		"unknown stop": {0, "NOPE"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			s.status.Store(int32(tt.status))
			p := newProvider(t, s, transitland.Config{Feed: feed})
			for _, st := range []struct {
				after time.Duration
				calls int32
			}{{0, 1}, {5 * time.Second, 1}, {30 * time.Second, 2}} {
				q := query(tt.stop, time.Hour)
				q.Now = now.Add(st.after)
				if _, err := p.Departures(t.Context(), q); err == nil {
					t.Fatal("no error")
				}
				if got := s.calls.Load(); got != st.calls {
					t.Fatalf("after %s: %d requests, want %d", st.after, got, st.calls)
				}
			}
		})
	}
}

// serving returns a provider whose requests go to h.
func serving(t *testing.T, h http.HandlerFunc) *transitland.Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := transitland.New(transitland.Config{APIKey: "k", BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// static answers every request with body.
func static(t *testing.T, body string) *transitland.Provider {
	t.Helper()
	return serving(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
}

// platform lists n departures a minute apart from start. Trip IDs begin with
// id, and added trips have no scheduled time.
func platform(id string, start time.Time, n int, added bool) string {
	rows := make([]string, n)
	for i := range rows {
		at := start.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		dep := fmt.Sprintf(`{"scheduled_utc":%q}`, at)
		if added {
			dep = fmt.Sprintf(`{"estimated_utc":%q}`, at)
		}
		rows[i] = fmt.Sprintf(`{"departure":%s,"trip":{"trip_id":"%s%d","route":{"route_id":"1"}}}`, dep, id, i)
	}
	return strings.Join(rows, ",")
}

func TestFullLists(t *testing.T) {
	children := func(lists ...string) string {
		for i, l := range lists {
			lists[i] = `{"departures":[` + l + `]}`
		}
		return `{"stops":[{"children":[` + strings.Join(lists, ",") + `]}]}`
	}
	// A full list from now ends at 999 minutes, one from an hour on at 1059.
	early, late := platform("a", now, 1000, false), platform("b", now.Add(time.Hour), 1000, false)
	const window = 1010 * time.Minute
	tests := map[string]struct {
		body        string
		window      time.Duration
		want        int
		wantPartial bool
		wantErr     bool
	}{
		"more than 50 departures":            {body: children(platform("a", now, 120, false)), window: 90 * time.Minute, want: 91},
		"cut off past the window":            {body: children(platform("a", now, 1000, false)), window: 90 * time.Minute, want: 91},
		"cut off within the window":          {body: children(platform("a", now, 1000, false)), window: 24 * time.Hour, want: 1000, wantPartial: true},
		"the stop's own list is cut off":     {body: `{"stops":[{"departures":[` + platform("a", now, 1000, false) + `]}]}`, window: 24 * time.Hour, want: 1000, wantPartial: true},
		"the first platform ends first":      {body: children(early, late), window: window, want: 1951, wantPartial: true},
		"the last platform ends first":       {body: children(late, early), window: window, want: 1951, wantPartial: true},
		"added trips are beyond the limit":   {body: children(platform("a", now, 600, false) + "," + platform("b", now, 600, true)), window: 24 * time.Hour, want: 1200},
		"the same trip on two platforms":     {body: children(platform("a", now, 3, false), platform("a", now, 3, false)), window: window, want: 3},
		"cut off before the window, no data": {body: children(platform("a", now.Add(-24*time.Hour), 1000, false)), window: window, wantErr: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			deps, err := static(t, tt.body).Departures(t.Context(), query("f-x:1", tt.window))
			var partial *transit.PartialError
			if got := errors.As(err, &partial); got != tt.wantPartial {
				t.Errorf("err = %v, want partial = %v", err, tt.wantPartial)
			}
			if got := err != nil && !tt.wantPartial; got != tt.wantErr {
				t.Errorf("err = %v, want a plain error = %v", err, tt.wantErr)
			}
			if len(deps) != tt.want {
				t.Errorf("%d departures, want %d", len(deps), tt.want)
			}
		})
	}
}

func TestRedirectsDoNotCarryTheKey(t *testing.T) {
	var followed, leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Store(true)
		leaked.Store(r.Header.Get("apikey") != "")
		_, _ = w.Write([]byte(`{"stops":[{"departures":[]}]}`))
	}))
	t.Cleanup(other.Close)
	p := serving(t, http.RedirectHandler(other.URL, http.StatusFound).ServeHTTP)
	if _, err := p.Departures(t.Context(), query("f-x:1", time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !followed.Load() || leaked.Load() {
		t.Errorf("redirect followed = %v, key sent = %v; want it followed without the key", followed.Load(), leaked.Load())
	}
}

func TestConvert(t *testing.T) {
	row := func(dep, rel, route string) string {
		return fmt.Sprintf(`{"schedule_relationship":%q,"departure":%s,"trip":{"trip_id":"t","route":{"route_id":"1"%s}}}`, rel, dep, route)
	}
	const sched = `{"scheduled_utc":"2026-11-25T16:09:34Z"}`
	// tripRow has the relationship on the trip alone.
	tripRow := func(rel string) string {
		return strings.Replace(row(sched, "STATIC", `,"route_type":3`), `"trip_id":"t"`, fmt.Sprintf(`"trip_id":"t","schedule_relationship":%q`, rel), 1)
	}
	at := now.Add(5 * time.Minute)
	dep := func(mode transit.Mode, st transit.Status, predicted *time.Time) []transit.Departure {
		return []transit.Departure{{
			Line:   transit.Line{ID: "1", ShortName: "1", Mode: mode},
			StopID: "f-x:1", Scheduled: at, Predicted: predicted, Status: st,
		}}
	}
	tests := map[string]struct {
		row  string
		want []transit.Departure
	}{
		"skipped stop":                {row(sched, "SKIPPED", `,"route_type":3`), []transit.Departure{}},
		"trip ends here":              {strings.Replace(row(sched, "STATIC", ""), "{", `{"pickup_type":1,`, 1), []transit.Departure{}},
		"listed twice":                {row(sched, "STATIC", `,"route_type":3`) + "," + row(sched, "STATIC", `,"route_type":3`), dep(transit.ModeBus, transit.StatusScheduled, nil)},
		"listed twice, zero offset":   {row(sched, "STATIC", `,"route_type":3`) + "," + row(strings.Replace(sched, "Z", "+00:00", 1), "STATIC", `,"route_type":3`), dep(transit.ModeBus, transit.StatusScheduled, nil)},
		"deleted trips are not shown": {row(sched, "DELETED", `,"route_type":3`), []transit.Departure{}},
		"replacement runs as planned": {row(sched, "REPLACEMENT", `,"route_type":3`), dep(transit.ModeBus, transit.StatusScheduled, nil)},
		"added trip has no schedule":  {row(`{"scheduled_utc":null,"estimated_utc":"2026-11-25T16:09:34Z"}`, "ADDED", `,"route_type":3`), dep(transit.ModeBus, transit.StatusAdded, &at)},
		"trip skipped":                {tripRow("SKIPPED"), []transit.Departure{}},
		"trip deleted":                {tripRow("DELETED"), []transit.Departure{}},
		"trip canceled":               {tripRow("CANCELED"), dep(transit.ModeBus, transit.StatusCanceled, nil)},
		"trip added":                  {tripRow("ADDED"), dep(transit.ModeBus, transit.StatusAdded, nil)},
		"no time at all":              {row(`{}`, "ADDED", `,"route_type":3`), []transit.Departure{}},
		"missing route type":          {row(sched, "STATIC", ""), dep(transit.ModeOther, transit.StatusScheduled, nil)},
		"null route type":             {row(sched, "STATIC", `,"route_type":null`), dep(transit.ModeOther, transit.StatusScheduled, nil)},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := static(t, `{"stops":[{"departures":[`+tt.row+`]}]}`).Departures(t.Context(), query("f-x:1", time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("departures (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBlankSearchSpendsNothing(t *testing.T) {
	s := newServer(t)
	p := newProvider(t, s, transitland.Config{Feed: feed})
	stops, err := p.SearchStops(t.Context(), "  ")
	if err != nil || len(stops) != 0 || s.calls.Load() != 0 {
		t.Errorf("stops %v, err %v, %d requests; want none", stops, err, s.calls.Load())
	}
}

type trip struct {
	id        string
	scheduled time.Time
	late      time.Duration
	// overnight puts the trip on the service day before its calendar date.
	overnight bool
}

// timetableServer answers as the Transitland server does: it filters by
// scheduled time, taking date, start_time and end_time on the clock of each
// service day, and starting next at the server's own clock.
type timetableServer struct {
	*httptest.Server
	trips []trip
	zone  *time.Location

	mu      sync.Mutex
	clock   time.Time
	queries []url.Values
}

func newTimetableServer(t *testing.T, zone *time.Location, trips ...trip) *timetableServer {
	t.Helper()
	s := &timetableServer{trips: trips, zone: zone}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *timetableServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()
	s.queries = append(s.queries, q)
	local := s.clock.In(s.zone)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	from := time.Duration(local.Hour())*time.Hour + time.Duration(local.Minute())*time.Minute + time.Duration(local.Second())*time.Second
	next, _ := strconv.Atoi(q.Get("next"))
	to := from + time.Duration(next)*time.Second
	if q.Get("start_time") != "" {
		var err error
		if date, err = time.Parse(time.DateOnly, q.Get("date")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		at := func(v string) time.Duration {
			var h, m, sec int
			_, _ = fmt.Sscanf(v, "%d:%d:%d", &h, &m, &sec)
			return time.Duration(h*3600+m*60+sec) * time.Second
		}
		from, to = at(q.Get("start_time")), at(q.Get("end_time"))
	}
	const day = 24 * time.Hour
	var rows []string
	for _, tr := range s.trips {
		l := tr.scheduled.In(s.zone)
		service := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
		if tr.overnight {
			service = service.AddDate(0, 0, -1)
		}
		noon := time.Date(service.Year(), service.Month(), service.Day(), 12, 0, 0, 0, s.zone)
		clock := tr.scheduled.Sub(noon.Add(-12 * time.Hour))
		var listed bool
		switch service.Sub(date) {
		case -day:
			listed = from < 6*time.Hour && clock >= day+from
		case 0:
			listed = clock >= from && clock <= to
		case day:
			listed = to > day && clock <= to-day
		}
		if !listed {
			continue
		}
		secs := int(clock / time.Second)
		rows = append(rows, fmt.Sprintf(
			`{"service_date":%q,"departure":{"scheduled":"%02d:%02d:%02d","scheduled_utc":%q,"estimated_utc":%q},"trip":{"trip_id":%q,"route":{"route_id":"1","route_type":3}}}`,
			service.Format(time.DateOnly), secs/3600, secs/60%60, secs%60, tr.scheduled.UTC().Format(time.RFC3339),
			tr.scheduled.Add(tr.late).UTC().Format(time.RFC3339), tr.id))
	}
	_, _ = w.Write([]byte(`{"stops":[{"departures":[` + strings.Join(rows, ",") + `]}]}`))
}

// poll moves the server's clock to at and asks for departures then.
func (s *timetableServer) poll(t *testing.T, p *transitland.Provider, at time.Time) []transit.Departure {
	t.Helper()
	s.mu.Lock()
	s.clock = at
	s.mu.Unlock()
	q := query("f-x:1", time.Hour)
	q.Now = at
	deps, err := p.Departures(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	transittest.CheckDepartures(t, q, deps)
	return deps
}

func (s *timetableServer) provider(t *testing.T) *transitland.Provider {
	t.Helper()
	p, err := transitland.New(transitland.Config{APIKey: "k", BaseURL: s.URL}, s.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLateTripsStayUntilTheyLeave(t *testing.T) {
	tests := map[string]struct {
		late time.Duration
		// poll is when the second fetch happens, after the scheduled time.
		poll time.Duration
		want bool
	}{
		"late at the second fetch": {late: 10 * time.Minute, poll: 5 * time.Minute, want: true},
		"left by then":             {late: 2 * time.Minute, poll: 5 * time.Minute},
		"later than the lookback":  {late: 2 * time.Hour, poll: 100 * time.Minute},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newTimetableServer(t, time.FixedZone("", -8*3600),
				trip{id: "late", scheduled: now.Add(time.Minute), late: tt.late}, trip{id: "other", scheduled: now.Add(50 * time.Minute)})
			p := s.provider(t)
			s.poll(t, p, now)
			deps := s.poll(t, p, now.Add(tt.poll))
			got := len(deps) > 0 && deps[0].Scheduled.Equal(now.Add(time.Minute))
			if got != tt.want {
				t.Errorf("late trip shown = %v, want %v: %v", got, tt.want, deps)
			}
		})
	}
}

func TestLookbackRequest(t *testing.T) {
	at := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		zone *time.Location
		want url.Values
	}{
		"west of UTC": {
			zone: time.FixedZone("", -8*3600), // the second fetch is at 10:05
			want: url.Values{"date": {"2026-11-25"}, "start_time": {"08:35:00"}, "end_time": {"12:10:00"}},
		},
		"half-hour zone": {
			zone: time.FixedZone("", 5*3600+1800), // the second fetch is at 23:35
			want: url.Values{"date": {"2026-11-25"}, "start_time": {"22:05:00"}, "end_time": {"25:40:00"}},
		},
		"just after midnight": {
			zone: time.FixedZone("", 6*3600), // the second fetch is at 00:05 on the 26th
			want: url.Values{"date": {"2026-11-25"}, "start_time": {"22:35:00"}, "end_time": {"26:10:00"}},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newTimetableServer(t, tt.zone, trip{id: "a", scheduled: at.Add(20 * time.Minute)})
			p := s.provider(t)
			s.poll(t, p, at)
			s.poll(t, p, at.Add(5*time.Minute))
			if len(s.queries) != 2 {
				t.Fatalf("%d requests, want 2", len(s.queries))
			}
			if s.queries[0].Get("next") == "" {
				t.Errorf("first request %v, want next: the offset is not known yet", s.queries[0])
			}
			got := s.queries[1]
			got.Del("limit")
			got.Del("use_service_window")
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("second request (-want +got):\n%s", diff)
			}
		})
	}
}

// The night clocks change, the offset learned from the last trips of one
// service day is an hour wrong for the next.
func TestClockChange(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]time.Time{
		"back":    time.Date(2026, 11, 1, 5, 58, 0, 0, time.UTC),
		"forward": time.Date(2027, 3, 14, 6, 58, 0, 0, time.UTC),
	}
	for name, at := range tests {
		t.Run(name, func(t *testing.T) {
			second := at.Add(5 * time.Minute)
			trips := []trip{{id: "overnight", scheduled: at.Add(2 * time.Minute), overnight: true}}
			for m := 1; m <= 59; m += 2 {
				trips = append(trips, trip{id: strconv.Itoa(m), scheduled: second.Add(time.Duration(m) * time.Minute)})
			}
			s := newTimetableServer(t, zone, trips...)
			p := s.provider(t)
			s.poll(t, p, at)
			if got := len(s.poll(t, p, second)); got != 30 {
				t.Errorf("%d departures after the change, want 30", got)
			}
			if s.queries[1].Get("start_time") == "" {
				t.Errorf("request %v, want the lookback", s.queries[1])
			}
		})
	}
}

func TestOffsetAge(t *testing.T) {
	// Daylight saving may have changed since an old offset was learned.
	tests := map[string]struct {
		age      time.Duration
		wantNext bool
	}{
		"12 hours":  {12 * time.Hour, false},
		"any older": {12*time.Hour + time.Second, true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newTimetableServer(t, time.UTC, trip{id: "a", scheduled: now.Add(20 * time.Minute)})
			p := s.provider(t)
			s.poll(t, p, now)
			s.poll(t, p, now.Add(tt.age))
			if got := s.queries[1]; (got.Get("next") != "") != tt.wantNext {
				t.Errorf("request %v, want next = %v", got, tt.wantNext)
			}
		})
	}
}

func TestOffsetFromServiceDay(t *testing.T) {
	tests := map[string]struct {
		date, scheduled string
		// wantStart is the second request's start_time, at 16:09:34 UTC less
		// the lookback and an hour, or empty when the offset cannot be learned.
		wantStart string
	}{
		"same day":            {"2026-11-25", "11:30:00", "09:39:34"},
		"past 24:00":          {"2026-11-24", "35:30:00", "09:39:34"},
		"date does not match": {"2026-11-24", "11:30:00", ""},
		"far ahead":           {"2026-11-26", "11:30:00", ""},
		"no date":             {"", "11:30:00", ""},
		"malformed time":      {"2026-11-25", "11:30", ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var last url.Values
			p := serving(t, func(w http.ResponseWriter, r *http.Request) {
				last = r.URL.Query()
				_, _ = fmt.Fprintf(w, `{"stops":[{"departures":[{"service_date":%q,"departure":{"scheduled":%q,"scheduled_utc":"2026-11-25T16:30:00Z"},"trip":{"trip_id":"t","route":{"route_id":"1"}}}]}]}`, tt.date, tt.scheduled)
			})
			for _, after := range []time.Duration{0, p.Quota().RefreshInterval} {
				q := query("f-x:1", time.Hour)
				q.Now = now.Add(after)
				if _, err := p.Departures(t.Context(), q); err != nil {
					t.Fatal(err)
				}
			}
			if got := last.Get("start_time"); got != tt.wantStart {
				t.Errorf("start_time %q, want %q (request %v)", got, tt.wantStart, last)
			}
		})
	}
}

func TestStopLines(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.EscapedPath())
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ":HC") {
			_, _ = w.Write([]byte(`{"stops":[{"route_stops":[
				{"route":{"onestop_id":"r-b","route_short_name":"B"}},
				{"route":{"onestop_id":"r-a","route_short_name":"","route_long_name":"Airport"}},
				{"route":{"onestop_id":"r-b","route_short_name":"B"}}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"stops":[{"route_stops":[]}]}`))
	}))
	t.Cleanup(srv.Close)
	p, err := transitland.New(transitland.Config{Feed: feed, APIKey: "k", BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.StopLines(t.Context(), []string{"HC", "QUIET"}, now)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range got["HC"] {
		names = append(names, l.ShortName)
	}
	if diff := cmp.Diff([]string{"Airport", "B"}, names); diff != "" || len(got) != 1 {
		t.Errorf("lines %v (-want +got):\n%s", got, diff)
	}
	if len(paths) != 2 {
		t.Errorf("requests %v, want one per stop", paths)
	}
}
