package gtfsrt

import (
	"bytes"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// townWith is town with files replaced, or removed where the body is empty.
func townWith(files map[string]string) map[string]string {
	out := maps.Clone(town)
	for name, body := range files {
		if body == "" {
			delete(out, name)
		} else {
			out[name] = body
		}
	}
	return out
}

// endingOn is town with a calendar that runs until end.
func endingOn(end string) map[string]string {
	return townWith(map[string]string{
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\n" +
			"WK,1,1,1,1,1,0,0,20260101," + end + "\nSUN,0,0,0,0,0,0,1,20260101," + end + "\n",
		"calendar_dates.txt": "",
	})
}

func feedInfo(start, end, version string) string {
	return "feed_publisher_name,feed_publisher_url,feed_lang,feed_start_date,feed_end_date,feed_version\n" +
		"Town,https://example.com,en," + start + "," + end + "," + version + "\n"
}

func TestReadCoverage(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  coverage
	}{
		{"the calendar's range", nil, coverage{20260101, 20261231, ""}},
		{"calendar_dates past the calendar", map[string]string{
			"calendar_dates.txt": "service_id,date,exception_type\nSUN,20251224,1\nWK,20270102,1\n",
		}, coverage{20251224, 20270102, ""}},
		{"calendar_dates only", map[string]string{
			"calendar.txt":       "",
			"calendar_dates.txt": "service_id,date,exception_type\nWK,20260105,1\nWK,20260102,1\n",
		}, coverage{20260102, 20260105, ""}},
		{"the calendar outranks an out-of-date feed_info", map[string]string{
			"feed_info.txt": feedInfo("20250101", "20250830", "v7"),
		}, coverage{20260101, 20261231, "v7"}},
		{"feed_info without a calendar", map[string]string{
			"feed_info.txt": feedInfo("20260301", "20260630", "v7"), "calendar.txt": "", "calendar_dates.txt": "",
		}, coverage{20260301, 20260630, "v7"}},
		{"nothing to go by", map[string]string{"calendar.txt": "", "calendar_dates.txt": ""}, coverage{}},
		{"malformed feed_info dates are ignored", map[string]string{
			"feed_info.txt": feedInfo("2026-03-01", "soon", ""), "calendar.txt": "", "calendar_dates.txt": "",
		}, coverage{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := zipOf(t, townWith(tt.files))
			st, err := parseStatic(bytes.NewReader(b), int64(len(b)))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, st.coverage, cmp.AllowUnexported(coverage{})); diff != "" {
				t.Errorf("parseStatic coverage (-want +got):\n%s", diff)
			}
			only, err := readCoverageOnly(bytes.NewReader(b), int64(len(b)))
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, only, cmp.AllowUnexported(coverage{})); diff != "" {
				t.Errorf("readCoverageOnly (-want +got):\n%s", diff)
			}
		})
	}
}

