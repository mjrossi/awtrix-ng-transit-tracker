package tfl_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/tfl"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/transittest"
)

// recorded is just after testdata was fetched; see testdata/README.md.
var recorded = time.Date(2026, 11, 27, 8, 49, 10, 0, time.UTC)

const (
	oxfordCircus = "940GZZLUOXC"
	bankDLR      = "940GZZDLBNK"
	busStopE     = "490013766E"
)

// fakeAPI serves testdata by path, answering 404 with the API's error body
// for anything not recorded.
type fakeAPI struct {
	*httptest.Server

	mu       sync.Mutex
	keys     map[string]bool
	requests map[string]int
	// status, when set, is returned for every request, with body.
	status int
	body   string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{keys: map[string]bool{}, requests: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		f.mu.Lock()
		f.requests[path]++
		f.keys[r.URL.Query().Get("app_key")] = true
		status, body := f.status, f.body
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		name := strings.NewReplacer("/", "_", " ", "_").Replace(path) + ".json"
		b, err := os.ReadFile(filepath.Join("testdata", name))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err != nil {
			b, _ = os.ReadFile(filepath.Join("testdata", "not_found.json"))
			w.WriteHeader(http.StatusNotFound)
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(f.Close)
	return f
}

// respond makes every request answer with status and body.
func (f *fakeAPI) respond(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeAPI) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[path]
}

func newProvider(t *testing.T, f *fakeAPI, key string) *tfl.Provider {
	t.Helper()
	p, err := tfl.New(tfl.Config{AppKey: key, BaseURL: f.URL}, f.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContract(t *testing.T) {
	p := newProvider(t, newFakeAPI(t), "")
	transittest.Run(t, p, []transittest.Case{
		{Name: "tube station", Query: transit.Query{StopID: oxfordCircus, Now: recorded, Window: 30 * time.Minute}},
		{Name: "DLR terminal", Query: transit.Query{StopID: bankDLR, Now: recorded, Window: 30 * time.Minute}},
		{Name: "bus stop", Query: transit.Query{StopID: busStopE, Now: recorded, Window: 30 * time.Minute}},
		{Name: "rail station", Query: transit.Query{StopID: "910GHGHI", Now: time.Date(2026, 11, 27, 9, 20, 50, 0, time.UTC), Window: 30 * time.Minute}},
		{Name: "nothing in a zero window", Query: transit.Query{StopID: oxfordCircus, Now: recorded}, WantEmpty: true},
	})
}

type row struct{ Time, Line, Color, Headsign, Direction string }

func summarize(deps []transit.Departure) []row {
	var out []row
	for _, d := range deps {
		out = append(out, row{d.Time().UTC().Format("15:04:05"), d.Line.ShortName, d.Line.Color, d.Headsign, d.Direction})
	}
	return out
}

func TestDepartures(t *testing.T) {
	tests := []struct {
		name   string
		stop   string
		window time.Duration
		mode   transit.Mode
		want   []row
	}{
		{
			// A Bakerloo train due at 08:48:37, before Now, is left out.
			name: "tube", stop: oxfordCircus, window: 10 * time.Minute, mode: transit.ModeMetro,
			want: []row{
				{"08:50:05", "Bakerloo", "#B36305", "Queen's Park", "Northbound"},
				{"08:51:00", "Victoria", "#0098D4", "Walthamstow Central", "Northbound"},
				{"08:52:36", "Bakerloo", "#B36305", "Elephant & Castle", "Southbound"},
				{"08:53:30", "Victoria", "#0098D4", "Brixton", "Southbound"},
				{"08:55:30", "Victoria", "#0098D4", "Walthamstow Central", "Northbound"},
				{"08:55:35", "Bakerloo", "#B36305", "Queen's Park", "Northbound"},
				{"08:56:30", "Victoria", "#0098D4", "Walthamstow Central", "Northbound"},
				{"08:56:30", "Victoria", "#0098D4", "Brixton", "Southbound"},
				{"08:56:36", "Bakerloo", "#B36305", "Elephant & Castle", "Southbound"},
			},
		},
		{
			// Two trains end their trip at Bank and are left out. All five
			// predictions share one ID, so IDs cannot tell trains apart.
			name: "DLR terminal", stop: bankDLR, window: 30 * time.Minute, mode: transit.ModeMetro,
			want: []row{
				{"08:50:54", "DLR", "#00A4A7", "Lewisham", "outbound"},
				{"08:52:55", "DLR", "#00A4A7", "Woolwich Arsenal", "outbound"},
				{"08:54:55", "DLR", "#00A4A7", "Lewisham", "outbound"},
			},
		},
		{
			name: "bus", stop: busStopE, window: 8 * time.Minute, mode: transit.ModeBus,
			want: []row{
				{"08:53:23", "9", "", "Aldwych", "inbound"},
				{"08:54:38", "23", "", "Aldwych", "outbound"},
			},
		},
		{
			name: "lowercase stop ID", stop: strings.ToLower(busStopE), window: 8 * time.Minute, mode: transit.ModeBus,
			want: []row{
				{"08:53:23", "9", "", "Aldwych", "inbound"},
				{"08:54:38", "23", "", "Aldwych", "outbound"},
			},
		},
	}
	p := newProvider(t, newFakeAPI(t), "")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, err := p.Departures(t.Context(), transit.Query{StopID: tt.stop, Now: recorded, Window: tt.window})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, summarize(deps)); diff != "" {
				t.Errorf("departures mismatch (-want +got):\n%s", diff)
			}
			for _, d := range deps {
				if !d.Realtime() || !d.Scheduled.Equal(*d.Predicted) || d.Line.Mode != tt.mode || d.StopID != tt.stop {
					t.Errorf("departure %+v: want a %s prediction at %s with Scheduled equal to Predicted", d, tt.mode, tt.stop)
				}
			}
		})
	}
}

