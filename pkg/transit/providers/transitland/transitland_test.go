package transitland_test

import (
	"errors"
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

// now is the instant testdata is built around; see testdata/README.md.
var now = time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)

const feed = "f-9q9-bart"

type server struct {
	*httptest.Server
	calls atomic.Int32
	// status, when set, is returned instead of data.
	status atomic.Int32

	mu      sync.Mutex
	lastReq *http.Request
}

// newServer serves testdata for "<feed>:<stop>" keys whose file exists and
// 404 for anything else, as the API does for an unknown stop key.
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
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
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
		{Name: "station with platforms", Query: query("LAKE", time.Hour)},
		{Name: "short window", Query: query("LAKE", 10*time.Minute)},
		{Name: "no service", Query: query("EMPTY", time.Hour), WantEmpty: true},
	})
}

func TestDepartures(t *testing.T) {
	p := newProvider(t, newServer(t), transitland.Config{Feed: feed})
	got, err := p.Departures(t.Context(), query("LAKE", time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	at := func(m int) time.Time { return now.Add(time.Duration(m) * time.Minute) }
	ptr := func(m int) *time.Time { t := at(m); return &t }
	yellow := transit.Line{ID: "r-9q9-antioch~sfia~millbrae", ShortName: "Antioch - SFIA/Millbrae", Color: "#FFFF33", Mode: transit.ModeMetro}
	blue := transit.Line{ID: "r-9q9-dublin~pleasanton~daly~city", ShortName: "Dublin/Pleasanton - Daly City", Color: "#0099CC", Mode: transit.ModeMetro}
	bus := transit.Line{ID: "bridge", ShortName: "B1", Mode: transit.ModeBus}
	want := []transit.Departure{
		// Scheduled before now but predicted after it, with a stop headsign.
		{Line: blue, Headsign: "Dublin", StopID: "LAKE", Scheduled: at(-3), Predicted: ptr(2), Status: transit.StatusScheduled},
		// Listed under both platforms, kept once.
		{Line: yellow, Headsign: "SFO / Millbrae", StopID: "LAKE", Scheduled: at(4), Predicted: ptr(6), Status: transit.StatusScheduled},
		{Line: bus, Headsign: "Fruitvale", StopID: "LAKE", Scheduled: at(9), Predicted: ptr(9), Status: transit.StatusAdded},
		{Line: yellow, Headsign: "SFO / Millbrae", StopID: "LAKE", Scheduled: at(19), Status: transit.StatusScheduled},
		{Line: yellow, Headsign: "SFO / Millbrae", StopID: "LAKE", Scheduled: at(34), Status: transit.StatusCanceled},
		{Line: blue, Headsign: "Daly City", StopID: "LAKE", Scheduled: at(60), Status: transit.StatusScheduled},
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
		"feed set":           {transitland.Config{Feed: feed}, "LAKE", feed + ":LAKE"},
		"feed set, full key": {transitland.Config{Feed: feed}, feed + ":LAKE", feed + ":LAKE"},
		"no feed, full key":  {transitland.Config{}, feed + ":LAKE", feed + ":LAKE"},
		"custom refresh":     {transitland.Config{Feed: feed, RefreshInterval: "2m"}, "LAKE", feed + ":LAKE"},
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
			want := url.Values{"next": {strconv.Itoa(int(wantNext))}, "limit": {"50"}, "use_service_window": {"false"}}
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
		q := query("LAKE", window)
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
			if _, err := p.Departures(t.Context(), query("LAKE", time.Hour)); err != nil {
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
		"bad key":       {status: http.StatusUnauthorized, stop: "LAKE", want: "check api_key"},
		"forbidden":     {status: http.StatusForbidden, stop: "LAKE", want: "check api_key"},
		"quota":         {status: http.StatusTooManyRequests, stop: "LAKE", want: "quota"},
		"server error":  {status: http.StatusBadGateway, stop: "LAKE", want: "502"},
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
		"within a feed": {transitland.Config{Feed: feed}, feed, []string{"LAKE", "12TH"}},
		"every feed":    {transitland.Config{}, "", []string{feed + ":LAKE", feed + ":12TH"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			p := newProvider(t, s, tt.cfg)
			stops, err := p.SearchStops(t.Context(), " lake ")
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
			if q.Get("search") != "lake" || q.Get("feed_onestop_id") != tt.wantFeed {
				t.Errorf("query %v", q)
			}
		})
	}
}
