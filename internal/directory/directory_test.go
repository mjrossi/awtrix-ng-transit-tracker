package directory_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/internal/directory"
)

// server serves testdata: feeds_v2.csv, a trimmed copy of the Mobility
// Database's list, and Transitland responses recorded on 2026-11-26.
type server struct {
	*httptest.Server
	csvCalls    atomic.Int32
	notModified atomic.Int32
	csvDown     atomic.Bool
	tlCalls     atomic.Int32
}

var listModified = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{}
	csv, err := os.ReadFile("testdata/feeds_v2.csv")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /feeds_v2.csv", func(w http.ResponseWriter, r *http.Request) {
		s.csvCalls.Add(1)
		if s.csvDown.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if since, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !listModified.After(since) {
			s.notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", listModified.Format(http.TimeFormat))
		_, _ = w.Write(csv)
	})
	tl := func(w http.ResponseWriter, r *http.Request, file string) {
		s.tlCalls.Add(1)
		if r.Header.Get("Apikey") != "key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", "transitland", file))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}
	mux.HandleFunc("GET /tl/operators", func(w http.ResponseWriter, r *http.Request) {
		if f := r.URL.Query().Get("feed_onestop_id"); f != "" {
			tl(w, r, "operators-feed-"+f+".json")
			return
		}
		tl(w, r, "operators-search-lisboa.json")
	})
	mux.HandleFunc("GET /tl/feeds/{id}", func(w http.ResponseWriter, r *http.Request) {
		tl(w, r, "feed-"+r.PathValue("id")+".json")
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *server) directory(dir, key string) *directory.Directory {
	return &directory.Directory{
		CacheDir: dir, HTTP: s.Client(), TransitlandKey: key,
		TransitlandURL: s.URL + "/tl", MobilityDBURL: s.URL + "/feeds_v2.csv",
	}
}

func ids(systems []directory.System) []string {
	var out []string
	for _, s := range systems {
		out = append(out, s.ID)
	}
	return out
}

func TestSearch(t *testing.T) {
	tests := map[string]struct {
		words   string
		want    []string
		without []string
	}{
		// The catalog's entry stands in for the same feed in the list;
		// realtime systems rank above the schedule-only variant.
		"agency with several systems": {words: "mta", want: []string{"mta-subway", "mdb-507", "mdb-511", "mdb-512"}},
		// Metro-North is listed under its own name, not MTA's.
		"system name":    {words: "metro-north", want: []string{"mdb-524"}},
		"acronym":        {words: "hsl", want: []string{"hsl"}},
		"accent-folded":  {words: "lisboa", without: []string{"mdb-2027", "tld-716"}},
		"place":          {words: "san mateo", want: []string{"mdb-54"}},
		"national feeds": {words: "ovapi", want: []string{"mdb-1077"}},
	}
	d := newServer(t).directory(t.TempDir(), "")
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			res, err := d.Search(t.Context(), tt.words)
			if err != nil {
				t.Fatal(err)
			}
			got := ids(res.Systems)
			if tt.want != nil {
				if diff := cmp.Diff(tt.want, got); diff != "" {
					t.Errorf("mismatch (-want +got):\n%s", diff)
				}
			}
			for _, id := range tt.without {
				if slices.Contains(got, id) {
					t.Errorf("%s listed beside the catalog entry for it: %v", id, got)
				}
			}
			if !res.NoTransitland {
				t.Error("NoTransitland not set without a key")
			}
		})
	}
}

func TestSearchLabels(t *testing.T) {
	res, err := newServer(t).directory("", "").Search(t.Context(), "metro-north")
	if err != nil {
		t.Fatal(err)
	}
	want := directory.System{
		ID: "mdb-524", Agency: "Metro-North Railroad", Place: "Ansonia, Connecticut, US",
		Data: directory.DataRealtime, Source: directory.SourceMobilityDB,
		License:   "http://web.mta.info/developers/developer-data-terms.html",
		StaticURL: "http://web.mta.info/developers/data/mnr/google_transit.zip",
	}
	if diff := cmp.Diff(want, res.Systems[0]); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestSearchTransitland(t *testing.T) {
	s := newServer(t)
	res, err := s.directory("", "key").Search(t.Context(), "lisboa")
	if err != nil {
		t.Fatal(err)
	}
	var carris *directory.System
	for i, sys := range res.Systems {
		if sys.ID == "f-carris~pt" {
			carris = &res.Systems[i]
		}
	}
	if carris == nil {
		t.Fatalf("f-carris~pt missing from %v", ids(res.Systems))
	}
	want := directory.System{
		ID: "f-carris~pt", Agency: "Companhia Carris de Ferro de Lisboa (CARRIS)", Place: "Lisbon, Lisboa, Portugal",
		Data: directory.DataSchedule, Source: directory.SourceTransitland,
	}
	if diff := cmp.Diff(want, *carris); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}

	if _, err := s.directory("", "wrong").Search(t.Context(), "lisboa"); err != nil {
		t.Errorf("a failing Transitland failed the search: %v", err)
	}
}

