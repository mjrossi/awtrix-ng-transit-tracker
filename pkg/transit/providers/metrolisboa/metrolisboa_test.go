package metrolisboa_test

import (
	"context"
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

	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit/providers/metrolisboa"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit/transittest"
)

// recorded is just after testdata was fetched; see testdata/README.md.
var recorded = time.Date(2026, 11, 23, 12, 3, 10, 0, time.UTC)

const base = "/estadoServicoML/1.0.1/"

// fakeAPI mimics the WSO2 gateway: client-credentials tokens at /token, and
// 401 for data requests without the current token.
type fakeAPI struct {
	*httptest.Server

	mu          sync.Mutex
	tokens      int
	current     string
	failStation bool
	failDests   bool
	// fail lists further paths that answer 503.
	fail     map[string]bool
	requests map[string]int
	// tokenRequests counts token requests as they arrive; tokenGate, when
	// set, holds them until it is closed.
	tokenRequests int
	tokenGate     chan struct{}
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{requests: map[string]int{}, fail: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != "key" || secret != "secret" || r.PostFormValue("grant_type") != "client_credentials" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.tokenRequests++
		gate := f.tokenGate
		f.mu.Unlock()
		if gate != nil {
			<-gate
		}
		f.mu.Lock()
		f.tokens++
		f.current = fmt.Sprintf("token-%d", f.tokens)
		tok := f.current
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"scope":"am_application_scope default","token_type":"Bearer","expires_in":3600}`, tok)
	})
	mux.HandleFunc("GET "+base+"{path...}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		authorized := r.Header.Get("Authorization") == "Bearer "+f.current && f.current != ""
		path := r.PathValue("path")
		f.requests[path]++
		failStation, failDests, failPath := f.failStation, f.failDests, f.fail[path]
		f.mu.Unlock()
		if !authorized {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if failPath || failStation && path == "infoEstacao/todos" || failDests && path == "infoDestinos/todos" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", strings.ReplaceAll(path, "/", "_")+".json"))
		if err != nil {
			b, _ = os.ReadFile(filepath.Join("testdata", "bad_station.json"))
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(b)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// revoke makes the gateway reject the current token, as after expiry.
func (f *fakeAPI) revoke() {
	f.mu.Lock()
	f.current = "revoked"
	f.mu.Unlock()
}

func (f *fakeAPI) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[path]
}

func newProvider(t *testing.T, f *fakeAPI) *metrolisboa.Provider {
	t.Helper()
	p, err := metrolisboa.New(metrolisboa.Config{
		ClientID:     "key",
		ClientSecret: "secret",
		BaseURL:      f.URL + base,
		TokenURL:     f.URL + "/token",
	}, f.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContract(t *testing.T) {
	p := newProvider(t, newFakeAPI(t))
	transittest.Run(t, p, []transittest.Case{
		{Name: "two-line station", Query: transit.Query{StopID: "BC", Now: recorded, Window: 30 * time.Minute}},
		{Name: "terminal", Query: transit.Query{StopID: "CS", Now: recorded, Window: 30 * time.Minute}},
		{Name: "nothing in a zero window", Query: transit.Query{StopID: "RB", Now: recorded}, WantEmpty: true},
	})
}

type row struct{ Time, Line, Color, Headsign string }

func summarize(deps []transit.Departure) []row {
	var out []row
	for _, d := range deps {
		out = append(out, row{d.Time().UTC().Format("15:04:05"), d.Line.ShortName, d.Line.Color, d.Headsign})
	}
	return out
}

func TestDepartures(t *testing.T) {
	p := newProvider(t, newFakeAPI(t))
	q := transit.Query{StopID: "BC", Now: recorded, Window: 10 * time.Minute}
	deps, err := p.Departures(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	// Baixa/Chiado is on Azul and Verde; both lines are read and merged.
	want := []row{
		{"12:04:04", "Verde", "#00A651", "Cais do Sodré"},
		{"12:04:27", "Azul", "#0072CE", "Reboleira"},
		{"12:04:47", "Verde", "#00A651", "Telheiras"},
		{"12:04:56", "Azul", "#0072CE", "Santa Apolónia"},
		{"12:10:04", "Verde", "#00A651", "Cais do Sodré"},
		{"12:10:36", "Verde", "#00A651", "Telheiras"},
		{"12:12:17", "Azul", "#0072CE", "Reboleira"},
	}
	if diff := cmp.Diff(want, summarize(deps)); diff != "" {
		t.Errorf("departures mismatch (-want +got):\n%s", diff)
	}
	for _, d := range deps {
		if !d.Realtime() || !d.Scheduled.Equal(*d.Predicted) || d.Line.Mode != transit.ModeMetro {
			t.Errorf("departure %+v: want a metro prediction with Scheduled equal to Predicted", d)
		}
	}
}

// Cais do Sodré has a platform frozen since 2024 with "--" for every time,
// and reports each Telheiras train on two platform entries.
func TestTerminalDropsStaleAndDuplicates(t *testing.T) {
	p := newProvider(t, newFakeAPI(t))
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "CS", Now: recorded, Window: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	want := []row{
		{"12:03:26", "Verde", "#00A651", "Telheiras"},
		{"12:09:13", "Verde", "#00A651", "Telheiras"},
		{"12:15:03", "Verde", "#00A651", "Telheiras"},
	}
	if diff := cmp.Diff(want, summarize(deps)); diff != "" {
		t.Errorf("departures mismatch (-want +got):\n%s", diff)
	}
}

func TestStaleTimestampsDropped(t *testing.T) {
	p := newProvider(t, newFakeAPI(t))
	// Ten minutes later every recorded timestamp is too old to trust.
	deps, err := p.Departures(t.Context(), transit.Query{StopID: "BC", Now: recorded.Add(10 * time.Minute), Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 0 {
		t.Errorf("got %d departures from stale data, want none", len(deps))
	}
}

func TestUnknownStation(t *testing.T) {
	p := newProvider(t, newFakeAPI(t))
	_, err := p.Departures(t.Context(), transit.Query{StopID: "ZZ", Now: recorded, Window: time.Hour})
	if !errors.Is(err, metrolisboa.ErrUnknownStop) {
		t.Fatalf("got %v, want ErrUnknownStop", err)
	}
}

func TestCachesTokenMetadataAndLines(t *testing.T) {
	f := newFakeAPI(t)
	p := newProvider(t, f)
	for _, stop := range []string{"BC", "BC", "RB"} {
		if _, err := p.Departures(t.Context(), transit.Query{StopID: stop, Now: recorded, Window: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokens != 1 {
		t.Errorf("tokens issued = %d, want 1", f.tokens)
	}
	got := map[string]int{
		"infoEstacao/todos":       f.count("infoEstacao/todos"),
		"infoDestinos/todos":      f.count("infoDestinos/todos"),
		"tempoEspera/Linha/Azul":  f.count("tempoEspera/Linha/Azul"),
		"tempoEspera/Linha/Verde": f.count("tempoEspera/Linha/Verde"),
	}
	want := map[string]int{"infoEstacao/todos": 1, "infoDestinos/todos": 1, "tempoEspera/Linha/Azul": 1, "tempoEspera/Linha/Verde": 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("requests mismatch (-want +got):\n%s", diff)
	}

	// After the line cache expires, the line is fetched again.
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "RB", Now: recorded.Add(20 * time.Second), Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if n := f.count("tempoEspera/Linha/Azul"); n != 2 {
		t.Errorf("Azul fetched %d times, want 2 after the cache expires", n)
	}
}

func TestRenewsRevokedToken(t *testing.T) {
	f := newFakeAPI(t)
	p := newProvider(t, f)
	q := transit.Query{StopID: "BC", Now: recorded, Window: time.Hour}
	if _, err := p.Departures(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	f.revoke()
	q.Now = recorded.Add(time.Minute) // past the line cache
	deps, err := p.Departures(t.Context(), q)
	if err != nil {
		t.Fatalf("after revocation: %v", err)
	}
	if f.tokens != 2 || len(deps) == 0 {
		t.Errorf("tokens = %d, departures = %d; want a second token and data", f.tokens, len(deps))
	}
}

// Fetching a token must not hold up boards whose data is cached.
func TestTokenFetchDoesNotBlockCachedReads(t *testing.T) {
	f := newFakeAPI(t)
	p := newProvider(t, f)
	q := transit.Query{StopID: "BC", Now: recorded, Window: time.Hour}
	if _, err := p.Departures(t.Context(), q); err != nil {
		t.Fatal(err)
	}

	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release) // before the server closes, which waits for it
	f.mu.Lock()
	f.tokenGate = gate
	f.mu.Unlock()
	f.revoke()
	renewed := make(chan error, 1)
	go func() {
		later := q
		later.Now = recorded.Add(time.Minute) // past the line cache
		_, err := p.Departures(t.Context(), later)
		renewed <- err
	}()
	for {
		f.mu.Lock()
		n := f.tokenRequests
		f.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.Departures(ctx, q)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("cached read: %v", err)
		}
	case <-ctx.Done():
		t.Error("cached read waited for the token request")
	}
	release()
	if err := <-renewed; err != nil {
		t.Errorf("after renewal: %v", err)
	}
}

// Baixa/Chiado is on Azul and Verde; one failing line must not hide the
// other, while a station served only by the failing line reports it.
func TestFailingLineGivesPartialResult(t *testing.T) {
	f := newFakeAPI(t)
	f.fail["tempoEspera/Linha/Azul"] = true
	p := newProvider(t, f)

	deps, err := p.Departures(t.Context(), transit.Query{StopID: "BC", Now: recorded, Window: time.Hour})
	var partial *transit.PartialError
	if !errors.As(err, &partial) || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want a partial result naming the failure", err)
	}
	if len(deps) == 0 {
		t.Fatal("got no departures, want Verde's")
	}
	for _, d := range deps {
		if d.Line.ID != "verde" {
			t.Errorf("got a %s departure, want only Verde", d.Line.ID)
		}
	}

	_, err = p.Departures(t.Context(), transit.Query{StopID: "RB", Now: recorded, Window: time.Hour})
	if err == nil || errors.As(err, &partial) {
		t.Errorf("Reboleira (Azul only): err = %v, want a plain error", err)
	}
}

func TestBadCredentials(t *testing.T) {
	f := newFakeAPI(t)
	p, err := metrolisboa.New(metrolisboa.Config{
		ClientID: "key", ClientSecret: "wrong", BaseURL: f.URL + base, TokenURL: f.URL + "/token",
	}, f.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Departures(t.Context(), transit.Query{StopID: "BC", Now: recorded, Window: time.Hour})
	if err == nil || !strings.Contains(err.Error(), "client_secret") {
		t.Fatalf("got %v, want a credentials hint", err)
	}
}

func TestMetadataRefreshFailureKeepsCache(t *testing.T) {
	f := newFakeAPI(t)
	p := newProvider(t, f)
	if _, err := p.Departures(t.Context(), transit.Query{StopID: "BC", Now: recorded, Window: time.Hour}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.failStation = true
	f.mu.Unlock()
	_, err := p.Departures(t.Context(), transit.Query{StopID: "BC", Now: recorded.Add(25 * time.Hour), Window: time.Hour})
	if err != nil {
		t.Fatalf("a failed daily refresh should keep the cached station list: %v", err)
	}
	if f.count("infoEstacao/todos") != 2 {
		t.Error("expected a refresh attempt after a day")
	}
}

func TestSearchStops(t *testing.T) {
	p := newProvider(t, newFakeAPI(t))
	tests := []struct {
		search string
		want   []string
	}{
		{"baixa chiado", []string{"BC"}},
		{"SAO SEBASTIAO", []string{"SS"}},
		{"cg", []string{"CG"}},
		{"nowhere", nil},
	}
	for _, tt := range tests {
		stops, err := p.SearchStops(t.Context(), tt.search)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, s := range stops {
			ids = append(ids, s.ID)
		}
		if diff := cmp.Diff(tt.want, ids); diff != "" {
			t.Errorf("SearchStops(%q) mismatch (-want +got):\n%s", tt.search, diff)
		}
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := metrolisboa.New(metrolisboa.Config{}, nil); err == nil {
		t.Error("New accepted empty credentials")
	}
	if _, err := metrolisboa.New(metrolisboa.Config{ClientID: "a", ClientSecret: "b", BaseURL: "ftp://x"}, nil); err == nil {
		t.Error("New accepted a non-http base_url")
	}
}

// A failed destination list is retried soon rather than after a day, so
// headsigns are not left as destination codes.
func TestRetriesFailedDestinations(t *testing.T) {
	f := newFakeAPI(t)
	f.failDests = true
	p := newProvider(t, f)
	q := func(after time.Duration) {
		t.Helper()
		if _, err := p.Departures(t.Context(), transit.Query{StopID: "BC", Now: recorded.Add(after), Window: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	q(0)
	q(time.Minute)
	if n := f.count("infoDestinos/todos"); n != 1 {
		t.Errorf("destinations fetched %d times, want 1 within the retry interval", n)
	}
	f.mu.Lock()
	f.failDests = false
	f.mu.Unlock()
	q(16 * time.Minute)
	if n := f.count("infoDestinos/todos"); n != 2 {
		t.Errorf("destinations fetched %d times, want a retry after 15 minutes", n)
	}
	q(17 * time.Minute)
	if n := f.count("infoDestinos/todos"); n != 2 {
		t.Errorf("destinations fetched %d times, want the list cached once loaded", n)
	}
}
