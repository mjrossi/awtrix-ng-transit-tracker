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
			wantNext := (time.Hour + p.RefreshInterval()) / time.Second
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
		{4*time.Minute + 59*time.Second, time.Hour, 1},
		{5 * time.Minute, time.Hour, 2},
		// A board with another window gets its own request.
		{5 * time.Minute, 30 * time.Minute, 3},
		{6 * time.Minute, 30 * time.Minute, 3},
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
		"not found":     {status: http.StatusNotFound, stop: "HC", is: transitland.ErrUnknownStop},
		"unknown stop":  {stop: "NOPE", is: transitland.ErrUnknownStop},
		"stop not sent": {stop: "", is: transitland.ErrUnknownStop},
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
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "test-key") {
				t.Errorf("err = %v leaks the API key", err)
			}
		})
	}
}

func TestEmptyStopsListIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stops":[]}`))
	}))
	t.Cleanup(srv.Close)
	p, err := transitland.New(transitland.Config{APIKey: "k", BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Departures(t.Context(), query("f-x:1", time.Hour)); !errors.Is(err, transitland.ErrUnknownStop) {
		t.Errorf("err = %v, want ErrUnknownStop", err)
	}
}

func TestNew(t *testing.T) {
	tests := map[string]struct {
		cfg         transitland.Config
		wantErr     string
		wantRefresh time.Duration
		wantQuota   int
	}{
		"defaults":      {cfg: transitland.Config{APIKey: "k"}, wantRefresh: 5 * time.Minute, wantQuota: 10_000},
		"hobbyist":      {cfg: transitland.Config{APIKey: "k", RefreshInterval: "2m", MonthlyQuota: 50_000}, wantRefresh: 2 * time.Minute, wantQuota: 50_000},
		"no key":        {cfg: transitland.Config{}, wantErr: "api_key"},
		"blank key":     {cfg: transitland.Config{APIKey: "  "}, wantErr: "api_key"},
		"too fast":      {cfg: transitland.Config{APIKey: "k", RefreshInterval: "30s"}, wantErr: "at least 1m"},
		"too slow":      {cfg: transitland.Config{APIKey: "k", RefreshInterval: "6h"}, wantErr: "at most 1h"},
		"base query":    {cfg: transitland.Config{APIKey: "k", BaseURL: "https://x/?token=1"}, wantErr: "base_url"},
		"bad duration":  {cfg: transitland.Config{APIKey: "k", RefreshInterval: "5"}, wantErr: "refresh_interval"},
		"neg quota":     {cfg: transitland.Config{APIKey: "k", MonthlyQuota: -1}, wantErr: "monthly_quota"},
		"bad base url":  {cfg: transitland.Config{APIKey: "k", BaseURL: "ftp://x"}, wantErr: "base_url"},
		"explicit base": {cfg: transitland.Config{APIKey: "k", BaseURL: "https://mirror.example/rest"}, wantRefresh: 5 * time.Minute, wantQuota: 10_000},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p, err := transitland.New(tt.cfg, nil)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var m transit.Metered = p
			if m.RefreshInterval() != tt.wantRefresh || m.MonthlyQuota() != tt.wantQuota {
				t.Errorf("refresh %s quota %d, want %s and %d", m.RefreshInterval(), m.MonthlyQuota(), tt.wantRefresh, tt.wantQuota)
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

func TestFailureAfterData(t *testing.T) {
	const refresh = 10 * time.Minute
	tests := map[string]struct {
		after time.Duration
		// wantPartial expects departures with a *transit.PartialError;
		// otherwise only an error.
		wantPartial bool
	}{
		"one refresh missed":  {after: refresh, wantPartial: true},
		"just before the end": {after: 2*refresh - time.Second, wantPartial: true},
		"too old":             {after: 2 * refresh},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			p := newProvider(t, s, transitland.Config{Feed: feed, RefreshInterval: refresh.String()})
			if _, err := p.Departures(t.Context(), query("HC", 2*time.Hour)); err != nil {
				t.Fatal(err)
			}
			s.status.Store(http.StatusBadGateway)
			q := query("HC", 2*time.Hour)
			q.Now = now.Add(tt.after)
			deps, err := p.Departures(t.Context(), q)
			var partial *transit.PartialError
			if got := errors.As(err, &partial); got != tt.wantPartial {
				t.Fatalf("err = %v, partial = %v, want %v", err, got, tt.wantPartial)
			}
			if err == nil {
				t.Fatal("no error")
			}
			if !tt.wantPartial {
				return
			}
			transittest.CheckDepartures(t, q, deps)
			for _, d := range deps {
				if d.Realtime() {
					t.Errorf("%s at %s is still live after a failed fetch", d.Line.ShortName, d.Time())
				}
			}
		})
	}
}

func TestFailedRefreshRetriesSoonerThanRefresh(t *testing.T) {
	s := newServer(t)
	p := newProvider(t, s, transitland.Config{Feed: feed, RefreshInterval: "30m"})
	poll := func(after time.Duration) error {
		q := query("HC", 2*time.Hour)
		q.Now = now.Add(after)
		_, err := p.Departures(t.Context(), q)
		return err
	}
	if err := poll(0); err != nil {
		t.Fatal(err)
	}
	s.status.Store(http.StatusBadGateway)
	steps := []struct {
		after time.Duration
		calls int32
	}{
		{30 * time.Minute, 2},
		{34 * time.Minute, 2},
		{35 * time.Minute, 3},
	}
	for _, st := range steps {
		if err := poll(st.after); err == nil {
			t.Fatalf("after %s: no error", st.after)
		}
		if got := s.calls.Load(); got != st.calls {
			t.Fatalf("after %s: %d requests, want %d", st.after, got, st.calls)
		}
	}
	s.status.Store(0)
	if err := poll(40 * time.Minute); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestUnknownStopIsCached(t *testing.T) {
	s := newServer(t)
	p := newProvider(t, s, transitland.Config{Feed: feed, RefreshInterval: "30m"})
	for after := time.Duration(0); after < 30*time.Minute; after += time.Minute {
		q := query("NOPE", time.Hour)
		q.Now = now.Add(after)
		if _, err := p.Departures(t.Context(), q); !errors.Is(err, transitland.ErrUnknownStop) {
			t.Fatalf("after %s: err = %v", after, err)
		}
	}
	if got := s.calls.Load(); got != 1 {
		t.Errorf("%d requests for a mistyped stop, want 1", got)
	}
}

func TestOldPredictionsAreNotLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(departuresJSON(r, now, 60))
	}))
	t.Cleanup(srv.Close)
	p, err := transitland.New(transitland.Config{APIKey: "k", BaseURL: srv.URL, RefreshInterval: "30m"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		after time.Duration
		want  bool
	}{{0, true}, {10 * time.Minute, true}, {11 * time.Minute, false}, {29 * time.Minute, false}}
	for _, st := range steps {
		after, want := st.after, st.want
		q := query("f-x:1", time.Hour)
		q.Now = now.Add(after)
		deps, err := p.Departures(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(deps) == 0 {
			t.Fatalf("after %s: no departures", after)
		}
		for _, d := range deps {
			if d.Realtime() != want {
				t.Fatalf("after %s: live = %v, want %v", after, d.Realtime(), want)
			}
		}
	}
}

// departuresJSON lists n departures a minute apart from start, honoring the
// request's limit as the API does.
func departuresJSON(r *http.Request, start time.Time, n int) []byte {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var rows []string
	for i := range min(n, limit) {
		at := start.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		rows = append(rows, fmt.Sprintf(`{"departure":{"scheduled_utc":%q,"estimated_utc":%q},"trip":{"trip_id":"%d","route":{"route_id":"1","route_type":3}}}`, at, at, i))
	}
	return []byte(`{"stops":[{"departures":[` + strings.Join(rows, ",") + `]}]}`)
}

func TestBusyStop(t *testing.T) {
	tests := map[string]struct {
		listed      int
		window      time.Duration
		want        int
		wantPartial bool
	}{
		"more than 50 departures": {listed: 120, window: 90 * time.Minute, want: 91},
		"list comes back full":    {listed: 2000, window: 24 * time.Hour, want: 1000, wantPartial: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(departuresJSON(r, now, tt.listed))
			}))
			t.Cleanup(srv.Close)
			p, err := transitland.New(transitland.Config{APIKey: "k", BaseURL: srv.URL}, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			deps, err := p.Departures(t.Context(), query("f-x:1", tt.window))
			var partial *transit.PartialError
			if got := errors.As(err, &partial); got != tt.wantPartial || (err != nil && !got) {
				t.Fatalf("err = %v, want partial = %v", err, tt.wantPartial)
			}
			if len(deps) != tt.want {
				t.Errorf("%d departures, want %d", len(deps), tt.want)
			}
		})
	}
}

