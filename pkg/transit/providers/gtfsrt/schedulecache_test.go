package gtfsrt_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/gtfsrt"
)

var scheduleQuery = transit.Query{StopID: "235", Now: mtaRecorded, Window: 3 * time.Minute}

// cachedProvider is a new provider, as a new process would build, sharing dir.
func (a *agency) cachedProvider(t *testing.T, dir string, mutate ...func(*gtfsrt.Config)) *gtfsrt.Provider {
	t.Helper()
	cfg := gtfsrt.Config{StaticURL: a.URL + "/gtfs.zip", RealtimeURLs: []string{a.URL + "/realtime.pb"}}
	for _, m := range mutate {
		m(&cfg)
	}
	p, err := gtfsrt.New(cfg, a.Client(), gtfsrt.WithCacheDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// departAt queries stop 235 at now, which is also when the schedule's age is
// judged. Away from the recorded time the feed has no departures, but a nil
// error still shows the schedule loaded: a failed load is a PartialError.
func departAt(t *testing.T, p *gtfsrt.Provider, now time.Time) ([]transit.Departure, error) {
	t.Helper()
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "235", Now: now, Window: 3 * time.Minute})
	p.WaitForSchedule()
	return deps, err
}

func hasColors(deps []transit.Departure) bool { return len(deps) > 0 && deps[0].Line.Color != "" }

func cacheFiles(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestCachedScheduleIsReused(t *testing.T) {
	a, dir := newAgency(t, "mta"), t.TempDir()
	for range 2 {
		deps, err := departAt(t, a.cachedProvider(t, dir), mtaRecorded)
		if err != nil || !hasColors(deps) {
			t.Fatalf("err = %v, colors %v", err, hasColors(deps))
		}
	}
	if n := a.staticCalls.Load(); n != 1 {
		t.Errorf("schedule downloaded %d times, want 1", n)
	}
}

func TestExpiredScheduleIsRevalidated(t *testing.T) {
	a, dir := newAgency(t, "mta"), t.TempDir()
	a.etag = `"v1"`
	for _, at := range []time.Time{mtaRecorded, mtaRecorded.Add(25 * time.Hour), mtaRecorded.Add(26 * time.Hour)} {
		if _, err := departAt(t, a.cachedProvider(t, dir), at); err != nil {
			t.Fatalf("at %v: %v", at, err)
		}
	}
	if n := a.notModified.Load(); n != 1 {
		t.Errorf("%d answers were 304, want 1", n)
	}
	if n := a.staticCalls.Load(); n != 2 {
		t.Errorf("%d schedule requests, want 2: a 304 renews the file", n)
	}
}

func TestExpiredScheduleWithoutValidators(t *testing.T) {
	a, dir := newAgency(t, "mta"), t.TempDir()
	for _, at := range []time.Time{mtaRecorded, mtaRecorded.Add(25 * time.Hour)} {
		if _, err := departAt(t, a.cachedProvider(t, dir), at); err != nil {
			t.Fatal(err)
		}
	}
	if n, m := a.staticCalls.Load(), a.notModified.Load(); n != 2 || m != 0 {
		t.Errorf("%d requests and %d 304s, want 2 full downloads", n, m)
	}
}

func TestFailedRefreshUsesTheCachedSchedule(t *testing.T) {
	a, dir := newAgency(t, "mta"), t.TempDir()
	if _, err := departAt(t, a.cachedProvider(t, dir), mtaRecorded); err != nil {
		t.Fatal(err)
	}
	a.failStatic.Store(true)
	p := a.cachedProvider(t, dir)
	if _, err := departAt(t, p, mtaRecorded.Add(25*time.Hour)); !isPartial(err) {
		t.Errorf("err = %v, want the failed refresh reported", err)
	}
	deps, _ := p.Departures(t.Context(), scheduleQuery)
	if !hasColors(deps) {
		t.Error("the cached schedule was not used")
	}
}

func TestCorruptCacheFileIsReplaced(t *testing.T) {
	a, dir := newAgency(t, "mta"), t.TempDir()
	if _, err := departAt(t, a.cachedProvider(t, dir), mtaRecorded); err != nil {
		t.Fatal(err)
	}
	for _, e := range cacheFiles(t, dir) {
		if err := os.WriteFile(filepath.Join(dir, e.Name()), []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deps, err := departAt(t, a.cachedProvider(t, dir), mtaRecorded)
	if err != nil || !hasColors(deps) {
		t.Errorf("err = %v, colors %v", err, hasColors(deps))
	}
	if n := a.staticCalls.Load(); n != 2 {
		t.Errorf("schedule downloaded %d times, want 2", n)
	}
}

func TestUnusableCacheDir(t *testing.T) {
	a := newAgency(t, "mta")
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deps, err := departAt(t, a.cachedProvider(t, file), mtaRecorded)
	if err != nil || !hasColors(deps) {
		t.Errorf("err = %v, colors %v", err, hasColors(deps))
	}
}

func TestConcurrentWritersShareADir(t *testing.T) {
	a, dir := newAgency(t, "mta"), t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { _, errs[i] = departAt(t, a.cachedProvider(t, dir), mtaRecorded) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	before := a.staticCalls.Load()
	if _, err := departAt(t, a.cachedProvider(t, dir), mtaRecorded); err != nil {
		t.Fatal(err)
	}
	if after := a.staticCalls.Load(); after != before {
		t.Errorf("a later process downloaded again (%d → %d)", before, after)
	}
	if n := len(cacheFiles(t, dir)); n != 1 {
		t.Errorf("%d files in the cache, want 1", n)
	}
}

func TestCacheHoldsNoURL(t *testing.T) {
	a := newAgency(t, "mta")
	dir := filepath.Join(t.TempDir(), "cache")
	p := a.cachedProvider(t, dir, func(c *gtfsrt.Config) { c.StaticURL += "?key=SECRET" })
	if _, err := departAt(t, p, mtaRecorded); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(a.URL, "http://")
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v, %v; want mode 0700", fi.Mode(), err)
	}
	for _, e := range cacheFiles(t, dir) {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{"SECRET", host} {
			if strings.Contains(e.Name(), leak) || strings.Contains(string(b), leak) {
				t.Errorf("%s holds %q", e.Name(), leak)
			}
		}
		if fi, _ := e.Info(); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", e.Name(), fi.Mode())
		}
	}
}

func TestCanceledDownloadLeavesNoFile(t *testing.T) {
	a, dir := newAgency(t, "mta"), t.TempDir()
	a.staticGate = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		for a.staticCalls.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
		close(a.staticGate)
	}()
	_, err := a.cachedProvider(t, dir).Departures(ctx, scheduleQuery)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if es := cacheFiles(t, dir); len(es) != 0 {
		t.Errorf("files left behind: %v", es)
	}
}

func TestCacheOffWritesNothing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	deps, err := departAt(t, newAgency(t, "mta").cachedProvider(t, ""), mtaRecorded)
	if err != nil || !hasColors(deps) {
		t.Fatalf("err = %v, colors %v", err, hasColors(deps))
	}
	if es := cacheFiles(t, tmp); len(es) != 0 {
		t.Errorf("files written with the cache off: %v", es)
	}
}

