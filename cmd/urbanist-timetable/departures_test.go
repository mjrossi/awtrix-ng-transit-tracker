package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/scheduler"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// runCLIOut is runCLI that also returns what the command writes to stderr.
func runCLIOut(t *testing.T, src string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCommand()
	var out, errOut bytes.Buffer
	cmd.Writer, cmd.ErrWriter = &out, &errOut
	err = cmd.Run(t.Context(), append([]string{"urbanist-timetable", "--config", path, "--cache-dir", "off"}, args...))
	return out.String(), errOut.String(), err
}

// at fixes the command's clock for one test.
func at(t *testing.T, when time.Time) {
	t.Helper()
	old := clock
	clock = func() time.Time { return when }
	t.Cleanup(func() { clock = old })
}

var (
	mtaRecorded    = time.Date(2026, 11, 23, 13, 41, 9, 0, time.UTC)
	carrisRecorded = time.Date(2026, 11, 23, 11, 30, 6, 0, time.UTC)
)

// carrisServer serves the provider's recorded stop 060001 and 404 for
// every other stop.
func carrisServer(t *testing.T) string {
	t.Helper()
	dir := "../../pkg/transit/providers/carrismetropolitana/testdata"
	file := func(w http.ResponseWriter, name string) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write(b)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/lines", func(w http.ResponseWriter, _ *http.Request) { file(w, "lines.json") })
	mux.HandleFunc("GET /v2/arrivals/by_stop/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "060001" {
			http.Error(w, "[]", http.StatusNotFound)
			return
		}
		file(w, "arrivals_060001.json")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "[providers.cm]\ntype = \"carrismetropolitana\"\nbase_url = \"" + srv.URL + "/v2\"\n"
}