func TestRedirectsDoNotCarryTheKey(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("apikey") != "")
		_, _ = w.Write([]byte(`{"stops":[{"departures":[]}]}`))
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.RedirectHandler(other.URL, http.StatusFound))
	t.Cleanup(srv.Close)
	client := srv.Client()
	p, err := transitland.New(transitland.Config{APIKey: "test-key", BaseURL: srv.URL}, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Departures(t.Context(), query("f-x:1", time.Hour)); err == nil {
		t.Error("followed a redirect to another host")
	}
	if leaked.Load() {
		t.Error("the API key reached another host")
	}
	if client.CheckRedirect != nil {
		t.Error("the caller's client was modified")
	}
}

func TestConvert(t *testing.T) {
	row := func(dep, rel, route string) string {
		return fmt.Sprintf(`{"schedule_relationship":%q,"departure":%s,"trip":{"trip_id":"t","route":{"route_id":"1"%s}}}`, rel, dep, route)
	}
	const sched = `{"scheduled_utc":"2026-11-25T16:09:34Z"}`
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
		"deleted trips are not shown": {row(sched, "DELETED", `,"route_type":3`), []transit.Departure{}},
		"replacement runs as planned": {row(sched, "REPLACEMENT", `,"route_type":3`), dep(transit.ModeBus, transit.StatusScheduled, nil)},
		"added trip has no schedule":  {row(`{"scheduled_utc":null,"estimated_utc":"2026-11-25T16:09:34Z"}`, "ADDED", `,"route_type":3`), dep(transit.ModeBus, transit.StatusAdded, &at)},
		"no time at all":              {row(`{}`, "ADDED", `,"route_type":3`), []transit.Departure{}},
		"missing route type":          {row(sched, "STATIC", ""), dep(transit.ModeOther, transit.StatusScheduled, nil)},
		"null route type":             {row(sched, "STATIC", `,"route_type":null`), dep(transit.ModeOther, transit.StatusScheduled, nil)},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"stops":[{"departures":[` + tt.row + `]}]}`))
			}))
			t.Cleanup(srv.Close)
			p, err := transitland.New(transitland.Config{APIKey: "k", BaseURL: srv.URL}, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.Departures(t.Context(), query("f-x:1", time.Hour))
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
}

// timetableServer answers as the Transitland server does: it filters by
// scheduled time, taking date, start_time and end_time in the agency's zone
// and starting next at the server's own clock.
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
	from := s.clock
	next, _ := strconv.Atoi(q.Get("next"))
	to := from.Add(time.Duration(next) * time.Second)
	if q.Get("start_time") != "" {
		day, err := time.ParseInLocation(time.DateOnly, q.Get("date"), s.zone)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		at := func(v string) time.Time {
			var h, m, sec int
			_, _ = fmt.Sscanf(v, "%d:%d:%d", &h, &m, &sec)
			return day.Add(time.Duration(h*3600+m*60+sec) * time.Second)
		}
		from, to = at(q.Get("start_time")), at(q.Get("end_time"))
	}
	var rows []string
	for _, tr := range s.trips {
		if tr.scheduled.Before(from) || tr.scheduled.After(to) {
			continue
		}
		local := tr.scheduled.In(s.zone)
		rows = append(rows, fmt.Sprintf(
			`{"service_date":%q,"departure":{"scheduled":%q,"scheduled_utc":%q,"estimated_utc":%q},"trip":{"trip_id":%q,"route":{"route_id":"1","route_type":3}}}`,
			local.Format(time.DateOnly), local.Format(time.TimeOnly), tr.scheduled.UTC().Format(time.RFC3339),
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
		"later than the lookback":  {late: time.Hour, poll: 40 * time.Minute},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newTimetableServer(t, time.FixedZone("", -8*3600),
				trip{"late", now.Add(time.Minute), tt.late}, trip{"other", now.Add(50 * time.Minute), 0})
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
			want: url.Values{"date": {"2026-11-25"}, "start_time": {"09:35:00"}, "end_time": {"11:10:00"}},
		},
		"half-hour zone": {
			zone: time.FixedZone("", 5*3600+1800), // the second fetch is at 23:35
			want: url.Values{"date": {"2026-11-25"}, "start_time": {"23:05:00"}, "end_time": {"24:40:00"}},
		},
		"just after midnight": {
			zone: time.FixedZone("", 6*3600), // the second fetch is at 00:05 on the 26th
			want: url.Values{"date": {"2026-11-25"}, "start_time": {"23:35:00"}, "end_time": {"25:10:00"}},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newTimetableServer(t, tt.zone, trip{"a", at.Add(20 * time.Minute), 0})
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

func TestOldOffsetIsNotUsed(t *testing.T) {
	s := newTimetableServer(t, time.UTC, trip{"a", now.Add(20 * time.Minute), 0})
	p := s.provider(t)
	s.poll(t, p, now)
	// Daylight saving may have changed since.
	s.poll(t, p, now.Add(48*time.Hour))
	if got := s.queries[1]; got.Get("next") == "" {
		t.Errorf("request %v, want next", got)
	}
}