// logMessages runs a provider sharing dir at each time and returns the
// messages it logged, in order.
func logMessages(t *testing.T, a *agency, dir string, times ...time.Time) []string {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := gtfsrt.Config{StaticURL: a.URL + "/gtfs.zip?key=secret", RealtimeURLs: []string{a.URL + "/realtime.pb"}}
	for _, at := range times {
		p, err := gtfsrt.New(cfg, a.Client(), gtfsrt.WithCacheDir(dir), gtfsrt.WithLogger(log))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := departAt(t, p, at); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("log holds the URL's query string:\n%s", buf.String())
	}
	var msgs []string
	for line := range strings.Lines(buf.String()) {
		var rec struct{ Msg string }
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, rec.Msg)
	}
	return msgs
}

func TestScheduleDownloadsAreLogged(t *testing.T) {
	downloaded := []string{"downloading GTFS schedule", "downloaded GTFS schedule"}
	tests := []struct {
		name  string
		cache bool
		times []time.Time
		want  []string
	}{
		{"cache off", false, []time.Time{mtaRecorded}, downloaded},
		{"cached schedule reused", true, []time.Time{mtaRecorded, mtaRecorded.Add(time.Hour)}, downloaded},
		{
			"unchanged schedule revalidated", true,
			[]time.Time{mtaRecorded, mtaRecorded.Add(25 * time.Hour)},
			append(downloaded, "revalidating GTFS schedule", "GTFS schedule unchanged"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAgency(t, "mta")
			a.etag = `"v1"`
			dir := ""
			if tt.cache {
				dir = t.TempDir()
			}
			if diff := cmp.Diff(tt.want, logMessages(t, a, dir, tt.times...)); diff != "" {
				t.Errorf("messages (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClearCache(t *testing.T) {
	a, dir := newAgency(t, "mta"), t.TempDir()
	if _, err := departAt(t, a.cachedProvider(t, dir), mtaRecorded); err != nil {
		t.Fatal(err)
	}
	cached := cacheFiles(t, dir)[0]
	info, err := cached.Info()
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, age time.Duration) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := mtaRecorded.Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write(".download-abandoned", time.Hour)
	write(".download-in-progress", time.Minute)
	write("notes.txt", time.Hour)

	n, freed, err := gtfsrt.ClearCache(dir, mtaRecorded)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || freed != info.Size()+5 {
		t.Errorf("removed %d schedules and %d bytes, want 1 and %d", n, freed, info.Size()+5)
	}
	var left []string
	for _, e := range cacheFiles(t, dir) {
		left = append(left, e.Name())
	}
	if diff := cmp.Diff([]string{".download-in-progress", "notes.txt"}, left); diff != "" {
		t.Errorf("files left (-want +got):\n%s", diff)
	}

	n, freed, err = gtfsrt.ClearCache(filepath.Join(dir, "missing"), mtaRecorded)
	if n != 0 || freed != 0 || err != nil {
		t.Errorf("missing dir: %d, %d, %v; want nothing removed and no error", n, freed, err)
	}
}