func TestUnknownStops(t *testing.T) {
	p := newProvider(t, newFakeAPI(t), "")
	for _, id := range []string{"ZZZNOPE", "490G000803"} {
		_, err := p.Departures(t.Context(), transit.Query{StopID: id, Now: recorded, Window: time.Hour})
		if !errors.Is(err, transit.ErrUnknownStop) {
			t.Errorf("Departures(%s) = %v, want ErrUnknownStop", id, err)
		}
	}
	for _, id := range []string{"490G000803", "490g000803"} {
		_, err := p.Departures(t.Context(), transit.Query{StopID: id, Now: recorded, Window: time.Hour})
		if err == nil || !strings.Contains(err.Error(), "group of bus stops") {
			t.Errorf("Departures(%s): a bus stop group's error should say what it is, got %v", id, err)
		}
	}
}

func TestRail(t *testing.T) {
	// Recorded at 09:20:46 UTC; see testdata/README.md. Windrush trains end
	// their trip at Highbury & Islington and are left out.
	now := time.Date(2026, 11, 27, 9, 20, 50, 0, time.UTC)
	deps, err := newProvider(t, newFakeAPI(t), "").Departures(t.Context(), transit.Query{StopID: "910GHGHI", Now: now, Window: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	want := []row{
		{"09:23:00", "Mildmay", "#0077AD", "Stratford (London)", "inbound"},
		{"09:23:00", "Mildmay", "#0077AD", "Richmond (London)", "outbound"},
		{"09:28:02", "Mildmay", "#0077AD", "Clapham Junction", "outbound"},
	}
	if diff := cmp.Diff(want, summarize(deps)); diff != "" {
		t.Errorf("departures mismatch (-want +got):\n%s", diff)
	}
	for _, d := range deps {
		if d.Line.Mode != transit.ModeRail {
			t.Errorf("%s is %s, want rail", d.Line.ShortName, d.Line.Mode)
		}
	}
}

func TestHeadsignsAndModes(t *testing.T) {
	f := newFakeAPI(t)
	f.respond(http.StatusOK, `[
		{"naptanId": "X", "lineId": "northern", "lineName": "Northern", "modeName": "tube",
		 "destinationName": "", "towards": "Morden via  Bank", "expectedArrival": "2026-11-27T08:50:00Z"},
		{"naptanId": "X", "lineId": "central", "lineName": "Central", "modeName": "tube",
		 "destinationName": "Check Front of Train", "expectedArrival": "2026-11-27T08:51:00Z"},
		{"naptanId": "X", "lineId": "london-cable-car", "lineName": "IFS Cloud Cable Car", "modeName": "cable-car",
		 "destinationName": "Royal Docks", "expectedArrival": "2026-11-27T08:52:00Z"}
	]`)
	deps, err := newProvider(t, f, "").Departures(t.Context(), transit.Query{StopID: "X", Now: recorded, Window: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	type got struct {
		Headsign string
		Mode     transit.Mode
	}
	var gots []got
	for _, d := range deps {
		gots = append(gots, got{d.Headsign, d.Line.Mode})
	}
	want := []got{
		// With no destination, towards stands in for it.
		{"Morden via Bank", transit.ModeMetro},
		// A placeholder for an unknown destination leaves the headsign empty.
		{"", transit.ModeMetro},
		{"Royal Docks", transit.ModeOther},
	}
	if diff := cmp.Diff(want, gots); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestDirection(t *testing.T) {
	tests := []struct {
		platform, direction, want string
	}{
		{"Westbound - Platform 2", "inbound", "Westbound"},
		{"Northbound - Platform 4", "outbound", "Northbound"},
		// A bus stop's letter, or a platform number, says nothing of the
		// direction.
		{"E", "inbound", "inbound"},
		{"Platform 7", "Outbound", "outbound"},
		{"Platform Unknown", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		f := newFakeAPI(t)
		f.respond(http.StatusOK, fmt.Sprintf(`[{"naptanId": "X", "lineId": "piccadilly", "lineName": "Piccadilly", "modeName": "tube",
			"destinationName": "Uxbridge", "platformName": %q, "direction": %q, "expectedArrival": "2026-11-27T08:50:00Z"}]`, tt.platform, tt.direction))
		deps, err := newProvider(t, f, "").Departures(t.Context(), transit.Query{StopID: "X", Now: recorded, Window: 10 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if len(deps) != 1 || deps[0].Direction != tt.want {
			t.Errorf("platform %q, direction %q: got %+v, want direction %q", tt.platform, tt.direction, deps, tt.want)
		}
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name, key string
		status    int
		body      string
		want      string
		wantNot   string
	}{
		{name: "malformed JSON", status: http.StatusOK, body: `[{`, want: "decode StopPoint/X/Arrivals"},
		{name: "refused without a key", status: http.StatusForbidden, want: "403", wantNot: "app_key"},
		{name: "refused with a key", key: "k", status: http.StatusForbidden, want: "check app_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.respond(tt.status, tt.body)
			_, err := newProvider(t, f, tt.key).Departures(t.Context(), transit.Query{StopID: "X", Now: recorded, Window: time.Hour})
			if err == nil || !strings.Contains(err.Error(), tt.want) || (tt.wantNot != "" && strings.Contains(err.Error(), tt.wantNot)) {
				t.Errorf("error %v: want it to contain %q and not %q", err, tt.want, tt.wantNot)
			}
		})
	}
}

func TestSearchStops(t *testing.T) {
	f := newFakeAPI(t)
	p := newProvider(t, f, "")
	got, err := p.SearchStops(t.Context(), "trafalgar square")
	if err != nil {
		t.Fatal(err)
	}
	// Groups are listed as their stops, each named with its letter and
	// direction; a stop with no direction keeps only its letter.
	want := []transit.Stop{
		{ID: "490013766E", Name: "Charing Cross Stn / Trafalgar Square (Stop E, towards Aldwych)"},
		{ID: "490013766F", Name: "Charing Cross Stn / Trafalgar Square (Stop F, towards Euston Or St Paul's)"},
		{ID: "490013767W1", Name: "Northumberland Avenue / Trafalgar Square (->W)"},
		{ID: "490013767W", Name: "Northumberland Avenue / Trafalgar Square (Stop W, towards Aldgate And Limehouse)"},
		{ID: "490013767X", Name: "Northumberland Avenue / Trafalgar Square (Stop X, towards Aldwych)"},
		{ID: "490013767A", Name: "Trafalgar Sq / Charing Cross Stn (Stop A, towards Lambeth Or Vauxhall)"},
		{ID: "490013767C", Name: "Trafalgar Sq / Charing Cross Stn (Stop C, towards Tottenham Court Road)"},
		{ID: "490013767B", Name: "Trafalgar Square (Stop B, towards Aldwych)"},
		{ID: "490013767S", Name: "Trafalgar Square (Stop S, towards Hyde Park Corner)"},
		{ID: "490013767T", Name: "Trafalgar Square (Stop T, towards Oxford Circus)"},
		{ID: "490013767Z", Name: "Trafalgar Square (Stop Z)"},
		{ID: "490013767D", Name: "Trafalgar Square / Charing Cross Stn (Stop D, towards Westminster)"},
		{ID: "490013766G", Name: "Trafalgar Square / Charing Cross Stn (Stop G, towards Aldwych)"},
		{ID: "490013766K", Name: "Trafalgar Square / Charing Cross Stn (Stop K, towards Westminster)"},
		{ID: "490014585M", Name: "Whitehall / Trafalgar Square (Stop M, towards Brixton)"},
		{ID: "490014585N", Name: "Whitehall / Trafalgar Square (Stop N, towards Victoria Or Vauxhall)"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("search mismatch (-want +got):\n%s", diff)
	}
	if n := f.count("StopPoint/490013766E,490013766F,490G000803,490G000804,490G000832"); n != 1 {
		t.Errorf("bus results looked up in %d batched requests, want 1", n)
	}
}

func TestSearchByID(t *testing.T) {
	p := newProvider(t, newFakeAPI(t), "")
	tests := []struct {
		search string
		want   []transit.Stop
	}{
		{oxfordCircus, []transit.Stop{{ID: oxfordCircus, Name: "Oxford Circus Underground Station"}}},
		{strings.ToLower(busStopE), []transit.Stop{{ID: busStopE, Name: "Charing Cross Stn / Trafalgar Square (Stop E, towards Aldwych)"}}},
		// A pair of stops on either side of a road is a group too.
		{"490G00003044", []transit.Stop{
			{ID: "490003044B", Name: "Homerton High Street (Stop B, towards Mile End)"},
			{ID: "490003044H", Name: "Homerton High Street (Stop H, towards Homerton Hospital)"},
		}},
		// A bus stop's SMS code is no stop ID, so the text search finds it.
		{"50435", []transit.Stop{{ID: busStopE, Name: "Charing Cross Stn / Trafalgar Square (Stop E, towards Aldwych)"}}},
		{"940GZZZZZZZ", nil},
		{"", nil},
	}
	for _, tt := range tests {
		got, err := p.SearchStops(t.Context(), tt.search)
		if err != nil {
			t.Fatalf("SearchStops(%q): %v", tt.search, err)
		}
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("SearchStops(%q) mismatch (-want +got):\n%s", tt.search, diff)
		}
	}
}

func TestStopName(t *testing.T) {
	f := newFakeAPI(t)
	p := newProvider(t, f, "")
	tests := map[string]string{
		oxfordCircus: "Oxford Circus Underground Station",
		busStopE:     "Charing Cross Stn / Trafalgar Square (Stop E, towards Aldwych)",
	}
	for id, want := range tests {
		for range 2 {
			got, ok := p.StopName(t.Context(), id, recorded)
			if !ok || got != want {
				t.Errorf("StopName(%s) = %q, %v; want %q", id, got, ok, want)
			}
		}
		if n := f.count("StopPoint/" + id); n != 1 {
			t.Errorf("StopName(%s) fetched %d times, want 1", id, n)
		}
	}
	if _, ok := p.StopName(t.Context(), "ZZZNOPE", recorded); ok {
		t.Error("StopName of an unknown stop reported ok")
	}
}

func TestAppKey(t *testing.T) {
	f := newFakeAPI(t)
	p := newProvider(t, f, "s3cret")
	if _, err := p.Departures(t.Context(), transit.Query{StopID: oxfordCircus, Now: recorded, Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !f.keys["s3cret"] {
		t.Error("app_key was not sent")
	}

	f.respond(http.StatusInternalServerError, "")
	_, err := p.SearchStops(t.Context(), "bank")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error %v: want a failure that does not reveal the key", err)
	}

	// A refused connection's error carries the request URL.
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	p, err = tfl.New(tfl.Config{AppKey: "s3cret", BaseURL: closed.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Departures(t.Context(), transit.Query{StopID: oxfordCircus, Now: recorded, Window: time.Hour})
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error %v: want a failure that does not reveal the key", err)
	}
}

func TestInvalidKey(t *testing.T) {
	f := newFakeAPI(t)
	// As the API answers a wrong key.
	f.respond(http.StatusTooManyRequests, "Invalid app_key is provided.")
	_, err := newProvider(t, f, "wrong").Departures(t.Context(), transit.Query{StopID: oxfordCircus, Now: recorded, Window: time.Hour})
	if err == nil || !strings.Contains(err.Error(), "app_key is not valid") {
		t.Errorf("error %v: want app_key reported as not valid", err)
	}
}

func TestRateLimitSuggestsKey(t *testing.T) {
	f := newFakeAPI(t)
	f.respond(http.StatusTooManyRequests, "")
	_, err := newProvider(t, f, "").Departures(t.Context(), transit.Query{StopID: oxfordCircus, Now: recorded, Window: time.Hour})
	if err == nil || !strings.Contains(err.Error(), "app_key") {
		t.Errorf("error %v: want a suggestion to set app_key", err)
	}
}

func TestSharedArrivals(t *testing.T) {
	f := newFakeAPI(t)
	p := newProvider(t, f, "")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := p.Departures(t.Context(), transit.Query{StopID: oxfordCircus, Now: recorded, Window: time.Hour}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := f.count("StopPoint/" + oxfordCircus + "/Arrivals"); n != 1 {
		t.Errorf("boards sharing a stop made %d requests, want 1", n)
	}
	if _, err := p.Departures(t.Context(), transit.Query{StopID: oxfordCircus, Now: recorded.Add(31 * time.Second), Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if n := f.count("StopPoint/" + oxfordCircus + "/Arrivals"); n != 2 {
		t.Errorf("after 31 s, %d requests, want 2", n)
	}
}
