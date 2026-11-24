package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

func TestClockHostDropsCredentials(t *testing.T) {
	if got := clockHost("http://admin:hunter2@clock.lan:8080"); got != "clock.lan:8080" {
		t.Errorf("clockHost = %q", got)
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
		sb, err := boardFor(cfg.Boards[i], provs)
		if err != nil {
			t.Fatal(err)
		}
		if sb.Name != tt.board || sb.Location.String() != tt.zone {
			t.Errorf("board %s: location = %s, want %s", sb.Name, sb.Location, tt.zone)
		}
		if got := sb.Active(instant.In(sb.Location)); got != tt.active {
			t.Errorf("board %s: active = %v, want %v", sb.Name, got, tt.active)
		}
	}
}