// gtfsServer serves the gtfsrt provider's recorded New York feeds. The
// realtime feed answers 500 when fail is set.
func gtfsServer(t *testing.T, fail bool) *httptest.Server {
	t.Helper()
	dir := "../../pkg/transit/providers/gtfsrt/testdata/mta"
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	for _, name := range []string{"routes.txt", "stops.txt", "trips.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		w, _ := zw.Create(name)
		_, _ = w.Write(b)
	}
	_ = zw.Close()
	rt, err := os.ReadFile(filepath.Join(dir, "realtime.pb"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gtfs.zip", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(zipped.Bytes()) })
	mux.HandleFunc("GET /realtime.pb", func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(rt)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDeparturesFromDemo(t *testing.T) {
	out, _, err := runCLIOut(t, demoConfig, "departures", "demo", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Reboleira") || !strings.HasPrefix(out, "s1 ") {
		t.Errorf("output %q, want the stop ID as title and the demo line", out)
	}
	out, _, err = runCLIOut(t, demoConfig, "departures", "--lines", "x", "demo", "s1")
	if err != nil || !strings.Contains(out, "No departures in the next 90 minutes") {
		t.Errorf("filtered out: %q, %v", out, err)
	}
}

func departuresJSON(t *testing.T, src string, args ...string) []map[string]any {
	t.Helper()
	out, _, err := runCLIOut(t, src, append([]string{"departures", "--json"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Source     string
		Departures []map[string]any
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("%v in %s", err, out)
	}
	if v.Source != args[0] {
		t.Errorf("source = %q, want %q", v.Source, args[0])
	}
	return v.Departures
}

func TestDeparturesJSONAndDuplicateStops(t *testing.T) {
	once := departuresJSON(t, demoConfig, "demo", "s1")
	if len(once) == 0 {
		t.Fatal("no departures")
	}
	if twice := departuresJSON(t, demoConfig, "demo", "s1", "s1"); len(twice) != len(once) {
		t.Errorf("s1 twice gave %d departures, once %d", len(twice), len(once))
	}
}

func TestDeparturesWithOneStopFailing(t *testing.T) {
	at(t, carrisRecorded)
	src := carrisServer(t)
	out, stderr, err := runCLIOut(t, src, "departures", "cm", "060001", "999999")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stderr, "warning: ") || !strings.Contains(out, "060001") {
		t.Errorf("stdout %q stderr %q; want the good stop and a warning", out, stderr)
	}
	_, _, err = runCLIOut(t, src, "departures", "cm", "999998", "999999")
	if !errors.Is(err, transit.ErrUnknownStop) || hint(err) != "find stop IDs with `urbanist-timetable stops search cm 999998 999999`" {
		t.Errorf("all failing: err = %v, hint %q", err, hint(err))
	}
}

func TestDeparturesFromFeedURLs(t *testing.T) {
	at(t, mtaRecorded)
	srv := gtfsServer(t, false)
	out, _, err := runCLIOut(t, "", "departures", "--static", srv.URL+"/gtfs.zip", "--realtime", srv.URL+"/realtime.pb", "235")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Atlantic Av-Barclays Ctr ") || !strings.Contains(out, "Flatbush Av-Brooklyn College") {
		t.Errorf("output %q, want the stop's name and its departures", out)
	}

	bad := gtfsServer(t, true)
	_, _, err = runCLIOut(t, "", "departures", "--realtime", bad.URL+"/realtime.pb?key=SECRET", "235")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v, want an error without the key", err)
	}
}

func TestDeparturesScheduleCache(t *testing.T) {
	at(t, mtaRecorded)
	srv := gtfsServer(t, false)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	args := []string{"departures", "--static", srv.URL + "/gtfs.zip", "--realtime", srv.URL + "/realtime.pb", "235"}
	if _, _, err := runCLIOut(t, "", args...); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(home); len(es) != 0 {
		t.Errorf("--cache-dir off wrote %v", es)
	}
	dir := filepath.Join(t.TempDir(), "c")
	if _, _, err := runCLIOut(t, "", append([]string{"--cache-dir", dir}, args...)...); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("cache dir holds %v, want one schedule", es)
	}
}

func TestDeparturesUsageErrors(t *testing.T) {
	tests := map[string]struct {
		args []string
		want string
	}{
		"no stop":            {[]string{"departures", "demo"}, "stop IDs"},
		"watch and json":     {[]string{"departures", "--watch", "--json", "demo", "s1"}, "--json"},
		"watch, no terminal": {[]string{"departures", "--watch", "demo", "s1"}, "terminal"},
		"short interval":     {[]string{"departures", "--watch", "--interval", "5s", "demo", "s1"}, "15s"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := runCLIOut(t, demoConfig, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

func TestWatch(t *testing.T) {
	start := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	tick := make(chan time.Time)
	shows := 0
	show := func(_ context.Context, now time.Time) (string, error) {
		shows++
		if shows == 2 {
			return "", errors.New("feed down")
		}
		return "board at " + now.Format("15:04:05") + "\n", nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	var out bytes.Buffer
	done := make(chan error)
	go func() { done <- watch(ctx, &out, start, tick, show) }()
	tick <- start.Add(30 * time.Second)
	tick <- start.Add(60 * time.Second)
	cancel()
	if err := <-done; err != nil {
		t.Errorf("watch returned %v after cancel", err)
	}
	frames := strings.Split(out.String(), clearScreen)[1:]
	if len(frames) != 3 {
		t.Fatalf("%d frames, want 3: %q", len(frames), out.String())
	}
	if frames[0] != "board at 08:00:00\n" {
		t.Errorf("first frame %q", frames[0])
	}
	if !strings.HasPrefix(frames[1], "board at 08:00:00\n") || !strings.Contains(frames[1], "last updated 30s ago: feed down") {
		t.Errorf("after a failure: %q", frames[1])
	}
	if frames[2] != "board at 08:01:00\n" {
		t.Errorf("recovered frame %q", frames[2])
	}
}

// failing answers with err, or with departures and err when partial.
type failing struct {
	err     error
	partial bool
}

func (f failing) Departures(_ context.Context, q transit.Query) ([]transit.Departure, error) {
	if f.partial {
		return []transit.Departure{{Line: transit.Line{ID: "1"}, StopID: q.StopID, Scheduled: q.Now.Add(time.Minute), Status: transit.StatusScheduled}}, &transit.PartialError{Err: f.err}
	}
	return nil, f.err
}

func TestServerTextCannotDriveTheTerminal(t *testing.T) {
	hostile := errors.New("GET x: 500 a\x1b]0;pwned\x07b\u202e")
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	var warnings bytes.Buffer
	q := query{boards: []boardQuery{{
		source: "x", window: time.Hour,
		stops: []scheduler.Source{{Provider: failing{err: hostile, partial: true}, StopID: "s"}},
	}}, warnings: &warnings}
	if _, err := q.show(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	var printed bytes.Buffer
	printError(&printed, hostile)
	var watched bytes.Buffer
	tick := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	shows := 0
	go func() {
		done <- watch(ctx, &watched, now, tick, func(context.Context, time.Time) (string, error) {
			shows++
			if shows == 1 {
				return "board\n", nil
			}
			return "", hostile
		})
	}()
	tick <- now.Add(time.Minute)
	cancel()
	<-done
	for name, s := range map[string]string{"warning": warnings.String(), "error": printed.String(), "watch": strings.ReplaceAll(watched.String(), clearScreen, "")} {
		if strings.ContainsAny(s, "\x1b\x07\u202e") {
			t.Errorf("%s output kept control characters: %q", name, s)
		}
	}
}
