package config_test

import (
	"os"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // zone tests must not depend on the host's zoneinfo

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

func noEnv(string) (string, bool) { return "", false }

func TestExampleConfig(t *testing.T) {
	b, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(b, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Location().String() != "Europe/Lisbon" {
		t.Errorf("location = %s", cfg.Location())
	}
	b0 := cfg.Boards[0]
	if b0.PollInterval.D() != 45*time.Second || b0.Clock.Lifetime.D() != 135*time.Second {
		t.Errorf("poll %s lifetime %s, want 45s and 135s", b0.PollInterval.D(), b0.Clock.Lifetime.D())
	}
	if got := b0.Clock.Labels["Santa Apolónia"]; got != "Sta Apolónia" {
		t.Errorf("label = %q, want the example's [boards.clock.labels] entry", got)
	}
	reg := providers.Registry()
	for name := range cfg.Providers {
		typ, opts, err := cfg.Provider(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reg.New(typ, opts); err != nil {
			t.Errorf("provider %s: %v", name, err)
		}
	}
}

const minimal = `
[clock]
url = "%s"
[providers.demo]
[[boards]]
name = "metro"
[[boards.stops]]
provider = "demo"
stop = "s1"
`

func TestDefaults(t *testing.T) {
	cfg, err := config.Parse([]byte(strings.Replace(minimal, "%s", "clock.local", 1)), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Boards[0]
	got := []time.Duration{b.PollInterval.D(), b.Window.D(), b.Clock.Lifetime.D(), b.MaxStale.D()}
	want := []time.Duration{config.DefaultPollInterval, config.DefaultWindow, 3 * config.DefaultPollInterval, config.DefaultMaxStale}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("defaults mismatch (-want +got):\n%s", diff)
	}
	if r := b.Clock.Render(); r.Layout != "departure" || r.Overflow != "scroll" || r.MaxRows != 2 {
		t.Errorf("render options %+v, want departure, scroll and 2 rows", r)
	}
	if b.PerRow != 3 {
		t.Errorf("per row %d, want 3", b.PerRow)
	}
	if cfg.Location() != time.Local {
		t.Error("empty timezone should be local")
	}
}

func TestEnvExpansion(t *testing.T) {
	src := "# ${IN_A_COMMENT} is ignored\n" + strings.Replace(minimal, "%s", "${CLOCK}", 1) +
		"[[providers.demo.lines]]\nid = \"${CLOCK}\"\n"
	env := func(k string) (string, bool) {
		if k == "CLOCK" {
			return `http://clock"quoted\`, true
		}
		return "", false
	}
	cfg, err := config.Parse([]byte(src), env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clock.URL != `http://clock"quoted\` {
		t.Errorf("url = %q", cfg.Clock.URL)
	}
	line := cfg.Providers["demo"]["lines"].([]any)[0].(map[string]any)
	if line["id"] != `http://clock"quoted\` {
		t.Errorf("provider value = %q", line["id"])
	}
	if _, err := config.Parse([]byte(src), noEnv); err == nil || !strings.Contains(err.Error(), "CLOCK") {
		t.Errorf("missing variable: got %v", err)
	}
}

func TestInvalid(t *testing.T) {
	tests := map[string]struct{ src, want string }{
		"unknown key":         {minimal + "color = 1\n", "color"},
		"no clock":            {strings.Replace(minimal, `url = "%s"`, "", 1), "clock.url"},
		"bad name":            {strings.Replace(minimal, `name = "metro"`, `name = "my board"`, 1), "name"},
		"fast poll":           {strings.Replace(minimal, `name = "metro"`, "name = \"m\"\npoll_interval = \"5s\"", 1), "poll_interval"},
		"short life":          {withClock("lifetime = \"20s\""), "clock.lifetime"},
		"no provider section": {strings.Replace(minimal, "[providers.demo]", "", 1), "providers.demo"},
		"bad hours":           {strings.Replace(minimal, `name = "metro"`, "name = \"m\"\nactive_hours = \"6-23\"", 1), "active_hours"},
		"bad zone":            {"timezone = \"Mars/Olympus\"\n" + minimal, "timezone"},
		"bad layout":          {withClock("layout = \"grid\""), "clock: layout"},
		"bad overflow":        {withClock("overflow = \"wrap\""), "overflow"},
		"negative rows":       {withClock("max_rows = -1"), "max_rows"},
		"duplicate":           {minimal + "[[boards]]\nname = \"metro\"\n[[boards.stops]]\nprovider = \"demo\"\nstop = \"s\"\n", "duplicate"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(tt.src, "%s", "clock.local", 1)
			_, err := config.Parse([]byte(src), noEnv)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error mentioning %q", err, tt.want)
			}
		})
	}
}

// withClock returns the minimal config with a [boards.clock] table holding
// settings.
func withClock(settings string) string {
	return strings.Replace(minimal, `name = "metro"`, "name = \"metro\"\n[boards.clock]\n"+settings, 1)
}

// Settings that moved to [boards.clock] in v0.3.0 are reported as moved,
// not just as unknown.
func TestMovedClockSettings(t *testing.T) {
	tests := map[string]string{
		"layout":     `layout = "rows"`,
		"max_rows":   "max_rows = 1",
		"lifetime":   `lifetime = "5m"`,
		"duration":   `duration = "10s"`,
		"font":       `font = "small"`,
		"empty_text": `empty_text = "None"`,
		"overflow":   `overflow = "clip"`,
		"labels":     "labels = { \"A\" = \"B\" }",
	}
	for key, line := range tests {
		t.Run(key, func(t *testing.T) {
			src := strings.Replace(strings.Replace(minimal, "%s", "clock.local", 1), `name = "metro"`, "name = \"metro\"\n"+line, 1)
			_, err := config.Parse([]byte(src), noEnv)
			if err == nil || !strings.Contains(err.Error(), "[boards.clock") || !strings.Contains(err.Error(), key) {
				t.Fatalf("got %v, want a hint that %s moved to [boards.clock]", err, key)
			}
		})
	}
	t.Run("labels table", func(t *testing.T) {
		src := strings.Replace(minimal, "%s", "clock.local", 1) + "[boards.labels]\n\"A\" = \"B\"\n"
		if _, err := config.Parse([]byte(src), noEnv); err == nil || !strings.Contains(err.Error(), "[boards.clock.labels]") {
			t.Fatalf("got %v, want a hint about [boards.clock.labels]", err)
		}
	})
	t.Run("other unknown key", func(t *testing.T) {
		src := strings.Replace(strings.Replace(minimal, "%s", "clock.local", 1), `name = "metro"`, "name = \"metro\"\nspeed = 1", 1)
		if _, err := config.Parse([]byte(src), noEnv); err == nil || strings.Contains(err.Error(), "[boards.clock]") {
			t.Fatalf("got %v, want an unknown-key error without the move hint", err)
		}
	})
}

func TestProviderOptionsStrict(t *testing.T) {
	var v struct {
		Lines []struct {
			ID string `toml:"id"`
		} `toml:"lines"`
	}
	opts := config.ProviderOptions{"lines": []any{map[string]any{"id": "a", "colr": "x"}}}
	if err := opts.Decode(&v); err == nil {
		t.Error("unknown provider key accepted")
	}

	// Values are expanded from the environment by now; a typo in a
	// secret's key must not print the secret.
	opts = config.ProviderOptions{"clientsecret": "SUPERSECRET"}
	err := opts.Decode(&v)
	if err == nil || !strings.Contains(err.Error(), "clientsecret") || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("err = %v; want the key named without its value", err)
	}
}

func TestActiveHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.UTC) }
	tests := []struct {
		spec string
		t    time.Time
		want bool
	}{
		{"", at(3, 0), true},
		{"06:00-23:30", at(5, 59), false},
		{"06:00-23:30", at(6, 0), true},
		{"06:00-23:30", at(23, 29), true},
		{"06:00-23:30", at(23, 30), false},
		{"18:00-02:00", at(1, 59), true},
		{"18:00-02:00", at(2, 0), false},
		{"18:00-02:00", at(17, 0), false},
		{"18:00-02:00", at(20, 0), true},
	}
	for _, tt := range tests {
		var a config.ActiveHours
		if err := a.UnmarshalText([]byte(tt.spec)); err != nil {
			t.Fatal(err)
		}
		if got := a.Contains(tt.t); got != tt.want {
			t.Errorf("%q at %s = %v, want %v", tt.spec, tt.t.Format("15:04"), got, tt.want)
		}
	}
	for _, bad := range []string{"06:00", "6-23", "25:00-26:00", "08:00-08:00"} {
		var a config.ActiveHours
		if err := a.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBoardTimezone(t *testing.T) {
	tests := []struct {
		name, top, board string
		want             string // location name; empty means time.Local
		wantErr          string
	}{
		{name: "board overrides top", top: "Europe/Lisbon", board: "America/New_York", want: "America/New_York"},
		{name: "board inherits top", top: "Europe/Lisbon", want: "Europe/Lisbon"},
		{name: "board only", board: "Asia/Tokyo", want: "Asia/Tokyo"},
		{name: "neither", want: ""},
		{name: "board Local overrides top", top: "Europe/Lisbon", board: "Local", want: ""},
		{name: "invalid board zone", board: "Mars/Olympus", wantErr: "boards[0] (metro): timezone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := strings.Replace(minimal, "%s", "clock.local", 1)
			if tt.top != "" {
				src = "timezone = \"" + tt.top + "\"\n" + src
			}
			if tt.board != "" {
				src = strings.Replace(src, "name = \"metro\"\n", "name = \"metro\"\ntimezone = \""+tt.board+"\"\n", 1)
			}
			cfg, err := config.Parse([]byte(src), noEnv)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := cfg.Boards[0].Location()
			if tt.want == "" {
				if got != time.Local {
					t.Errorf("location = %s, want time.Local", got)
				}
				return
			}
			if got.String() != tt.want {
				t.Errorf("location = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestLabels(t *testing.T) {
	src := strings.Replace(minimal, "%s", "clock.local", 1) + "[boards.clock.labels]\n\"Santa Apolónia\" = \"Sta Apolónia\"\n"
	cfg, err := config.Parse([]byte(src), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Boards[0].Clock.Labels["Santa Apolónia"]; got != "Sta Apolónia" {
		t.Errorf("label = %q", got)
	}
}

func TestStopFilter(t *testing.T) {
	src := strings.Replace(minimal, "%s", "clock.local", 1) +
		"lines = [\"azul\"]\nheadsigns = [\"Santa\"]\nmin_lead = \"4m\"\nkeep_canceled = true\n"
	cfg, err := config.Parse([]byte(src), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := transit.Filter{Lines: []string{"azul"}, Headsigns: []string{"Santa"}, MinLead: 4 * time.Minute, KeepCanceled: true}
	if diff := cmp.Diff(want, cfg.Boards[0].Stops[0].Filter()); diff != "" {
		t.Errorf("filter mismatch (-want +got):\n%s", diff)
	}
}

func TestProviderType(t *testing.T) {
	src := strings.Replace(minimal, "%s", "clock.local", 1) + "[providers.nyc]\ntype = \"gtfsrt\"\nrealtime_urls = [\"https://x\"]\n"
	cfg, err := config.Parse([]byte(src), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	typ, opts, err := cfg.Provider("nyc")
	if err != nil || typ != "gtfsrt" {
		t.Fatalf("Provider(nyc) = %q, %v", typ, err)
	}
	if _, has := opts["type"]; has {
		t.Error("type key should not reach the provider's options")
	}
	if typ, _, _ := cfg.Provider("demo"); typ != "demo" {
		t.Errorf("block without type: got %q, want its name", typ)
	}
	bad := strings.Replace(minimal, "%s", "clock.local", 1) + "[providers.x]\ntype = 3\n"
	if _, err := config.Parse([]byte(bad), noEnv); err == nil || !strings.Contains(err.Error(), "providers.x") {
		t.Errorf("non-string type: got %v", err)
	}
}

func TestProviderSystem(t *testing.T) {
	base := strings.Replace(minimal, "%s", "clock.local", 1)
	cfg, err := config.Parse([]byte(base+"[providers.nyc]\nsystem = \"nyc-subway\"\nheaders = { \"X-Test\" = \"1\" }\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	typ, opts, err := cfg.Provider("nyc")
	if err != nil {
		t.Fatal(err)
	}
	if typ != "gtfsrt" || opts["static_url"] == nil || opts["headers"] == nil {
		t.Errorf("got type %q options %v; want the catalog's gtfsrt settings plus the user's headers", typ, opts)
	}
	if _, has := opts["system"]; has {
		t.Error("system key should not reach the provider's options")
	}

	tests := map[string]struct{ block, want string }{
		"unknown system":  {"[providers.x]\nsystem = \"atlantis-metro\"\n", "unknown system"},
		"missing keys":    {"[providers.metro]\nsystem = \"lisbon-metro\"\n", "client_id and client_secret"},
		"system and type": {"[providers.x]\nsystem = \"sf-bart\"\ntype = \"gtfsrt\"\n", "not both"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := config.Parse([]byte(base+tt.block), noEnv)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error mentioning %q", err, tt.want)
			}
		})
	}
}
