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
	navy  = transit.Line{ID: "760", ShortName: "760", Color: "#1F3A93", Mode: transit.ModeBus}
)

func canceled(r transit.Row, i int) transit.Row {
	r.Departures[i].Status = transit.StatusCanceled
	r.Departures[i].Predicted = nil
	return r
}

func texts(l *awtrix.Layout, id string) []string {
	var out []string
	for _, r := range l.Regions {
		if r.ID == id && r.Text != nil {
			for _, f := range r.Text.Fragments {
				if strings.TrimSpace(f.Text) != "" {
					out = append(out, f.Text)
				}
			}
		}
	}
	return out
}

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
		"departure_due": {
			rows: []transit.Row{row(verde, "Cais do Sodré", 0, 5, 11)},
		},
		"rows_canceled": {
			rows: []transit.Row{row(bus, "Cacilhas", 0, 12), canceled(row(navy, "Sé", 3, 18), 0)},
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
	many := row(long, "A destination that will never fit on the display", 0, 22, 45, 67, 88, 109)
	for i := range many.Departures {
		p := many.Departures[i].Scheduled
		many.Departures[i].Predicted = &p
	}
	rows := []transit.Row{many, canceled(row(bus, "B", 1, 2, 3, 4), 0), row(verde, "C", 3)}
	for _, layout := range []string{render.LayoutDeparture, render.LayoutRows} {
		t.Run(layout, func(t *testing.T) {
			l := render.TC002(rows, now, render.Options{Layout: layout, MaxRows: 5})
			if len(l.Regions) > 16 {
				t.Errorf("%d regions, limit 16", len(l.Regions))
			}
			ids := map[string]bool{}
			scrolling := 0
			for _, r := range l.Regions {
				if r.Text != nil && (r.Scroll == nil || r.Scroll.Mode != awtrix.ScrollStatic) {
					scrolling++
				}
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
			if scrolling > 8 {
				t.Errorf("%d scrolling texts, limit 8", scrolling)
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

func TestBadges(t *testing.T) {
	plain := transit.Line{ID: "x", ShortName: "15E", Mode: transit.ModeTram}
	worcester := transit.Line{ID: "CR-Worcester", ShortName: "Framingham/Worcester Line", Color: "#80276C", Mode: transit.ModeRail}
	gondola := transit.Line{ID: "g", ShortName: "Telecabine Lisboa", Color: "#00A651", Mode: transit.ModeCableway}
	tests := []struct {
		name string
		line transit.Line
		// want is each badge region's content: the drawing's command name
		// or the text and its color.
		want []string
	}{
		{"metro gets a bar", azul, []string{"rectFill"}},
		{"bright fill, unlit number", bus, []string{"rectFill", "735 #000000"}},
		{"dark fill, light number", navy, []string{"rectFill", "760 " + render.DefaultTextColor}},
		{"no color, plain number", plain, []string{"15E " + render.DefaultTextColor}},
		{"long name, mode glyph", worcester, []string{"pixels"}},
		{"long name, no glyph for the mode", gondola, []string{"rectFill"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := render.TC002([]transit.Row{row(tt.line, "Somewhere", 9)}, now, render.Options{})
			var got []string
			for _, r := range l.Regions {
				if !strings.HasPrefix(r.ID, "badge") {
					continue
				}
				if r.Draw != nil {
					got = append(got, r.Draw[0][0].(string))
				} else {
					got = append(got, r.Text.Plain+" "+r.Color)
				}
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("badge (-want +got):\n%s", diff)
			}
		})
	}
}

// Due needs room the rows layout gives to the destination.
func TestDueOnlyOnDepartureLayout(t *testing.T) {
	rows := []transit.Row{row(bus, "Cacilhas", 0, 12)}
	if got := texts(render.TC002(rows, now, render.Options{}), "min"); got[0] != "Due" {
		t.Errorf("departure layout times = %v, want Due first", got)
	}
	if got := texts(render.TC002(rows, now, render.Options{Layout: render.LayoutRows}), "r0-min"); got[0] != "0" {
		t.Errorf("rows layout times = %v, want 0 first", got)
	}
}

// In the rows layout a second time is shown only if the destination still
// fits beside it.
func TestRowsLayoutPutsDestinationFirst(t *testing.T) {
	tests := []struct {
		name     string
		headsign string
		want     []string
	}{
		{"short name keeps both times", "Sé", []string{"4", "19"}},
		{"name that fits with one time", "Cacilhas", []string{"4"}},
		{"name too long either way", "Gomes Freire", []string{"4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := render.TC002([]transit.Row{row(bus, tt.headsign, 4, 19)}, now, render.Options{Layout: render.LayoutRows})
			if diff := cmp.Diff(tt.want, texts(l, "r0-min")); diff != "" {
				t.Errorf("times (-want +got):\n%s", diff)
			}
		})
	}
}