func TestScheduleFreshUntil(t *testing.T) {
	since := at(2026, 3, 30, 8, 0)
	march := coverage{start: 20260301, end: 20260331}
	tests := []struct {
		name string
		st   *static
		want time.Time
	}{
		{"no schedule", nil, since.Add(staticTTL)},
		{"unknown coverage", &static{loc: lisbon}, since.Add(staticTTL)},
		{"ends after the next daily check", &static{loc: lisbon, coverage: coverage{end: 20260630}}, since.Add(staticTTL)},
		{"ends before the next daily check", &static{loc: lisbon, coverage: coverage{end: 20260330}}, at(2026, 3, 31, 0, 0)},
		{"ended, revalidatable", &static{loc: lisbon, coverage: coverage{end: 20260329}, revalidatable: true}, since.Add(outOfRangeRecheck)},
		{"ended, a check is a full download", &static{loc: lisbon, coverage: coverage{end: 20260329}}, since.Add(staticTTL)},
		{"not started, revalidatable", &static{loc: lisbon, coverage: coverage{start: 20260401}, revalidatable: true}, since.Add(outOfRangeRecheck)},
		{"in range, the daily check comes first", &static{loc: lisbon, coverage: march, revalidatable: true}, since.Add(staticTTL)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scheduleFreshUntil(tt.st, since); !got.Equal(tt.want) {
				t.Errorf("scheduleFreshUntil = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOutOfRange(t *testing.T) {
	c := coverage{start: 20260301, end: 20260331}
	tests := []struct {
		now  time.Time
		want error
	}{
		{at(2026, 2, 28, 23, 59), errScheduleNotStarted},
		{at(2026, 3, 1, 0, 0), nil},
		{at(2026, 3, 31, 23, 59), nil},
		{at(2026, 4, 1, 0, 0), errScheduleEnded},
	}
	for _, tt := range tests {
		err := c.outOfRange(tt.now, lisbon)
		if !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
			t.Errorf("outOfRange(%v) = %v, want %v", tt.now, err, tt.want)
		}
	}
	if err := (coverage{}).outOfRange(monday8, lisbon); err != nil {
		t.Errorf("unknown coverage: outOfRange = %v, want nil", err)
	}
}

// A schedule outside its coverage is shown with a warning, or fails where
// it is the only source and plans nothing, rather than claim no departures.
func TestScheduleOutOfRange(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		realtime bool
		want     error
		partial  bool
	}{
		{"ended: nothing planned, schedule only", map[string]string{
			"calendar.txt":       "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,0,0,20260101,20260329\n",
			"calendar_dates.txt": "",
		}, false, errScheduleEnded, false},
		{"ended: nothing planned, with realtime", map[string]string{
			"calendar.txt":       "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,0,0,20260101,20260329\n",
			"calendar_dates.txt": "",
		}, true, errScheduleEnded, true},
		{"an out-of-date feed_info while the calendar runs", map[string]string{"feed_info.txt": feedInfo("20260101", "20260329", "")}, false, nil, false},
		{"not started: trips are still planned", map[string]string{
			"calendar.txt":       "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,0,0,20260330,20261231\n",
			"calendar_dates.txt": "service_id,date,exception_type\nWK,20260327,1\n",
		}, false, nil, false},
		{"not started", map[string]string{
			"calendar.txt":       "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nWK,1,1,1,1,1,0,0,20260401,20261231\n",
			"calendar_dates.txt": "",
		}, false, errScheduleNotStarted, false},
		{"in range", nil, false, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTown(t, townWith(tt.files))
			if tt.realtime {
				s.setFeed()
			}
			_, err := s.provider(t, tt.realtime).Departures(t.Context(), transit.Query{StopID: "S", Now: monday8, Window: time.Hour})
			var partial *transit.PartialError
			switch {
			case tt.want == nil && err != nil:
				t.Fatalf("err = %v, want none", err)
			case tt.want != nil && !errors.Is(err, tt.want):
				t.Fatalf("err = %v, want it to wrap %v", err, tt.want)
			case tt.want != nil && errors.As(err, &partial) != tt.partial:
				t.Errorf("err = %v, partial = %t, want partial %t", err, !tt.partial, tt.partial)
			}
		})
	}
}

// countingTransport counts schedule requests and the 304s among them.
type countingTransport struct {
	next                http.RoundTripper
	requests, unchanged atomic.Int32
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if r.URL.Path == "/gtfs.zip" {
		c.requests.Add(1)
		if err == nil && resp.StatusCode == http.StatusNotModified {
			c.unchanged.Add(1)
		}
	}
	return resp, err
}

func countedProvider(t *testing.T, s *townServer, dir string) (*Provider, *countingTransport) {
	t.Helper()
	ct := &countingTransport{next: s.Client().Transport}
	p, err := New(Config{StaticURL: s.URL + "/gtfs.zip"}, &http.Client{Transport: ct}, WithCacheDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	return p, ct
}

func departAt(t *testing.T, p *Provider, now time.Time) {
	t.Helper()
	_, _ = p.Departures(t.Context(), transit.Query{StopID: "S", Now: now, Window: time.Hour})
	p.WaitForSchedule()
}

// A process starting after the schedule ended checks it with the server,
// although the file was checked less than a day ago, and then hourly.
func TestEndedScheduleIsRechecked(t *testing.T) {
	s := newTown(t, endingOn("20260329"))
	etag := `"v1"`
	s.etag.Store(&etag)
	dir := t.TempDir()
	first, _ := countedProvider(t, s, dir)
	departAt(t, first, at(2026, 3, 29, 20, 0))

	p, ct := countedProvider(t, s, dir)
	for _, step := range []struct {
		at       time.Duration
		requests int32
	}{
		{0, 1},
		{30 * time.Minute, 1},
		{61 * time.Minute, 2},
	} {
		departAt(t, p, monday8.Add(step.at))
		if n := ct.requests.Load(); n != step.requests {
			t.Errorf("at +%v: %d schedule requests, want %d", step.at, n, step.requests)
		}
	}
	if n := ct.unchanged.Load(); n != 2 {
		t.Errorf("%d answers were 304, want 2", n)
	}
}

// A server that sends an ETag but never answers 304 is not checked hourly
// once the schedule has ended: each check would download it in full.
func TestEndedScheduleWithoutConditionalRequests(t *testing.T) {
	s := newTown(t, endingOn("20260329"))
	etag := `"v1"`
	s.etag.Store(&etag)
	s.ignoreConditional.Store(true)
	p, ct := countedProvider(t, s, t.TempDir())
	departAt(t, p, monday8)
	departAt(t, p, monday8.Add(2*time.Hour))
	if n := ct.requests.Load(); n != 1 {
		t.Errorf("%d schedule requests in two hours, want 1", n)
	}
}

// An hourly check answered 304 keeps the schedule already in memory, with
// the stop times its timetable has read, rather than parsing it again.
func TestUnchangedScheduleKeepsItsTimetable(t *testing.T) {
	s := newTown(t, endingOn("20260329"))
	etag := `"v1"`
	s.etag.Store(&etag)
	dir := t.TempDir()
	departAt(t, s.provider(t, false, WithCacheDir(dir)), at(2026, 3, 29, 20, 0))

	p, ct := countedProvider(t, s, dir)
	departAt(t, p, monday8)
	before := p.currentStatic()
	departAt(t, p, monday8.Add(61*time.Minute))
	after := p.currentStatic()
	if n := ct.unchanged.Load(); n != 2 {
		t.Fatalf("%d answers were 304, want 2", n)
	}
	if after == before || after.tt != before.tt {
		t.Errorf("after a 304 the schedule is %p with timetable %p, want a copy of %p sharing timetable %p", after, after.tt, before, before.tt)
	}
}

// A schedule is checked again once its last day is over, not a day after
// it was loaded.
func TestScheduleIsRecheckedWhenItEnds(t *testing.T) {
	s := newTown(t, endingOn("20260330"))
	p, ct := countedProvider(t, s, t.TempDir())
	departAt(t, p, monday8)
	departAt(t, p, at(2026, 3, 30, 23, 0))
	if n := ct.requests.Load(); n != 1 {
		t.Fatalf("%d requests before the end, want 1", n)
	}
	departAt(t, p, at(2026, 3, 31, 0, 1))
	if n := ct.requests.Load(); n != 2 {
		t.Errorf("%d requests after the end, want 2", n)
	}
}

// writeCached writes a schedule into dir as the cache would, with one stop
// index, last used at used.
func writeCached(t *testing.T, dir, url string, files map[string]string, used time.Time) (gtfs, index string) {
	t.Helper()
	gtfs = filepath.Join(dir, cacheName(url))
	if err := os.WriteFile(gtfs, append([]byte("{}\n"), zipOf(t, files)...), 0o600); err != nil {
		t.Fatal(err)
	}
	index = filepath.Join(dir, cacheName(url)[:64]+".v1.abc.stops")
	if err := os.WriteFile(index, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{gtfs, index} {
		if err := os.Chtimes(p, used, used); err != nil {
			t.Fatal(err)
		}
	}
	return gtfs, index
}

func TestPruneEndedSchedules(t *testing.T) {
	now := at(2026, 6, 1, 8, 0)
	old, recent := now.Add(-8*24*time.Hour), now.Add(-24*time.Hour)
	ended := endingOn("20260430")
	unknown := townWith(map[string]string{"calendar.txt": "", "calendar_dates.txt": ""})
	tests := []struct {
		name  string
		files map[string]string
		used  time.Time
		keep  bool // the provider's own schedule
		gone  bool
	}{
		{"ended and unused", ended, old, false, true},
		{"ended, used yesterday", ended, recent, false, false},
		{"valid and unused", town, old, false, false},
		{"coverage unknown", unknown, old, false, false},
		{"the provider's own", ended, old, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			gtfs, index := writeCached(t, dir, "https://example.com/"+tt.name, tt.files, tt.used)
			keep := filepath.Join(dir, "other.gtfs")
			if tt.keep {
				keep = gtfs
			}
			pruneEndedSchedules(dir, keep, now)
			for _, p := range []string{gtfs, index} {
				_, err := os.Stat(p)
				if gone := errors.Is(err, os.ErrNotExist); gone != tt.gone {
					t.Errorf("%s: removed = %t, want %t", filepath.Base(p), gone, tt.gone)
				}
			}
		})
	}
}

// After a 304, failing to renew the file's time leaves the schedule valid.
func TestUnchangedScheduleWithoutRenewedTime(t *testing.T) {
	s := newTown(t, town)
	etag := `"v1"`
	s.etag.Store(&etag)
	dir := t.TempDir()
	departAt(t, s.provider(t, false, WithCacheDir(dir)), monday8)

	chtimes = func(string, time.Time, time.Time) error { return errors.New("read-only") }
	t.Cleanup(func() { chtimes = os.Chtimes })
	p := s.provider(t, false, WithCacheDir(dir))
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "S", Now: monday8.Add(25 * time.Hour), Window: time.Hour}); err != nil {
		t.Errorf("err = %v, want none", err)
	}
}