func TestResolve(t *testing.T) {
	tests := map[string]struct {
		id    string
		typ   string
		opts  map[string]any
		needs []string
	}{
		"realtime feeds, alerts left out": {
			id: "mdb-53", typ: "gtfsrt",
			opts: map[string]any{
				"static_url":    "http://www.bart.gov/dev/schedules/google_transit.zip",
				"realtime_urls": []any{"http://api.bart.gov/gtfsrt/tripupdate.aspx"},
			},
		},
		"key in a URL parameter": {
			id: "mdb-54", typ: "gtfsrt", needs: []string{"CALTRAIN_API_KEY"},
			opts: map[string]any{
				"static_url":    "http://data.trilliumtransit.com/gtfs/caltrain-ca-us/caltrain-ca-us.zip",
				"realtime_urls": []any{"http://api.511.org/transit/tripupdates?agency=CT&api_key=${CALTRAIN_API_KEY}"},
			},
		},
		"key in a header": {id: "mdb-448", typ: "gtfsrt", needs: []string{"RCT_API_KEY"}},
		"replaced feed":   {id: "mdb-1037", typ: "gtfsrt", opts: map[string]any{"static_url": "https://publico.cp.pt/gtfs/gtfs.zip"}},
		"transitland schedule alone": {
			id: "f-carris~pt", typ: "gtfsrt",
			opts: map[string]any{"static_url": "https://gateway.carris.pt/gateway/gtfs/api/v2.11/GTFS"},
		},
		"transitland schedule and realtime": {
			id: "f-barta~pa", typ: "gtfsrt",
			opts: map[string]any{
				"static_url":    "https://busfinder.bartabus.com/InfoPoint/gtfs-zip.ashx",
				"realtime_urls": []any{"https://busfinder.bartabus.com/Infopoint/gtfs-realtime.ashx?type=tripupdate"},
			},
		},
		// Two schedules and a keyed regional realtime feed: Transitland
		// sorts them out.
		"transitland ambiguous": {
			id: "f-9q9-bart", typ: "transitland", needs: []string{"TRANSITLAND_API_KEY"},
			opts: map[string]any{"feed": "f-9q9-bart", "api_key": "${TRANSITLAND_API_KEY}"},
		},
	}
	d := newServer(t).directory("", "key")
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			sys, b, err := d.Resolve(t.Context(), tt.id)
			if err != nil {
				t.Fatal(err)
			}
			if b.Type != tt.typ {
				t.Errorf("type = %q, want %q", b.Type, tt.typ)
			}
			if tt.opts != nil {
				if diff := cmp.Diff(tt.opts, b.Options); diff != "" {
					t.Errorf("options mismatch (-want +got):\n%s", diff)
				}
			}
			if diff := cmp.Diff(tt.needs, sys.Needs); diff != "" {
				t.Errorf("needs mismatch (-want +got):\n%s", diff)
			}
			if b.Comment == "" {
				t.Error("no comment saying where the block came from")
			}
		})
	}
}

func TestResolveHeaderKey(t *testing.T) {
	_, b, err := newServer(t).directory("", "").Resolve(t.Context(), "mdb-448")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string]any{"authorization": "${RCT_API_KEY}"}, b.Options["headers"]); diff != "" {
		t.Errorf("headers mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveErrors(t *testing.T) {
	d := newServer(t).directory("", "")
	if _, _, err := d.Resolve(t.Context(), "mdb-999999"); !errors.Is(err, directory.ErrUnknownID) {
		t.Errorf("err = %v, want unknown", err)
	}
	if _, _, err := d.Resolve(t.Context(), "f-carris~pt"); err == nil || !strings.Contains(err.Error(), directory.TransitlandKeyVar) {
		t.Errorf("err = %v, want a pointer to the key", err)
	}
}

func TestListIsCached(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	d := s.directory(dir, "")
	d.Now = func() time.Time { return now }
	search := func() {
		t.Helper()
		if res, err := d.Search(t.Context(), "bart"); err != nil || len(res.Systems) == 0 {
			t.Fatalf("got %v, err %v", ids(res.Systems), err)
		}
	}
	search()
	search()
	if n := s.csvCalls.Load(); n != 1 {
		t.Errorf("%d downloads for two searches, want 1", n)
	}

	now = now.Add(8 * 24 * time.Hour)
	search()
	if s.notModified.Load() != 1 {
		t.Error("a week-old list was not revalidated")
	}

	now = now.Add(8 * 24 * time.Hour)
	s.csvDown.Store(true)
	search()

	if freed, err := directory.ClearCache(dir); err != nil || freed == 0 {
		t.Errorf("ClearCache = %d, %v", freed, err)
	}
}

func TestIsID(t *testing.T) {
	for id, want := range map[string]bool{"mdb-53": true, "tld-716": true, "f-9q9-bart": true, "bart": false, "mta-subway": false, "f-": false} {
		if got := directory.IsID(id); got != want {
			t.Errorf("IsID(%q) = %v", id, got)
		}
	}
}

func TestWordStarts(t *testing.T) {
	res, err := newServer(t).directory("", "").Search(t.Context(), "rail")
	if err != nil {
		t.Fatal(err)
	}
	// "Railroad" starts with "rail"; nothing should match inside a word.
	if got := ids(res.Systems); !slices.Contains(got, "mdb-524") || slices.Contains(got, "mdb-1077") {
		t.Errorf("got %v", got)
	}
}
