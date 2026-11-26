package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

func noEnv(string) (string, bool) { return "", false }

// demoConfig is a whole configuration that needs no network to preview.
const demoConfig = `[clock]
url = "http://clock.local"
[providers.demo]
[[providers.demo.lines]]
id = "azul"
headsign = "Reboleira"
headway = "6m"
[[boards]]
name = "metro"
[[boards.stops]]
provider = "demo"
stop = "s1"
`

// runCLI runs the command line args, with src as its configuration file, and
// returns what it prints.
func runCLI(t *testing.T, src string, args ...string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.Writer = &out
	err := cmd.Run(t.Context(), append([]string{"urbanist-timetable", "--config", path}, args...))
	return out.String(), err
}

// validateConfig runs `config validate` on src and returns what it prints.
func validateConfig(t *testing.T, src string) (string, error) {
	t.Helper()
	return runCLI(t, src, "config", "validate")
}

func TestSystemsCommands(t *testing.T) {
	tests := map[string]struct {
		args []string
		want []string
	}{
		"list": {[]string{"systems"}, []string{"sf-bart", "Show how to configure one"}},
		"show": {[]string{"systems", "show", "sf-bart"}, []string{"[providers.sf-bart]\nsystem = \"sf-bart\"\n", "# Example stop: "}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := runCLI(t, demoConfig, tt.args...)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output %q, want %q", out, want)
				}
			}
		})
	}
	if _, err := runCLI(t, demoConfig, "systems", "show", "nowhere"); err == nil || !strings.Contains(err.Error(), `unknown system "nowhere"`) {
		t.Errorf("err = %v, want unknown system", err)
	}
}

func TestStopsSearchPrintsMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"010001","long_name":"Cais do Sodré"},{"id":"010002","long_name":"Rossio"}]`)
	}))
	defer srv.Close()
	src := demoConfig + fmt.Sprintf("[providers.carrismetropolitana]\nbase_url = %q\n", srv.URL)
	tests := map[string]struct{ search, want string }{
		"match":    {"cais", "010001     Cais do Sodré\n"},
		"no match": {"nowhere", "no stops found\n"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := runCLI(t, src, "stops", "search", "carrismetropolitana", tt.search)
			if err != nil {
				t.Fatal(err)
			}
			if out != tt.want {
				t.Errorf("output %q, want %q", out, tt.want)
			}
		})
	}
	if _, err := runCLI(t, src, "stops", "search", "demo", "x"); err == nil || !strings.Contains(err.Error(), "cannot search stops") {
		t.Errorf("err = %v, want the provider to lack search", err)
	}
}

func TestPreviewPrintsRowsAndPayload(t *testing.T) {
	out, err := runCLI(t, demoConfig, "clock", "preview")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# metro (departure layout)", "Reboleira", `"layout": {`} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q, want %q", out, want)
		}
	}

	out, err = runCLI(t, demoConfig, "clock", "preview", "--json", "--board", "metro")
	if err != nil {
		t.Fatal(err)
	}
	var app map[string]any
	if err := json.Unmarshal([]byte(out), &app); err != nil {
		t.Errorf("--json printed more than a payload: %v", err)
	}

	if _, err := runCLI(t, demoConfig, "clock", "preview", "--board", "bus"); err == nil || !strings.Contains(err.Error(), `no board named "bus"`) {
		t.Errorf("err = %v, want no such board", err)
	}
}

// A bare host must identify its clock too.
func TestCleanupIdentifiesTheClock(t *testing.T) {
	for _, u := range []string{"192.168.1.50", "http://192.168.1.50/"} {
		clock, err := awtrix.NewClient(u)
		if err != nil {
			t.Fatal(err)
		}
		if got := newCleanup(&config.Config{}, "", clock, nil).Clock; got != "192.168.1.50" {
			t.Errorf("%q: clock = %q", u, got)
		}
	}
}

func TestCleanupFollowsQuietHours(t *testing.T) {
	clock, err := awtrix.NewClient("192.168.1.50")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{QuietHours: "23:00-06:00"}
	c := newCleanup(cfg, "", clock, nil)
	if night := time.Date(2026, 11, 24, 0, 30, 0, 0, cfg.Location()); c.Quiet == nil || !c.Quiet(night) {
		t.Error("cleanup is not paused in quiet hours")
	}
}

func TestVersionFlag(t *testing.T) {
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.Writer = &out
	if err := cmd.Run(t.Context(), []string{"urbanist-timetable", "--version"}); err != nil {
		t.Fatal(err)
	}
	if want := "urbanist-timetable version " + version + "\n"; out.String() != want {
		t.Errorf("--version printed %q, want %q", out.String(), want)
	}
}

func TestConfigValidateWithoutClock(t *testing.T) {
	out, err := validateConfig(t, "[providers.carrismetropolitana]\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "OK (1 providers, 0 boards)") {
		t.Errorf("output %q", out)
	}
	if _, err := runCLI(t, "[providers.carrismetropolitana]\n", "clock", "preview"); err == nil || !strings.Contains(err.Error(), "clock.url") {
		t.Errorf("preview without a clock: err = %v", err)
	}
}

func TestConfigValidateBuildsClock(t *testing.T) {
	src := "[clock]\nurl = \"ftp://clock.local\"\n[providers.demo]\n[[providers.demo.lines]]\nid = \"azul\"\nheadway = \"6m\"\n[[boards]]\nname = \"metro\"\n[[boards.stops]]\nprovider = \"demo\"\nstop = \"s1\"\n"
	_, err := validateConfig(t, src)
	if err == nil || !strings.Contains(err.Error(), "clock url") {
		t.Errorf("err = %v, want a clock url error", err)
	}
}

func TestOpenSource(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.toml")
	if _, err := openSource(missing, "helsinki-hsl"); err != nil {
		t.Errorf("without a config file: %v", err)
	}

	unrelated := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(unrelated, []byte("[clock]\nurl = \"${TT_TEST_UNSET_CLOCK}\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openSource(unrelated, "helsinki-hsl"); err != nil {
		t.Errorf("an unset variable outside the block stopped the query: %v", err)
	}

	t.Setenv("LISBON_METRO_CLIENT_ID", "id")
	t.Setenv("LISBON_METRO_CLIENT_SECRET", "secret")
	if _, err := openSource(missing, "lisbon-metro"); err != nil {
		t.Errorf("keys from the environment: %v", err)
	}
	t.Setenv("LISBON_METRO_CLIENT_SECRET", "")
	var me *catalog.MissingOptionsError
	if _, err := openSource(missing, "lisbon-metro"); !errors.As(err, &me) || !slices.Equal(me.Options, []string{"client_secret"}) {
		t.Errorf("err = %v, want client_secret missing", err)
	}
}

// preview must mark the same rows hidden that the renderer leaves out.
func TestPrintRowsMarksHiddenRows(t *testing.T) {
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	line := transit.Line{ID: "1", ShortName: "1", Mode: transit.ModeMetro}
	row := func(headsign string, in time.Duration) transit.Row {
		return transit.Row{Line: line, Headsign: headsign, Departures: []transit.Departure{{
			Line: line, Headsign: headsign, Scheduled: now.Add(in), Status: transit.StatusScheduled,
		}}}
	}
	// The soonest row is not the one the departure layout keeps.
	rows := []transit.Row{row("South Ferry", 2*time.Minute), row("Van Cortlandt Park", 5*time.Minute)}
	var out bytes.Buffer
	printRows(&out, config.Board{Name: "b", Clock: config.BoardClock{Layout: render.LayoutDeparture}}, rows, now)
	for _, l := range strings.Split(out.String(), "\n") {
		hidden := strings.Contains(l, "(not shown)")
		if strings.Contains(l, "South Ferry") && hidden || strings.Contains(l, "Van Cortlandt") && !hidden {
			t.Errorf("wrong mark: %q", l)
		}
	}
}

func TestBoardForUsesBoardTimezone(t *testing.T) {
	src := `timezone = "Europe/Lisbon"
quiet_hours = "23:00-06:00"
[clock]
url = "http://clock.local"
[providers.demo]
[[providers.demo.lines]]
id = "azul"
headway = "6m"
[[boards]]
name = "lisbon"
active_hours = "07:00-09:00"
[[boards.stops]]
provider = "demo"
stop = "s1"
[[boards]]
name = "tokyo"
timezone = "Asia/Tokyo"
active_hours = "07:00-09:00"
[[boards.stops]]
provider = "demo"
stop = "s1"
`
	cfg, err := config.Parse([]byte(src), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	provs := map[string]transit.Provider{"demo": nil}
	// 08:00 in Lisbon is 17:00 in Tokyo, so only the Lisbon board is active.
	instant := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		board, zone string
		active      bool
	}{
		{"lisbon", "Europe/Lisbon", true},
		{"tokyo", "Asia/Tokyo", false},
	}
	for i, tt := range tests {
		sb, err := boardFor(cfg, cfg.Boards[i], provs)
		if err != nil {
			t.Fatal(err)
		}
		if sb.Name != tt.board || sb.Location.String() != tt.zone {
			t.Errorf("board %s: location = %s, want %s", sb.Name, sb.Location, tt.zone)
		}
		if got := sb.Active(instant.In(sb.Location)); got != tt.active {
			t.Errorf("board %s: active = %v, want %v", sb.Name, got, tt.active)
		}
		// 00:30 in Lisbon is 09:30 in Tokyo.
		night := time.Date(2026, 11, 24, 0, 30, 0, 0, time.UTC)
		if sb.Quiet(instant.In(sb.Location)) || !sb.Quiet(night.In(sb.Location)) {
			t.Errorf("board %s: quiet hours do not follow the top-level time zone", sb.Name)
		}
	}
}

func TestConfigValidateEstimatesQuota(t *testing.T) {
	tests := map[string]struct {
		refresh string
		want    string
		warn    bool
	}{
		"within quota": {"5m", "providers.tl: about 8928 requests a month, quota 10000\n", false},
		"over quota":   {"2m", "providers.tl: about 22320 requests a month, quota 10000\n", true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			src := "[clock]\nurl = \"http://clock.local\"\n[providers.tl]\ntype = \"transitland\"\napi_key = \"k\"\nfeed = \"f-drt-mbta\"\nrefresh_interval = \"" + tt.refresh + "\"\n[[boards]]\nname = \"mbta\"\n[[boards.stops]]\nprovider = \"tl\"\nstop = \"place-sstat\"\n"
			out, err := validateConfig(t, src)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("output %q, want %q", out, tt.want)
			}
			if got := strings.Contains(out, "warning: providers.tl would exceed"); got != tt.warn {
				t.Errorf("warning printed = %v, want %v: %q", got, tt.warn, out)
			}
		})
	}
}

func TestSharedQuotaIgnoresBlocksThatDoNotSetIt(t *testing.T) {
	tests := map[string]struct {
		a, b string
		want int
	}{
		"neither set":         {"", "", 10000},
		"only the first set":  {"monthly_quota = 50000", "", 50000},
		"only the second set": {"", "monthly_quota = 50000", 50000},
		"set to the default":  {"monthly_quota = 50000", "monthly_quota = 10000", 10000},
		"both set":            {"monthly_quota = 20000", "monthly_quota = 50000", 20000},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			src := fmt.Sprintf(`
[clock]
url = "http://clock.local"
[providers.a]
type = "transitland"
api_key = "k"
%s
[providers.b]
type = "transitland"
api_key = "k"
%s
[[boards]]
name = "a"
[[boards.stops]]
provider = "a"
stop = "f-x:1"
`, tt.a, tt.b)
			cfg, err := config.Parse([]byte(src), noEnv)
			if err != nil {
				t.Fatal(err)
			}
			provs, err := buildProviders(cfg)
			if err != nil {
				t.Fatal(err)
			}
			uses := cfg.QuotaUses(provs)
			if len(uses) != 1 || uses[0].Quota != tt.want {
				t.Errorf("uses = %+v, want one with quota %d", uses, tt.want)
			}
		})
	}
}

func TestConfigValidateSumsBlocksSharingAKey(t *testing.T) {
	tests := map[string]struct {
		muniKey string
		want    []string
		warn    bool
	}{
		"same key": {"k", []string{"providers.bart, providers.muni: about 17856 requests a month, quota 10000\n"}, true},
		"own keys": {"k2", []string{
			"providers.bart: about 8928 requests a month, quota 10000\n",
			"providers.muni: about 8928 requests a month, quota 10000\n",
		}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var src strings.Builder
			src.WriteString("[clock]\nurl = \"http://clock.local\"\n")
			for block, key := range map[string]string{"bart": "k", "muni": tt.muniKey} {
				fmt.Fprintf(&src, "[providers.%[1]s]\ntype = \"transitland\"\napi_key = %[2]q\nfeed = \"f-%[1]s\"\n[[boards]]\nname = %[1]q\n[[boards.stops]]\nprovider = %[1]q\nstop = \"1\"\n", block, key)
			}
			out, err := validateConfig(t, src.String())
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output %q, want %q", out, want)
				}
			}
			if got := strings.Contains(out, "warning: "); got != tt.warn {
				t.Errorf("warning printed = %v, want %v: %q", got, tt.warn, out)
			}
		})
	}
}

func TestHint(t *testing.T) {
	tests := map[string]struct {
		err  error
		want string
	}{
		"unknown system": {fmt.Errorf("providers.x: %w", fmt.Errorf("%w %q", catalog.ErrUnknownSystem, "atlantis")), "run `urbanist-timetable systems`"},
		"missing keys":   {errors.Join(errors.New("other"), &catalog.MissingOptionsError{System: "lisbon-metro", Options: []string{"client_id"}}), "run `urbanist-timetable systems show lisbon-metro`"},
		"anything else":  {errors.New("boom"), ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := hint(tt.err); got != tt.want {
				t.Errorf("hint = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOldCommandsAreGone(t *testing.T) {
	root := newRootCommand()
	for _, name := range []string{"run", "preview"} {
		if root.Command(name) != nil {
			t.Errorf("root still has %q", name)
		}
		if c := root.Command("clock"); c == nil || c.Command(name) == nil {
			t.Errorf("clock lacks %q", name)
		}
	}
}
