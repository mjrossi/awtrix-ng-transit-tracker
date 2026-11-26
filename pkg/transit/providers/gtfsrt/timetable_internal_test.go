package gtfsrt

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/transittest"
)

// town is a small hand-written schedule in Lisbon's time zone, which
// changes to summer time on 2026-03-29.
var town = map[string]string{
	"agency.txt": `agency_id,agency_name,agency_url,agency_timezone
A,Town Transit,https://example.com,Europe/Lisbon
`,
	"routes.txt": `route_id,route_short_name,route_long_name,route_type,route_color
R1,1,,3,FF0000
R2,2,,3,00FF00
`,
	"stops.txt": `stop_id,stop_name,parent_station
S,Central,
S1,Central A,S
S2,Central B,S
T,Market,
U,Harbor,
`,
	"trips.txt": `route_id,service_id,trip_id,trip_headsign
R1,WK,t1,Harbor
R1,WK,t2,Harbor
R1,WK,late,Harbor
R2,WK,nohead,
R1,SUN,sun1,Harbor
R2,WK,freq,Market
R1,WK,ends,Central
R1,WK,nopick,Harbor
`,
	"calendar.txt": `service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date
WK,1,1,1,1,1,0,0,20260101,20261231
SUN,0,0,0,0,0,0,1,20260101,20261231
`,
	"calendar_dates.txt": `service_id,date,exception_type
WK,20260401,2
SUN,20260401,1
`,
	"frequencies.txt": `trip_id,start_time,end_time,headway_secs
freq,10:00:00,11:00:00,1200
`,
	"stop_times.txt": `trip_id,arrival_time,departure_time,stop_id,stop_sequence,pickup_type
t1,08:00:00,08:00:00,T,1,
t1,08:10:00,08:10:00,S1,2,
t1,08:20:00,08:20:00,U,3,
t2,08:30:00,08:30:00,T,1,
t2,08:40:00,08:40:00,S2,2,
t2,08:50:00,08:50:00,U,3,
late,25:00:00,25:00:00,T,1,
late,25:10:00,25:10:00,S1,2,
late,25:20:00,25:20:00,U,3,
nohead,09:15:00,09:15:00,U,2,
nohead,09:00:00,09:00:00,S2,1,
sun1,08:10:00,08:10:00,S1,1,
sun1,08:20:00,08:20:00,U,2,
freq,00:00:00,00:00:00,S1,1,
freq,00:05:00,00:05:00,T,2,
ends,08:00:00,08:00:00,T,1,
ends,08:12:00,08:12:00,S1,2,
nopick,08:14:00,08:14:00,S1,1,1
nopick,08:30:00,08:30:00,U,2,
`,
}

var lisbon = mustLoad("Europe/Lisbon")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func at(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, lisbon)
}

// Monday, the day after the change to summer time.
var monday8 = at(2026, 3, 30, 8, 0)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type townServer struct {
	*httptest.Server
	schedule atomic.Pointer[[]byte]
	etag     atomic.Pointer[string]
	// realtime is the feed served, or nil to fail.
	realtime atomic.Pointer[[]byte]
}

