package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

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

func TestConfigValidateBuildsClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	src := "[clock]\nurl = \"ftp://clock.local\"\n[providers.demo]\n[[providers.demo.lines]]\nid = \"azul\"\nheadway = \"6m\"\n[[boards]]\nname = \"metro\"\n[[boards.stops]]\nprovider = \"demo\"\nstop = \"s1\"\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	err := newRootCommand().Run(t.Context(), []string{"urbanist-timetable", "--config", path, "config", "validate"})
	if err == nil || !strings.Contains(err.Error(), "clock url") {
		t.Errorf("err = %v, want a clock url error", err)
	}
}

func TestProviderForSearch(t *testing.T) {
	dir := t.TempDir()
	if _, err := providerForSearch(filepath.Join(dir, "missing.toml"), "helsinki-hsl"); err != nil {
		t.Errorf("without a config file: %v", err)
	}

	broken := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(broken, []byte("[clock]\nurl = \"${TT_TEST_UNSET_CLOCK}\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := providerForSearch(broken, "helsinki-hsl"); err == nil || !strings.Contains(err.Error(), "TT_TEST_UNSET_CLOCK") {
		t.Errorf("err = %v; want the config error reported", err)
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
	cfg, err := config.Parse([]byte(src), func(string) (string, bool) { return "", false })
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
			path := filepath.Join(t.TempDir(), "config.toml")
			src := "[clock]\nurl = \"http://clock.local\"\n[providers.tl]\ntype = \"transitland\"\napi_key = \"k\"\nfeed = \"f-drt-mbta\"\nrefresh_interval = \"" + tt.refresh + "\"\n[[boards]]\nname = \"mbta\"\n[[boards.stops]]\nprovider = \"tl\"\nstop = \"place-sstat\"\n"
			if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := newRootCommand()
			var out bytes.Buffer
			cmd.Writer = &out
			if err := cmd.Run(t.Context(), []string{"urbanist-timetable", "--config", path, "config", "validate"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Errorf("output %q, want %q", out.String(), tt.want)
			}
			if got := strings.Contains(out.String(), "warning: providers.tl would exceed"); got != tt.warn {
				t.Errorf("warning printed = %v, want %v: %q", got, tt.warn, out.String())
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
			path := filepath.Join(t.TempDir(), "config.toml")
			var src strings.Builder
			src.WriteString("[clock]\nurl = \"http://clock.local\"\n")
			for block, key := range map[string]string{"bart": "k", "muni": tt.muniKey} {
				fmt.Fprintf(&src, "[providers.%[1]s]\ntype = \"transitland\"\napi_key = %[2]q\nfeed = \"f-%[1]s\"\n[[boards]]\nname = %[1]q\n[[boards.stops]]\nprovider = %[1]q\nstop = \"1\"\n", block, key)
			}
			if err := os.WriteFile(path, []byte(src.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := newRootCommand()
			var out bytes.Buffer
			cmd.Writer = &out
			if err := cmd.Run(t.Context(), []string{"urbanist-timetable", "--config", path, "config", "validate"}); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output %q, want %q", out.String(), want)
				}
			}
			if got := strings.Contains(out.String(), "warning: "); got != tt.warn {
				t.Errorf("warning printed = %v, want %v: %q", got, tt.warn, out.String())
			}
		})
	}
}
