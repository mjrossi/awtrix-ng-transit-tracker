package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/awtrix-ng-transit-tracker/internal/config"
	"github.com/mjrossi/awtrix-ng-transit-tracker/internal/render"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit"
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
	if err := cmd.Run(t.Context(), []string{"transit-tracker", "--version"}); err != nil {
		t.Fatal(err)
	}
	if want := "transit-tracker version " + version + "\n"; out.String() != want {
		t.Errorf("--version printed %q, want %q", out.String(), want)
	}
}

func TestConfigValidateBuildsClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	src := "[clock]\nurl = \"ftp://clock.local\"\n[providers.demo]\n[[providers.demo.lines]]\nid = \"azul\"\nheadway = \"6m\"\n[[boards]]\nname = \"metro\"\n[[boards.stops]]\nprovider = \"demo\"\nstop = \"s1\"\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	err := newRootCommand().Run(t.Context(), []string{"transit-tracker", "--config", path, "config", "validate"})
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
	printRows(&out, config.Board{Name: "b", Layout: render.LayoutDeparture}, rows, now)
	for _, l := range strings.Split(out.String(), "\n") {
		hidden := strings.Contains(l, "(not shown)")
		if strings.Contains(l, "South Ferry") && hidden || strings.Contains(l, "Van Cortlandt") && !hidden {
			t.Errorf("wrong mark: %q", l)
		}
	}
}
