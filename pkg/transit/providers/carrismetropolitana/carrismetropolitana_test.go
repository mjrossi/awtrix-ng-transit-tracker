package carrismetropolitana_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/carrismetropolitana"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/transittest"
)

// recorded is when testdata was fetched; see testdata/README.md.
var recorded = time.Date(2026, 11, 23, 11, 30, 6, 0, time.UTC)

type server struct {
	*httptest.Server
	linesCalls atomic.Int32
	failLines  atomic.Bool
	// linesGate, when set, holds line requests until it is closed.
	linesGate chan struct{}
}

// newServer serves testdata the way the API does: known stops from their
// recorded files, 404 with an empty list for anything else.
func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{}
	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, file string) {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("[]"))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(b)
	}
	mux.HandleFunc("GET /v2/lines", func(w http.ResponseWriter, _ *http.Request) {
		s.linesCalls.Add(1)
		if s.linesGate != nil {
			<-s.linesGate
		}
		if s.failLines.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		serve(w, "lines.json")
	})
	mux.HandleFunc("GET /v2/stops", func(w http.ResponseWriter, _ *http.Request) { serve(w, "stops.json") })
	mux.HandleFunc("GET /v2/arrivals/by_stop/{id}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, "arrivals_"+r.PathValue("id")+".json")
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func newProvider(t *testing.T, s *server) *carrismetropolitana.Provider {
	t.Helper()
	p, err := carrismetropolitana.New(carrismetropolitana.Config{BaseURL: s.URL + "/v2"}, s.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContract(t *testing.T) {
	p := newProvider(t, newServer(t))
	transittest.Run(t, p, []transittest.Case{
		{Name: "stop with realtime", Query: transit.Query{StopID: "060001", Now: recorded, Window: time.Hour}},
		{Name: "stop with no service in window", Query: transit.Query{StopID: "060451", Now: recorded, Window: time.Hour}, WantEmpty: true},
	})
}

func TestDepartures(t *testing.T) {
	lisbon, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		t.Fatal(err)
	}
	p := newProvider(t, newServer(t))
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "060001", Now: recorded.In(lisbon), Window: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	type row struct {
		Line, Color, Headsign, Scheduled, Predicted string
	}
	var got []row
	for _, d := range deps {
		r := row{Line: d.Line.ShortName, Color: d.Line.Color, Headsign: d.Headsign, Scheduled: d.Scheduled.Format("15:04:05")}
		if d.Predicted != nil {
			r.Predicted = d.Predicted.Format("15:04:05")
		}
		got = append(got, r)
	}
	// The first bus was due at 11:20 but is predicted at 11:33:50, so it
	// stays on the board even though its schedule has passed. The 11:29
	// trip has no prediction and its schedule has passed, so it is gone.
	// The 12:00 trip is schedule-only.
	want := []row{
		{"4701", "#FDB71A", "Lisboa (Oriente)", "11:20:00", "11:33:50"},
		{"4701", "#FDB71A", "Lisboa (Oriente)", "11:50:00", "11:51:43"},
		{"4701", "#FDB71A", "Vale da Amoreira", "12:00:00", ""},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("departures mismatch (-want +got):\n%s", diff)
	}
	for _, d := range deps {
		if d.Line.ID != "4701" || d.Line.Mode != transit.ModeBus {
			t.Errorf("line = %+v, want bare ID 4701 and mode bus", d.Line)
		}
		if d.Scheduled.Location() != lisbon {
			t.Errorf("times should be in the query's location, got %s", d.Scheduled.Location())
		}
	}
}

func TestUnknownStop(t *testing.T) {
	p := newProvider(t, newServer(t))
	_, err := p.Departures(t.Context(), transit.Query{StopID: "999999", Now: recorded, Window: time.Hour})
	if !errors.Is(err, carrismetropolitana.ErrUnknownStop) {
		t.Fatalf("got %v, want ErrUnknownStop", err)
	}
}

func TestLinesCachedAndOptional(t *testing.T) {
	s := newServer(t)
	p := newProvider(t, s)
	q := transit.Query{StopID: "060001", Now: recorded, Window: time.Hour}

	for range 3 {
		if _, err := p.Departures(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.linesCalls.Load(); n != 1 {
		t.Errorf("lines fetched %d times, want 1 within a day", n)
	}

	// A day later the refresh fails; the cached colors must survive it.
	s.failLines.Store(true)
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "060001", Now: recorded.Add(25 * time.Hour), Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if n := s.linesCalls.Load(); n != 2 {
		t.Fatalf("lines fetched %d times, want a refresh attempt after a day", n)
	}
	deps, err := p.Departures(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if deps[0].Line.Color != "#FDB71A" {
		t.Errorf("color = %q after failed refresh, want cached #FDB71A", deps[0].Line.Color)
	}

	// With no line data at all, departures still show the bare number.
	fresh := newProvider(t, s)
	deps, err = fresh.Departures(t.Context(), transit.Query{StopID: "060001", Now: recorded, Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if l := deps[0].Line; l.ShortName != "4701" || l.Color != "" {
		t.Errorf("line = %+v, want short name 4701 and no color", l)
	}
}

// Boards sharing a provider poll it at the same moment at startup; they
// must share one download of the line list.
func TestConcurrentDeparturesShareLines(t *testing.T) {
	s := newServer(t)
	s.linesGate = make(chan struct{})
	release := sync.OnceFunc(func() { close(s.linesGate) })
	t.Cleanup(release) // before the server closes, which waits for it
	p := newProvider(t, s)
	q := transit.Query{StopID: "060001", Now: recorded, Window: time.Hour}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			deps, err := p.Departures(t.Context(), q)
			if err == nil && (len(deps) == 0 || deps[0].Line.Color == "") {
				err = errors.New("departures without line colors")
			}
			errs <- err
		})
	}
	for s.linesCalls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the others reach the wait
	release()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := s.linesCalls.Load(); n != 1 {
		t.Errorf("lines fetched %d times, want 1", n)
	}
}

// Without cached lines, a failing /lines endpoint is retried after a
// backoff rather than on every poll.
func TestLinesFailureBacksOff(t *testing.T) {
	s := newServer(t)
	s.failLines.Store(true)
	p := newProvider(t, s)
	for _, after := range []time.Duration{0, time.Minute, 14 * time.Minute} {
		deps, err := p.Departures(t.Context(), transit.Query{StopID: "060001", Now: recorded.Add(after), Window: time.Hour})
		if err != nil {
			t.Fatalf("after %v: %v", after, err)
		}
		if len(deps) > 0 && deps[0].Line.Color != "" {
			t.Fatalf("after %v: got a color without line data", after)
		}
	}
	if n := s.linesCalls.Load(); n != 1 {
		t.Errorf("lines fetched %d times within 15 minutes, want 1", n)
	}

	s.failLines.Store(false)
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "060001", Now: recorded, Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if n := s.linesCalls.Load(); n != 1 {
		t.Errorf("lines fetched %d times, want no retry before the backoff ends", n)
	}
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "060001", Now: recorded.Add(16 * time.Minute), Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if n := s.linesCalls.Load(); n != 2 || len(deps) == 0 || deps[0].Line.Color == "" {
		t.Errorf("lines fetched %d times; want a retry after 15 minutes that loads colors", n)
	}
}

func TestSearchStops(t *testing.T) {
	p := newProvider(t, newServer(t))
	ids := func(search string) []string {
		t.Helper()
		stops, err := p.SearchStops(t.Context(), search)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range stops {
			out = append(out, s.ID)
		}
		return out
	}
	tests := []struct {
		search string
		want   []string
	}{
		{"cais sodre", []string{"060419", "060451", "060452", "062000"}},
		{"CAIS DO SODRÉ metro", []string{"060451", "060452"}},
		{"060001", []string{"060001"}},
		{"nowhere", nil},
		{"   ", nil},
	}
	for _, tt := range tests {
		if diff := cmp.Diff(tt.want, ids(tt.search)); diff != "" {
			t.Errorf("SearchStops(%q) mismatch (-want +got):\n%s", tt.search, diff)
		}
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	for _, u := range []string{"ftp://x", "not a url", "http://"} {
		if _, err := carrismetropolitana.New(carrismetropolitana.Config{BaseURL: u}, nil); err == nil || !strings.Contains(err.Error(), "base_url") {
			t.Errorf("New(%q) = %v, want base_url error", u, err)
		}
	}
}
