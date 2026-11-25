package gtfsrt_test

import (
	"archive/zip"
	"bytes"
	"context"
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
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/gtfsrt"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/transittest"
)

// Feed header timestamps of the recorded realtime files.
var (
	mtaRecorded  = time.Date(2026, 11, 23, 13, 41, 9, 0, time.UTC)
	bartRecorded = time.Date(2026, 11, 23, 13, 41, 12, 0, time.UTC)
)

type agency struct {
	*httptest.Server
	staticCalls   atomic.Int32
	realtimeCalls atomic.Int32
	failStatic    atomic.Bool
	failRealtime  atomic.Bool
	wantHeader    string
	// staticGate, when set, holds schedule downloads until it is closed.
	staticGate chan struct{}
	// etag, when set, is sent with the schedule, and a request that already
	// has it is answered 304 Not Modified.
	etag        string
	notModified atomic.Int32
}

// newAgency serves testdata/<dir> like an agency: the static CSV files
// zipped at /gtfs.zip and the recorded feed at /realtime.pb.
func newAgency(t *testing.T, dir string) *agency {
	t.Helper()
	return newAgencyWithout(t, dir, nil)
}

// newAgencyWithout is newAgency with the schedule lines for which drop
// returns true left out.
func newAgencyWithout(t *testing.T, dir string, drop func(file, line string) bool) *agency {
	t.Helper()
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	for _, name := range []string{"routes.txt", "stops.txt", "trips.txt"} {
		b, err := os.ReadFile(filepath.Join("testdata", dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if drop != nil {
			var kept []string
			for line := range strings.SplitSeq(string(b), "\n") {
				if !drop(name, line) {
					kept = append(kept, line)
				}
			}
			b = []byte(strings.Join(kept, "\n"))
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	rt, err := os.ReadFile(filepath.Join("testdata", dir, "realtime.pb"))
	if err != nil {
		t.Fatal(err)
	}

	a := &agency{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gtfs.zip", func(w http.ResponseWriter, r *http.Request) {
		a.staticCalls.Add(1)
		if a.staticGate != nil {
			<-a.staticGate
		}
		if a.failStatic.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if a.etag != "" {
			w.Header().Set("ETag", a.etag)
			if r.Header.Get("If-None-Match") == a.etag {
				a.notModified.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		_, _ = w.Write(zipped.Bytes())
	})
	mux.HandleFunc("GET /realtime.pb", func(w http.ResponseWriter, r *http.Request) {
		a.realtimeCalls.Add(1)
		if a.failRealtime.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if a.wantHeader != "" && r.Header.Get("X-Api-Key") != a.wantHeader {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = w.Write(rt)
	})
	a.Server = httptest.NewServer(mux)
	t.Cleanup(a.Close)
	return a
}

func (a *agency) provider(t *testing.T, mutate ...func(*gtfsrt.Config)) *gtfsrt.Provider {
	t.Helper()
	cfg := gtfsrt.Config{StaticURL: a.URL + "/gtfs.zip", RealtimeURLs: []string{a.URL + "/realtime.pb"}}
	for _, m := range mutate {
		m(&cfg)
	}
	p, err := gtfsrt.New(cfg, a.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContract(t *testing.T) {
	transittest.Run(t, newAgency(t, "mta").provider(t), []transittest.Case{
		{Name: "parent station", Query: transit.Query{StopID: "235", Now: mtaRecorded, Window: 15 * time.Minute}},
		{Name: "platform", Query: transit.Query{StopID: "235N", Now: mtaRecorded, Window: 15 * time.Minute}},
		{Name: "station not in this feed", Query: transit.Query{StopID: "A27", Now: mtaRecorded, Window: time.Hour}, WantEmpty: true},
	})
	transittest.Run(t, newAgency(t, "bart").provider(t), []transittest.Case{
		{Name: "bart", Query: transit.Query{StopID: "EMBR", Now: bartRecorded, Window: 15 * time.Minute}},
	})
}

type row struct{ Time, Scheduled, Line, Color, Headsign string }

func summarize(deps []transit.Departure) []row {
	var out []row
	for _, d := range deps {
		out = append(out, row{d.Time().UTC().Format("15:04:05"), d.Scheduled.UTC().Format("15:04:05"), d.Line.ShortName, d.Line.Color, d.Headsign})
	}
	return out
}

// New York's realtime trip IDs do not match its schedule, so destinations
// come from each trip's last stop, named after its parent station.
func TestMTAParentStationAndLastStopHeadsign(t *testing.T) {
	p := newAgency(t, "mta").provider(t)
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "235", Now: mtaRecorded, Window: 3 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	want := []row{
		{"13:42:51", "13:42:51", "4", "#009952", "149 St-Hostos"},
		{"13:43:03", "13:43:03", "2", "#D82233", "Flatbush Av-Brooklyn College"},
		{"13:43:41", "13:43:41", "3", "#D82233", "New Lots Av"},
		{"13:43:59", "13:43:59", "5", "#009952", "Flatbush Av-Brooklyn College"},
	}
	if diff := cmp.Diff(want, summarize(deps)); diff != "" {
		t.Errorf("departures mismatch (-want +got):\n%s", diff)
	}
	if deps[0].Line.Mode != transit.ModeMetro {
		t.Errorf("mode = %s, want metro from route_type 1", deps[0].Line.Mode)
	}

	// A platform ID narrows to one direction.
	all, _ := p.Departures(t.Context(), transit.Query{StopID: "235", Now: mtaRecorded, Window: time.Hour})
	north, err := p.Departures(t.Context(), transit.Query{StopID: "235N", Now: mtaRecorded, Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(north) == 0 || len(north) >= len(all) {
		t.Errorf("235N has %d departures of the station's %d; want a non-empty subset", len(north), len(all))
	}
}

// BART leaves route_id out of its updates, so lines come from trips.txt,
// and it sends delays, which give the scheduled time.
func TestBARTRouteFromScheduleAndDelays(t *testing.T) {
	p := newAgency(t, "bart").provider(t)
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "EMBR", Now: bartRecorded, Window: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	want := []row{
		{"13:41:45", "13:41:23", "Yellow-N", "#FFFF33", "SFO / SF / Antioch"},
		{"13:42:37", "13:41:11", "Yellow-S", "#FFFF33", "San Francisco International Airport"},
	}
	if diff := cmp.Diff(want, summarize(deps)); diff != "" {
		t.Errorf("departures mismatch (-want +got):\n%s", diff)
	}
}

func TestWithoutStaticFeed(t *testing.T) {
	a := newAgency(t, "mta")
	p := a.provider(t, func(c *gtfsrt.Config) { c.StaticURL = "" })
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "235S", Now: mtaRecorded, Window: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) == 0 {
		t.Fatal("no departures")
	}
	// Without the schedule, the route ID stands in for the line and the
	// last stop ID for the destination.
	if d := deps[0]; d.Line.ShortName != d.Line.ID || d.Line.Color != "" || d.Headsign == "" {
		t.Errorf("departure %+v: want bare route ID, no color, last stop ID as headsign", d)
	}
	if a.staticCalls.Load() != 0 {
		t.Error("static feed fetched although static_url is empty")
	}
}

func TestCaching(t *testing.T) {
	a := newAgency(t, "mta")
	p := a.provider(t)
	for _, stop := range []string{"235", "127", "235"} {
		if _, err := p.Departures(t.Context(), transit.Query{StopID: stop, Now: mtaRecorded, Window: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	if s, r := a.staticCalls.Load(), a.realtimeCalls.Load(); s != 1 || r != 1 {
		t.Errorf("static fetched %d times, realtime %d; want 1 and 1 within the cache window", s, r)
	}
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "235", Now: mtaRecorded.Add(20 * time.Second), Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if r := a.realtimeCalls.Load(); r != 2 {
		t.Errorf("realtime fetched %d times, want 2 after the feed cache expires", r)
	}

	// A day later the schedule download fails; the cached one stays in use
	// and the failure is reported alongside the departures once the
	// background refresh has finished.
	a.failStatic.Store(true)
	later := mtaRecorded.Add(25 * time.Hour)
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "235", Now: later, Window: time.Hour}); err != nil {
		t.Errorf("err = %v, want the cached schedule while the refresh runs", err)
	}
	p.WaitForSchedule()
	if s := a.staticCalls.Load(); s != 2 {
		t.Errorf("static fetched %d times, want a refresh attempt after a day", s)
	}
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "235", Now: later.Add(time.Minute), Window: time.Hour}); !isPartial(err) {
		t.Errorf("err = %v, want the failure still reported", err)
	}
	if s := a.staticCalls.Load(); s != 2 {
		t.Errorf("static fetched %d times, want no retry within %v", s, 15*time.Minute)
	}
	// Back at the recorded time, so the feed has departures to check.
	deps, _ := p.Departures(t.Context(), transit.Query{StopID: "235", Now: mtaRecorded, Window: 3 * time.Minute})
	if len(deps) == 0 || deps[0].Line.Color == "" {
		t.Error("line colors lost after a failed schedule refresh")
	}
}

func isPartial(err error) bool {
	var pe *transit.PartialError
	return errors.As(err, &pe)
}

// Without a schedule, departures show with bare IDs and the failure is
// reported, while downloads are spaced out rather than retried every poll.
func TestStaticFailureBacksOff(t *testing.T) {
	a := newAgency(t, "mta")
	a.failStatic.Store(true)
	p := a.provider(t)
	q := func(after time.Duration) ([]transit.Departure, error) {
		// A platform ID: without the schedule, stations are unknown.
		return p.Departures(t.Context(), transit.Query{StopID: "235N", Now: mtaRecorded.Add(after), Window: time.Hour})
	}
	for _, after := range []time.Duration{0, time.Minute, 14 * time.Minute} {
		deps, err := q(after)
		if !isPartial(err) || !strings.Contains(err.Error(), "502") {
			t.Fatalf("after %v: err = %v, want a partial result naming the failure", after, err)
		}
		if len(deps) == 0 || deps[0].Line.Color != "" {
			t.Fatalf("after %v: want departures with bare IDs, got %+v", after, deps)
		}
	}
	if s := a.staticCalls.Load(); s != 1 {
		t.Errorf("static fetched %d times within 15 minutes, want 1", s)
	}

	a.failStatic.Store(false)
	deps, err := q(16 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if s := a.staticCalls.Load(); s != 2 || len(deps) == 0 || deps[0].Line.Color == "" {
		t.Errorf("static fetched %d times; want a retry after 15 minutes that loads colors", s)
	}
}

// The daily schedule refresh can take minutes; a board polling with a
// cached schedule must not wait for it, or its app expires on the clock.
func TestStaleScheduleRefreshesInBackground(t *testing.T) {
	a := newAgency(t, "mta")
	// One token lets the first download through; the refresh then waits.
	// The gate is set before any request so the handler reads it race-free.
	a.staticGate = make(chan struct{}, 1)
	a.staticGate <- struct{}{}
	// Runs before the server closes, which waits for a held download.
	release := sync.OnceFunc(func() { close(a.staticGate) })
	t.Cleanup(release)
	p := a.provider(t)
	q := transit.Query{StopID: "235", Now: mtaRecorded, Window: time.Hour}
	if _, err := p.Departures(t.Context(), q); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	q.Now = mtaRecorded.Add(25 * time.Hour)
	if _, err := p.Departures(ctx, q); err != nil {
		t.Fatalf("Departures waited for the refresh: %v", err)
	}
	q.Now = mtaRecorded
	deps, err := p.Departures(ctx, q)
	if err != nil || len(deps) == 0 || deps[0].Line.Color == "" {
		t.Fatalf("during the refresh got %d departures, err %v; want the cached schedule", len(deps), err)
	}

	release()
	p.WaitForSchedule()
	if s := a.staticCalls.Load(); s != 2 {
		t.Errorf("static fetched %d times, want one background refresh", s)
	}
}

// Boards sharing a provider poll it at the same moment at startup; they
// must share one download of each feed.
func TestConcurrentDeparturesShareDownloads(t *testing.T) {
	a := newAgency(t, "mta")
	a.staticGate = make(chan struct{})
	p := a.provider(t)
	q := transit.Query{StopID: "235", Now: mtaRecorded, Window: time.Hour}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			deps, err := p.Departures(t.Context(), q)
			if err == nil && (len(deps) == 0 || deps[0].Line.Color == "") {
				err = errors.New("departures without the schedule")
			}
			errs <- err
		})
	}
	for a.staticCalls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the others reach the wait
	close(a.staticGate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if s, r := a.staticCalls.Load(), a.realtimeCalls.Load(); s != 1 || r != 1 {
		t.Errorf("static fetched %d times, realtime %d; want 1 and 1", s, r)
	}
}

// New York splits its network across feeds. One failing feed must not
// blank boards whose stop it does not serve.
func TestFeedFailures(t *testing.T) {
	mta, bart := newAgency(t, "mta"), newAgency(t, "bart")
	p := mta.provider(t, func(c *gtfsrt.Config) {
		c.RealtimeURLs = []string{mta.URL + "/realtime.pb", bart.URL + "/realtime.pb"}
	})
	at := func(after time.Duration) transit.Query {
		return transit.Query{StopID: "235", Now: mtaRecorded.Add(after), Window: time.Hour}
	}

	bart.failRealtime.Store(true)
	deps, err := p.Departures(t.Context(), at(0))
	if !isPartial(err) || len(deps) == 0 {
		t.Fatalf("got %d departures, err %v; want departures with a partial error", len(deps), err)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v, want the failed feed's status", err)
	}

	// The feed that served this stop fails; the remaining one does not
	// serve it, so an empty answer would be wrong.
	bart.failRealtime.Store(false)
	mta.failRealtime.Store(true)
	if deps, err := p.Departures(t.Context(), at(time.Minute)); err == nil || isPartial(err) {
		t.Errorf("got %d departures, err %v; want a plain error", len(deps), err)
	}

	bart.failRealtime.Store(true)
	if _, err := p.Departures(t.Context(), at(2*time.Minute)); err == nil || isPartial(err) {
		t.Errorf("err = %v with every feed down; want a plain error", err)
	}
}

func TestFailuresAreCachedBriefly(t *testing.T) {
	a := newAgency(t, "mta")
	a.failRealtime.Store(true)
	p := a.provider(t)
	for _, stop := range []string{"235", "127"} {
		if _, err := p.Departures(t.Context(), transit.Query{StopID: stop, Now: mtaRecorded, Window: time.Hour}); err == nil {
			t.Fatal("want an error from a failing feed")
		}
	}
	if r := a.realtimeCalls.Load(); r != 1 {
		t.Errorf("realtime fetched %d times, want one shared failure within the cache window", r)
	}
}

// API keys in URLs must not reach logs through error messages.
func TestErrorsRedactURLs(t *testing.T) {
	a := newAgency(t, "mta")
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for name, u := range map[string]string{
		"status":  a.URL + "/missing.pb?api_key=SECRET",
		"network": closed.URL + "/realtime.pb?api_key=SECRET",
	} {
		t.Run(name, func(t *testing.T) {
			p := a.provider(t, func(c *gtfsrt.Config) { c.StaticURL, c.RealtimeURLs = "", []string{u} })
			_, err := p.Departures(t.Context(), transit.Query{StopID: "235", Now: mtaRecorded, Window: time.Hour})
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("err = %v; want an error without the key", err)
			}
		})
	}
	if _, err := gtfsrt.New(gtfsrt.Config{RealtimeURLs: []string{"ftp://x?key=SECRET"}}, nil); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("New err = %v; want an error without the key", err)
	}
}

func TestRedirectsDoNotCarryHeaders(t *testing.T) {
	var followed, leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		followed.Store(true)
		leaked.Store(r.Header.Get("X-Api-Key") != "" || r.Header.Get("Referer") != "")
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.RedirectHandler(other.URL, http.StatusFound))
	t.Cleanup(srv.Close)
	p, err := gtfsrt.New(gtfsrt.Config{
		RealtimeURLs: []string{srv.URL + "/?key=secret-key"},
		Headers:      map[string]string{"x-api-key": "secret-key"},
	}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "a", Now: bartRecorded, Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !followed.Load() || leaked.Load() {
		t.Errorf("redirect followed = %v, key sent = %v; want it followed without the key", followed.Load(), leaked.Load())
	}
}

func TestHeaders(t *testing.T) {
	a := newAgency(t, "mta")
	a.wantHeader = "secret-key"
	q := transit.Query{StopID: "235", Now: mtaRecorded, Window: time.Hour}
	if _, err := a.provider(t).Departures(t.Context(), q); err == nil {
		t.Error("request without the API key header succeeded")
	}
	withKey := a.provider(t, func(c *gtfsrt.Config) { c.Headers = map[string]string{"X-Api-Key": "secret-key"} })
	if _, err := withKey.Departures(t.Context(), q); err != nil {
		t.Errorf("with header: %v", err)
	}
}

func TestSearchStops(t *testing.T) {
	p := newAgency(t, "mta").provider(t)
	stops, err := p.SearchStops(t.Context(), "atlantic barclays")
	if err != nil {
		t.Fatal(err)
	}
	// Platforms such as 235N and 235S are folded into their station. The
	// complex has three stations in the schedule, one per group of lines.
	want := []transit.Stop{
		{ID: "235", Name: "Atlantic Av-Barclays Ctr"},
		{ID: "D24", Name: "Atlantic Av-Barclays Ctr"},
		{ID: "R31", Name: "Atlantic Av-Barclays Ctr"},
	}
	if diff := cmp.Diff(want, stops); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
	stops, err = p.SearchStops(t.Context(), "235N")
	if err != nil || len(stops) != 1 || stops[0].ID != "235N" {
		t.Errorf("search by platform ID: %v %v", stops, err)
	}
}

func TestNewValidates(t *testing.T) {
	bad := []gtfsrt.Config{
		{},
		{RealtimeURLs: []string{"ftp://x"}},
		{RealtimeURLs: []string{"https://x"}, StaticURL: "nope"},
	}
	for _, c := range bad {
		if _, err := gtfsrt.New(c, nil); err == nil {
			t.Errorf("New(%+v) succeeded", c)
		}
	}
}

// The schedule zip is staged in a temporary file, which must be removed,
// and a missing temporary directory falls back to downloading into memory.
func TestStaticTempFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	q := transit.Query{StopID: "235", Now: mtaRecorded, Window: 3 * time.Minute}

	deps, err := newAgency(t, "mta").provider(t).Departures(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) == 0 || deps[0].Line.Color == "" {
		t.Fatal("schedule not loaded through the temporary file")
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}

	t.Setenv("TMPDIR", filepath.Join(tmp, "missing"))
	deps, err = newAgency(t, "mta").provider(t).Departures(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) == 0 || deps[0].Line.Color == "" {
		t.Error("schedule not loaded through the in-memory fallback")
	}
}

func TestUnknownStop(t *testing.T) {
	tests := map[string]struct {
		stop   string
		change func(*agency)
		mutate func(*gtfsrt.Config)
		want   bool
	}{
		"not in schedule or feed": {stop: "NOPE", want: true},
		"in schedule, no trips":   {stop: "A27"},
		"no schedule":             {stop: "NOPE", mutate: func(c *gtfsrt.Config) { c.StaticURL = "" }},
		"schedule failed":         {stop: "NOPE", change: func(a *agency) { a.failStatic.Store(true) }},
		"a feed failed": {stop: "NOPE", mutate: func(c *gtfsrt.Config) {
			c.RealtimeURLs = append(c.RealtimeURLs, strings.Replace(c.RealtimeURLs[0], "realtime", "missing", 1))
		}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			a := newAgency(t, "mta")
			if tt.change != nil {
				tt.change(a)
			}
			var m []func(*gtfsrt.Config)
			if tt.mutate != nil {
				m = append(m, tt.mutate)
			}
			_, err := a.provider(t, m...).Departures(t.Context(), transit.Query{StopID: tt.stop, Now: mtaRecorded, Window: time.Hour})
			if got := errors.Is(err, transit.ErrUnknownStop); got != tt.want {
				t.Errorf("err = %v, want unknown stop %v", err, tt.want)
			}
		})
	}
}

// A platform the feed names but the schedule lacks is still served.
func TestStopInFeedButNotInSchedule(t *testing.T) {
	a := newAgencyWithout(t, "mta", func(file, line string) bool {
		return file == "stops.txt" && strings.HasPrefix(line, "235N,")
	})
	deps, err := a.provider(t).Departures(t.Context(), transit.Query{StopID: "235N", Now: mtaRecorded, Window: time.Hour})
	if err != nil || len(deps) == 0 {
		t.Errorf("got %d departures, err %v; want the feed's departures", len(deps), err)
	}
}

func TestStopName(t *testing.T) {
	p := newAgency(t, "mta").provider(t)
	var namer transit.StopNamer = p
	if name, ok := namer.StopName(t.Context(), "235", mtaRecorded); !ok || name != "Atlantic Av-Barclays Ctr" {
		t.Errorf("StopName(235) = %q, %v", name, ok)
	}
	if name, ok := namer.StopName(t.Context(), "NOPE", mtaRecorded); ok {
		t.Errorf("StopName(NOPE) = %q, want not found", name)
	}
}