func newTown(t *testing.T, files map[string]string) *townServer {
	t.Helper()
	s := &townServer{}
	b := zipOf(t, files)
	s.schedule.Store(&b)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gtfs.zip", func(w http.ResponseWriter, r *http.Request) {
		if e := s.etag.Load(); e != nil {
			w.Header().Set("ETag", *e)
			if r.Header.Get("If-None-Match") == *e {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		_, _ = w.Write(*s.schedule.Load())
	})
	mux.HandleFunc("GET /rt.pb", func(w http.ResponseWriter, _ *http.Request) {
		rt := s.realtime.Load()
		if rt == nil {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(*rt)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *townServer) setFeed(entities ...[]byte) {
	msg := bytesField(1, varint(3, uint64(monday8.Unix())))
	for _, e := range entities {
		msg = append(msg, e...)
	}
	s.realtime.Store(&msg)
}

func (s *townServer) provider(t *testing.T, realtime bool, opts ...Option) *Provider {
	t.Helper()
	cfg := Config{StaticURL: s.URL + "/gtfs.zip"}
	if realtime {
		cfg.RealtimeURLs = []string{s.URL + "/rt.pb"}
	}
	p, err := New(cfg, s.Client(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// tripEntity builds a FeedEntity with one TripUpdate.
func tripEntity(trip, startDate, route string, relation int, stops ...[]byte) []byte {
	desc := [][]byte{bytesField(1, []byte(trip))}
	if startDate != "" {
		desc = append(desc, bytesField(3, []byte(startDate)))
	}
	if relation != 0 {
		desc = append(desc, varint(4, uint64(relation)))
	}
	if route != "" {
		desc = append(desc, bytesField(5, []byte(route)))
	}
	parts := append([][]byte{bytesField(1, desc...)}, stops...)
	return bytesField(2, bytesField(1, []byte(trip)), bytesField(3, parts...))
}

// stopUpdate builds a StopTimeUpdate. A zero seq or stop is left out; ev
// is the departure event's fields.
func stopUpdate(seq int, stop string, relation int, ev ...[]byte) []byte {
	var parts [][]byte
	if seq != 0 {
		parts = append(parts, varint(1, uint64(seq)))
	}
	if len(ev) > 0 {
		parts = append(parts, bytesField(3, ev...))
	}
	if stop != "" {
		parts = append(parts, bytesField(4, []byte(stop)))
	}
	if relation != 0 {
		parts = append(parts, varint(5, uint64(relation)))
	}
	return bytesField(2, parts...)
}

func delayOf(secs int) []byte   { return varint(1, uint64(int64(secs))) }
func timeOf(t time.Time) []byte { return varint(2, uint64(t.Unix())) }

type plannedRow struct {
	Time, Scheduled, Line, Headsign string
	Realtime                        bool
	Status                          transit.Status
}

func rows(deps []transit.Departure) []plannedRow {
	var out []plannedRow
	for _, d := range deps {
		out = append(out, plannedRow{
			Time: d.Time().In(lisbon).Format("Mon 15:04"), Scheduled: d.Scheduled.In(lisbon).Format("15:04"),
			Line: d.Line.ShortName, Headsign: d.Headsign, Realtime: d.Realtime(), Status: d.Status,
		})
	}
	return out
}

func TestTimetableOnly(t *testing.T) {
	tests := map[string]struct {
		stop   string
		now    time.Time
		window time.Duration
		want   []plannedRow
	}{
		"station, with a headsign from the last stop, without arrivals or no-pickup stops": {
			stop: "S", now: monday8, window: time.Hour,
			want: []plannedRow{
				{"Mon 08:10", "08:10", "1", "Harbor", false, "scheduled"},
				{"Mon 08:40", "08:40", "1", "Harbor", false, "scheduled"},
				{"Mon 09:00", "09:00", "2", "Harbor", false, "scheduled"},
			},
		},
		"platform": {
			stop: "S1", now: monday8, window: time.Hour,
			want: []plannedRow{{"Mon 08:10", "08:10", "1", "Harbor", false, "scheduled"}},
		},
		"a time past 24:00 runs after midnight": {
			stop: "S", now: at(2026, 3, 31, 1, 0), window: 30 * time.Minute,
			want: []plannedRow{{"Tue 01:10", "01:10", "1", "Harbor", false, "scheduled"}},
		},
		"calendar_dates removes and adds a day": {
			stop: "S", now: at(2026, 4, 1, 8, 0), window: 30 * time.Minute,
			want: []plannedRow{{"Wed 08:10", "08:10", "1", "Harbor", false, "scheduled"}},
		},
		"on the day the clocks change": {
			stop: "S1", now: at(2026, 3, 29, 8, 0), window: 30 * time.Minute,
			want: []plannedRow{{"Sun 08:10", "08:10", "1", "Harbor", false, "scheduled"}},
		},
		"frequencies": {
			stop: "S", now: at(2026, 3, 30, 10, 0), window: 50 * time.Minute,
			want: []plannedRow{
				{"Mon 10:00", "10:00", "2", "Market", false, "scheduled"},
				{"Mon 10:20", "10:20", "2", "Market", false, "scheduled"},
				{"Mon 10:40", "10:40", "2", "Market", false, "scheduled"},
			},
		},
	}
	p := newTown(t, town).provider(t, false)
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			deps, err := p.Departures(t.Context(), transit.Query{StopID: tt.stop, Now: tt.now, Window: tt.window})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, rows(deps)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTimetableContract(t *testing.T) {
	transittest.Run(t, newTown(t, town).provider(t, false), []transittest.Case{
		{Name: "station", Query: transit.Query{StopID: "S", Now: monday8, Window: time.Hour}},
		{Name: "night", Query: transit.Query{StopID: "S", Now: at(2026, 3, 30, 3, 0), Window: time.Hour}, WantEmpty: true},
	})
}

func TestTimetableWithRealtime(t *testing.T) {
	s := newTown(t, town)
	s.setFeed(
		// A delay without a time, at the stop.
		tripEntity("t1", "20260330", "", 0, stopUpdate(2, "S1", 0, delayOf(120))),
		// A delay at an earlier stop carries forward.
		tripEntity("t2", "20260330", "", 0, stopUpdate(1, "T", 0, delayOf(300))),
		tripEntity("nohead", "20260330", "", tripCanceled),
		// A trip the schedule does not have.
		tripEntity("x9", "", "R2", tripNew, stopUpdate(0, "S1", 0, timeOf(at(2026, 3, 30, 8, 30))), stopUpdate(0, "U", 0, timeOf(at(2026, 3, 30, 8, 45)))),
		// Another day's update leaves today's trip as scheduled.
		tripEntity("late", "20260329", "", 0, stopUpdate(2, "S1", 0, delayOf(60))),
	)
	deps, err := s.provider(t, true).Departures(t.Context(), transit.Query{StopID: "S", Now: monday8, Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	want := []plannedRow{
		{"Mon 08:12", "08:10", "1", "Harbor", true, "scheduled"},
		{"Mon 08:30", "08:30", "2", "Harbor", true, "added"},
		{"Mon 08:45", "08:40", "1", "Harbor", true, "scheduled"},
		{"Mon 09:00", "09:00", "2", "Harbor", false, "canceled"},
	}
	if diff := cmp.Diff(want, rows(deps)); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}

	s.setFeed(tripEntity("t2", "20260330", "", 0, stopUpdate(2, "S2", stopSkipped)))
	deps, err = s.provider(t, true).Departures(t.Context(), transit.Query{StopID: "S2", Now: monday8, Window: 30 * time.Minute})
	if err != nil || len(deps) != 0 {
		t.Errorf("got %v, err %v; want the skipped stop left out", rows(deps), err)
	}
}

func TestTimetableOff(t *testing.T) {
	s := newTown(t, town)
	s.setFeed(tripEntity("t1", "20260330", "", 0, stopUpdate(2, "S1", 0, timeOf(at(2026, 3, 30, 8, 11)))))
	off := false
	p, err := New(Config{StaticURL: s.URL + "/gtfs.zip", RealtimeURLs: []string{s.URL + "/rt.pb"}, Timetable: &off}, s.Client())
	if err != nil {
		t.Fatal(err)
	}
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "S", Now: monday8, Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]plannedRow{{"Mon 08:11", "08:11", "1", "Harbor", true, "scheduled"}}, rows(deps)); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// A feed whose trip IDs are not the schedule's, as New York's, would show
// each trip twice, so its routes show realtime trips only.
func TestUnmatchedTripIDs(t *testing.T) {
	s := newTown(t, town)
	var entities [][]byte
	for i := range 4 {
		trip := "rt-" + string(rune('a'+i))
		entities = append(entities, tripEntity(trip, "", "R1", 0, stopUpdate(0, "S1", 0, timeOf(at(2026, 3, 30, 8, 20+i))), stopUpdate(0, "U", 0, timeOf(at(2026, 3, 30, 9, 0)))))
	}
	s.setFeed(entities...)
	var logged bytes.Buffer
	p := s.provider(t, true, WithLogger(slog.New(slog.NewTextHandler(&logged, nil))))
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "S", Now: monday8, Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps {
		if d.Line.ID == "R1" && !d.Realtime() {
			t.Errorf("timetabled trip on route R1 at %s shown beside its realtime trips", d.Time().In(lisbon).Format("15:04"))
		}
	}
	if !hasLine(deps, "R2") {
		t.Error("route R2, not in the feed, lost its timetabled trips")
	}
	if !strings.Contains(logged.String(), "do not match the schedule") {
		t.Errorf("not logged:\n%s", logged.String())
	}
}

func hasLine(deps []transit.Departure, id string) bool {
	for _, d := range deps {
		if d.Line.ID == id {
			return true
		}
	}
	return false
}

func TestTimetableStandsInForAFailedFeed(t *testing.T) {
	s := newTown(t, town)
	deps, err := s.provider(t, true).Departures(t.Context(), transit.Query{StopID: "S", Now: monday8, Window: time.Hour})
	var partial *transit.PartialError
	if !errors.As(err, &partial) || len(deps) != 3 {
		t.Errorf("got %d departures, err %v; want the timetable's 3 with a partial error", len(deps), err)
	}
}

func TestTimetableUnknownStop(t *testing.T) {
	_, err := newTown(t, town).provider(t, false).Departures(t.Context(), transit.Query{StopID: "NOPE", Now: monday8, Window: time.Hour})
	if !errors.Is(err, transit.ErrUnknownStop) {
		t.Errorf("err = %v, want unknown stop", err)
	}
}

func TestNewNeedsAFeed(t *testing.T) {
	if _, err := New(Config{}, nil); err == nil {
		t.Error("New accepted a configuration without feeds")
	}
	off := false
	if _, err := New(Config{StaticURL: "https://example.com/gtfs.zip", Timetable: &off}, nil); err == nil {
		t.Error("New accepted timetable = false without realtime_urls")
	}
	if _, err := New(Config{StaticURL: "https://example.com/gtfs.zip"}, nil); err != nil {
		t.Errorf("schedule alone: %v", err)
	}
}

// indexLogs returns how many times providers sharing dir, each asked once
// at the same time, scanned stop_times.txt.
func scans(t *testing.T, s *townServer, dir string, n int) int {
	t.Helper()
	var logged bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logged, nil))
	for range n {
		p := s.provider(t, false, WithCacheDir(dir), WithLogger(log))
		if _, err := p.Departures(t.Context(), transit.Query{StopID: "S", Now: monday8, Window: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for line := range strings.Lines(logged.String()) {
		var rec struct{ Msg string }
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Msg == "indexed GTFS stop times" {
			count++
		}
	}
	return count
}

func TestStopIndexIsSaved(t *testing.T) {
	dir := t.TempDir()
	s := newTown(t, town)
	if got := scans(t, s, dir, 2); got != 1 {
		t.Errorf("%d scans for two processes, want 1: the second reads the saved index", got)
	}
	old, _ := filepath.Glob(filepath.Join(dir, "*.stops"))
	if len(old) != 1 {
		t.Fatalf("saved indexes: %v, want one", old)
	}

	// A new schedule makes the saved index useless; it is replaced.
	changed := map[string]string{}
	for k, v := range town {
		changed[k] = v
	}
	changed["stop_times.txt"] = strings.Replace(town["stop_times.txt"], "08:40:00,08:40:00", "08:41:00,08:41:00", 1)
	b := zipOf(t, changed)
	s.schedule.Store(&b)
	for _, p := range mustGlob(t, filepath.Join(dir, "*.gtfs")) {
		expired := monday8.Add(-2 * staticTTL)
		if err := os.Chtimes(p, expired, expired); err != nil {
			t.Fatal(err)
		}
	}
	p := s.provider(t, false, WithCacheDir(dir))
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "S2", Now: monday8, Window: time.Hour})
	if err != nil || len(deps) == 0 || deps[0].Time().In(lisbon).Format("15:04") != "08:41" {
		t.Errorf("got %v, err %v; want the new schedule's 08:41", rows(deps), err)
	}
	now, _ := filepath.Glob(filepath.Join(dir, "*.stops"))
	for _, o := range old {
		for _, n := range now {
			if o == n {
				t.Errorf("index for the old schedule kept: %s", o)
			}
		}
	}

	if _, _, err := ClearCache(dir, monday8); err != nil {
		t.Fatal(err)
	}
	if left := mustGlob(t, filepath.Join(dir, "*.stops")); len(left) != 0 {
		t.Errorf("ClearCache left indexes: %v", left)
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Boards starting together share one pass over stop_times.txt.
func TestConcurrentStopsShareAScan(t *testing.T) {
	s := newTown(t, town)
	var logged bytes.Buffer
	p := s.provider(t, false, WithLogger(slog.New(slog.NewJSONHandler(&logged, nil))))
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "U", Now: monday8, Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	logged.Reset()
	done := make(chan error)
	for _, stop := range []string{"S", "S1", "S2", "T"} {
		go func() {
			_, err := p.Departures(t.Context(), transit.Query{StopID: stop, Now: monday8, Window: time.Hour})
			done <- err
		}()
	}
	for range 4 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(logged.String(), "indexed GTFS stop times"); n != 1 {
		t.Errorf("%d scans for four stops asked at once, want 1", n)
	}
}

// HSL leaves trip IDs out and names each trip by route, direction, start
// time and day. The town's trips have no direction, so one the feed gives
// still matches.
func TestUpdatesWithoutTripIDs(t *testing.T) {
	byStart := func(route, start string, stops ...[]byte) []byte {
		desc := bytesField(1, bytesField(2, []byte(start)), bytesField(3, []byte("20260330")), bytesField(5, []byte(route)), varint(6, 1))
		return bytesField(2, bytesField(1, []byte(route+start)), bytesField(3, append([][]byte{desc}, stops...)...))
	}
	s := newTown(t, town)
	s.setFeed(
		byStart("R1", "08:00:00", stopUpdate(0, "S1", 0, timeOf(at(2026, 3, 30, 8, 13)))),
		// One run of the frequency trip.
		byStart("R2", "10:20:00", stopUpdate(0, "S1", 0, delayOf(90))),
	)
	p := s.provider(t, true)
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "S1", Now: monday8, Window: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]plannedRow{{"Mon 08:13", "08:10", "1", "Harbor", true, "scheduled"}}, rows(deps)); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
	deps, err = p.Departures(t.Context(), transit.Query{StopID: "S1", Now: at(2026, 3, 30, 10, 0), Window: 50 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	want := []plannedRow{
		{"Mon 10:00", "10:00", "2", "Market", false, "scheduled"},
		{"Mon 10:21", "10:20", "2", "Market", true, "scheduled"},
		{"Mon 10:40", "10:40", "2", "Market", false, "scheduled"},
	}
	if diff := cmp.Diff(want, rows(deps)); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

// Every run of a frequency trip has its trip ID, so an update names its run
// by start time, and one without a start time names none.
func TestFrequencyRunByTripID(t *testing.T) {
	run := func(start string, stops ...[]byte) []byte {
		desc := [][]byte{bytesField(1, []byte("freq")), bytesField(3, []byte("20260330"))}
		if start != "" {
			desc = append(desc, bytesField(2, []byte(start)))
		}
		return bytesField(2, bytesField(1, []byte("freq"+start)), bytesField(3, append([][]byte{bytesField(1, desc...)}, stops...)...))
	}
	tests := map[string]struct {
		update []byte
		want   []plannedRow
	}{
		"with a start time": {
			update: run("10:20:00", stopUpdate(1, "S1", 0, timeOf(at(2026, 3, 30, 10, 21)))),
			want: []plannedRow{
				{"Mon 10:00", "10:00", "2", "Market", false, "scheduled"},
				{"Mon 10:21", "10:20", "2", "Market", true, "scheduled"},
				{"Mon 10:40", "10:40", "2", "Market", false, "scheduled"},
			},
		},
		"without one": {
			update: run("", stopUpdate(1, "S1", 0, delayOf(60))),
			want: []plannedRow{
				{"Mon 10:00", "10:00", "2", "Market", false, "scheduled"},
				{"Mon 10:20", "10:20", "2", "Market", false, "scheduled"},
				{"Mon 10:40", "10:40", "2", "Market", false, "scheduled"},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := newTown(t, town)
			s.setFeed(tt.update)
			deps, err := s.provider(t, true).Departures(t.Context(), transit.Query{StopID: "S1", Now: at(2026, 3, 30, 10, 0), Window: 50 * time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, rows(deps)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// One realtime trip the schedule lacks, beside matched ones, is a trip
// added late, not a mismatch, so the line keeps its timetable.
func TestOneUnmatchedTripKeepsTheTimetable(t *testing.T) {
	s := newTown(t, town)
	s.setFeed(
		tripEntity("t1", "20260330", "", 0, stopUpdate(2, "S1", 0, delayOf(60))),
		tripEntity("t2", "20260330", "", 0, stopUpdate(2, "S2", 0, delayOf(60))),
		tripEntity("rt-x", "", "R1", 0, stopUpdate(0, "S1", 0, timeOf(at(2026, 3, 30, 8, 25))), stopUpdate(0, "U", 0, timeOf(at(2026, 3, 30, 9, 0)))),
	)
	var logged bytes.Buffer
	p := s.provider(t, true, WithLogger(slog.New(slog.NewTextHandler(&logged, nil))))
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "S", Now: monday8, Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !hasLine(deps, "R2") || len(deps) != 4 {
		t.Errorf("got %v; want both matched trips, the unmatched one and route 2's", rows(deps))
	}
	if strings.Contains(logged.String(), "do not match the schedule") {
		t.Errorf("mismatch logged for one unmatched trip:\n%s", logged.String())
	}
}

// Without realtime feeds, a schedule that cannot give a timetable is an
// error, not an empty board.
func TestScheduleOnlyWithoutATimetable(t *testing.T) {
	noCalendar := map[string]string{}
	for k, v := range town {
		if k != "calendar.txt" && k != "calendar_dates.txt" {
			noCalendar[k] = v
		}
	}
	deps, err := newTown(t, noCalendar).provider(t, false).Departures(t.Context(), transit.Query{StopID: "S", Now: monday8, Window: time.Hour})
	var partial *transit.PartialError
	if err == nil || errors.As(err, &partial) || !errors.Is(err, errNoTimetable) {
		t.Errorf("got %d departures, err %v; want errNoTimetable, not partial", len(deps), err)
	}
}

// A query that gives up while waiting for a shared scan does not stop it
// for the others.
func TestCanceledWaiterLeavesTheScan(t *testing.T) {
	s := newTown(t, town)
	p := s.provider(t, false)
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "U", Now: monday8, Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	canceled := make(chan error)
	go func() {
		_, err := p.Departures(ctx, transit.Query{StopID: "S1", Now: monday8, Window: time.Hour})
		canceled <- err
	}()
	cancel()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Errorf("canceled query: err = %v", err)
	}
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "S1", Now: monday8, Window: time.Hour})
	if err != nil || len(deps) != 1 {
		t.Errorf("got %v, err %v; want S1's departure", rows(deps), err)
	}
}

func TestStopLines(t *testing.T) {
	p := newTown(t, town).provider(t, false)
	var liner transit.StopLiner = p
	got, err := liner.StopLines(t.Context(), []string{"S1", "T", "U"}, monday8)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string][]string{}
	for id, lines := range got {
		for _, l := range lines {
			names[id] = append(names[id], l.ShortName)
		}
	}
	// Harbor (U) only ends trips, so no line departs from it.
	want := map[string][]string{"S1": {"1", "2"}, "T": {"1"}}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("lines (-want +got):\n%s", diff)
	}
}

// timetable = false hides timetabled trips, but the timetable still names
// a stop's lines, as for New York's stations with a stop per group of lines.
func TestStopLinesWithTimetableOff(t *testing.T) {
	s := newTown(t, town)
	off := false
	cfg := Config{StaticURL: s.URL + "/gtfs.zip", RealtimeURLs: []string{s.URL + "/rt.pb"}, Timetable: &off}

	t.Run("cached", func(t *testing.T) {
		p, err := New(cfg, s.Client(), WithCacheDir(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.StopLines(t.Context(), []string{"S1"}, monday8)
		if err != nil || len(got["S1"]) != 2 {
			t.Errorf("StopLines = %v, %v; want lines 1 and 2", got, err)
		}
	})
	t.Run("uncached", func(t *testing.T) {
		p, err := New(cfg, s.Client())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.StopLines(t.Context(), []string{"S1"}, monday8); err == nil || !strings.Contains(err.Error(), "schedule cache") {
			t.Errorf("StopLines error = %v, want one naming the schedule cache", err)
		}
		if st := p.currentStatic(); st == nil || st.tt != nil {
			t.Error("the temporary schedule file is kept with timetable = false")
		}
	})
}
