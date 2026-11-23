package render_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/awtrix-ng-transit-tracker/internal/render"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/awtrix"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit"
)

var update = flag.Bool("update", false, "rewrite golden files")

var now = time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)

func dep(line transit.Line, headsign string, in time.Duration, live bool) transit.Departure {
	d := transit.Departure{Line: line, Headsign: headsign, StopID: "s", Scheduled: now.Add(in), Status: transit.StatusScheduled}
	if live {
		p := d.Scheduled
		d.Predicted = &p
	}
	return d
}

func row(line transit.Line, headsign string, mins ...int) transit.Row {
	r := transit.Row{Line: line, Headsign: headsign}
	for i, m := range mins {
		r.Departures = append(r.Departures, dep(line, headsign, time.Duration(m)*time.Minute+10*time.Second, i == 0))
	}
	return r
}

var (
	azul  = transit.Line{ID: "azul", ShortName: "Azul", Color: "#0072CE", Mode: transit.ModeMetro}
	verde = transit.Line{ID: "verde", ShortName: "Verde", Color: "#00A651", Mode: transit.ModeMetro}
	bus   = transit.Line{ID: "735", ShortName: "735", Color: "#FFCC00", Mode: transit.ModeBus}
)

func TestTC002Golden(t *testing.T) {
	tests := map[string]struct {
		rows []transit.Row
		opts render.Options
	}{
		"departure_metro": {
			rows: []transit.Row{row(azul, "Reboleira", 3, 8, 14)},
		},
		"departure_bus_label_clip": {
			rows: []transit.Row{row(bus, "Hospital Santa Maria", 12, 27)},
			opts: render.Options{Overflow: render.OverflowClip, Labels: map[string]string{"Hospital Santa Maria": "Hosp. Sta Maria"}},
		},
		"rows_stable_order": {
			// The two soonest rows are shown, Azul above Verde although Verde
			// leaves first; Azul to Reboleira leaves last and is left out.
			rows: []transit.Row{row(verde, "Telheiras", 1, 6), row(azul, "Santa Apolónia", 4, 9), row(azul, "Reboleira", 7)},
			opts: render.Options{Layout: render.LayoutRows},
		},
		"rows_single": {
			rows: []transit.Row{row(bus, "Cacilhas", 0)},
			opts: render.Options{Layout: render.LayoutRows},
		},
		"empty": {},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(render.TC002(tt.rows, now, tt.opts), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create)", err)
			}
			if diff := cmp.Diff(string(want), string(got)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestTC002Geometry checks the rules AWTRIX NG enforces, for any input:
// every box inside the display, unique IDs, one kind of content per region,
// and the per-layout limits. It also checks that times are never cut off.
func TestTC002Geometry(t *testing.T) {
	long := transit.Line{ID: "x", ShortName: "VeryLongLineName", Mode: transit.ModeBus}
	many := row(long, "A destination that will never fit on the display", 1, 22, 45, 67, 88, 109)
	rows := []transit.Row{many, row(azul, "B", 2), row(verde, "C", 3)}
	for _, layout := range []string{render.LayoutDeparture, render.LayoutRows} {
		t.Run(layout, func(t *testing.T) {
			l := render.TC002(rows, now, render.Options{Layout: layout, MaxRows: 5})
			if len(l.Regions) > 16 {
				t.Errorf("%d regions, limit 16", len(l.Regions))
			}
			ids := map[string]bool{}
			for _, r := range l.Regions {
				if ids[r.ID] {
					t.Errorf("duplicate id %q", r.ID)
				}
				ids[r.ID] = true
				x, y, w, h := r.Box[0], r.Box[1], r.Box[2], r.Box[3]
				if x < 0 || y < 0 || w <= 0 || h <= 0 || x+w > awtrix.TC002Width || y+h > awtrix.TC002Height {
					t.Errorf("region %s box %v outside 52x16", r.ID, r.Box)
				}
				n := 0
				if r.Text != nil {
					n++
				}
				if r.Icon != "" {
					n++
				}
				if r.Draw != nil {
					n++
				}
				if n != 1 {
					t.Errorf("region %s has %d content kinds, want exactly 1", r.ID, n)
				}
				if r.Text != nil && r.Text.Fragments != nil {
					// The firmware measures fragments as one string, so
					// the gap after each number counts.
					var s strings.Builder
					for _, f := range r.Text.Fragments {
						s.WriteString(f.Text)
					}
					if w := render.TextWidth(s.String()); w > r.Box[2] {
						t.Errorf("region %s: times are %d px wide in a %d px box", r.ID, w, r.Box[2])
					}
				}
			}
		})
	}
}

func TestTextWidth(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"8", 3},
		{"18", 7},
		{"  ", 4},
		{"Cais do Sodré", 47},
		{"Santa Apolónia", 53},
	}
	for _, tt := range tests {
		if got := render.TextWidth(tt.s); got != tt.want {
			t.Errorf("TextWidth(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
	if render.Fits("Santa Apolónia") || !render.Fits("Sta Apolónia") {
		t.Error("Santa Apolónia should overflow 52 px and Sta Apolónia should fit")
	}
}

// An unfiltered stop served in both directions keeps one destination on
// the departure layout, whichever direction departs next.
func TestDepartureLayoutKeepsOneRow(t *testing.T) {
	for _, rows := range [][]transit.Row{
		{row(azul, "Reboleira", 2), row(azul, "Santa Apolónia", 5)},
		{row(azul, "Santa Apolónia", 1), row(azul, "Reboleira", 4)},
	} {
		got := render.Visible(rows, render.Options{})
		if len(got) != 1 || got[0].Headsign != "Reboleira" {
			t.Errorf("Visible(%s first) = %v, want only Reboleira", rows[0].Headsign, got)
		}
		l := render.TC002(rows, now, render.Options{})
		if text := l.Regions[0].Text.Plain; text != "Reboleira" {
			t.Errorf("sign = %q, want Reboleira", text)
		}
	}
}

func TestLongLineNameGetsBar(t *testing.T) {
	rail := transit.Line{ID: "CR-Worcester", ShortName: "Framingham/Worcester Line", Color: "#80276C", Mode: transit.ModeRail}
	l := render.TC002([]transit.Row{row(rail, "Worcester", 9)}, now, render.Options{})
	badge := l.Regions[1]
	if badge.Text != nil || len(badge.Draw) == 0 {
		t.Errorf("badge = %+v, want a color bar", badge)
	}
}