// A long-running process that started from a cached file counts the next
// check from the file's last check, not from when it started.
func TestDailyCheckCountsFromTheFile(t *testing.T) {
	s := newTown(t, town)
	etag := `"v1"`
	s.etag.Store(&etag)
	dir := t.TempDir()
	first, _ := countedProvider(t, s, dir)
	departAt(t, first, monday8)

	p, ct := countedProvider(t, s, dir)
	departAt(t, p, monday8.Add(23*time.Hour))
	if n := ct.requests.Load(); n != 0 {
		t.Fatalf("%d requests for a file checked 23 hours ago, want 0", n)
	}
	departAt(t, p, monday8.Add(24*time.Hour+time.Minute))
	if n := ct.requests.Load(); n != 1 {
		t.Errorf("%d requests a day after the file's check, want 1", n)
	}
}

// A failed check of a schedule that has ended keeps it, read once, as a
// stale result: from the file for a new process, or the copy in memory,
// with its timetable still open, for a running one.
func TestFailedCheckKeepsEndedSchedule(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "new process", true: "running"}[running], func(t *testing.T) {
			s := newTown(t, endingOn("20260329"))
			dir := t.TempDir()
			first, _ := countedProvider(t, s, dir)
			departAt(t, first, at(2026, 3, 29, 20, 0))
			p := first
			if !running {
				p, _ = countedProvider(t, s, dir)
			}
			before := p.currentStatic()

			s.scheduleDown.Store(true)
			departAt(t, p, monday8)
			after := p.currentStatic()
			if after == nil || after.tt == nil {
				t.Fatalf("schedule = %v, want the cached one with its timetable", after)
			}
			if running && after.tt != before.tt {
				t.Errorf("timetable = %p, want the running one, %p", after.tt, before.tt)
			}
			after.tt.mu.Lock()
			closed := after.tt.closed
			after.tt.mu.Unlock()
			if closed {
				t.Error("the kept schedule's timetable is closed")
			}
			_, err := p.Departures(t.Context(), transit.Query{StopID: "S", Now: monday8, Window: time.Hour})
			if !errors.Is(err, errScheduleEnded) {
				t.Errorf("err = %v, want it to wrap %v", err, errScheduleEnded)
			}
		})
	}
}

// A schedule still fresh in the cache is served without pruning, which
// reads other schedules' calendars; a check with the server prunes.
func TestPruneRunsWithTheCheck(t *testing.T) {
	s := newTown(t, town)
	dir := t.TempDir()
	departAt(t, s.provider(t, false, WithCacheDir(dir)), monday8)
	other, _ := writeCached(t, dir, "https://example.com/other", endingOn("20260101"), monday8.Add(-30*24*time.Hour))

	departAt(t, s.provider(t, false, WithCacheDir(dir)), monday8.Add(time.Hour))
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("after a load from the cache: %v, want the ended schedule kept", err)
	}
	departAt(t, s.provider(t, false, WithCacheDir(dir)), monday8.Add(25*time.Hour))
	if _, err := os.Stat(other); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after a check with the server: %v, want the ended schedule removed", err)
	}
}
