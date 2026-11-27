package render_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
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
	plain = transit.Line{ID: "x", ShortName: "15E", Mode: transit.ModeTram}
	// regional's four-character name is too wide to fill in a grid cell.
	regional = transit.Line{ID: "3703", ShortName: "3703", Color: "#C61D23", Mode: transit.ModeBus}
)

var piccadilly = transit.Line{ID: "piccadilly", ShortName: "Piccadilly", Color: "#0019A8", Mode: transit.ModeMetro}

// directed sets the direction of r and its departures, as transit.Group
// would.
func directed(r transit.Row, dir string) transit.Row {
	r.Direction = dir
	r.Departures = slices.Clone(r.Departures)
	for i := range r.Departures {
		r.Departures[i].Direction = dir
	}
	return r
}

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
		"grid_four": {
			rows: []transit.Row{row(bus, "", 5, 24), row(navy, "", 7), row(plain, "", 8), row(verde, "", 12)},
			opts: render.Options{Layout: render.LayoutGrid},
		},
		"grid_two": {
			rows: []transit.Row{row(bus, "", 5), row(navy, "", 7)},
			opts: render.Options{Layout: render.LayoutGrid},
		},
		"grid_four_char_badge": {
			rows: []transit.Row{row(regional, "Almada", 14)},
			opts: render.Options{Layout: render.LayoutGrid},
		},
		"rows_direction": {
			// Two destinations each way merge into one row per direction,
			// named after the next train.
			rows: []transit.Row{
				directed(row(piccadilly, "Heathrow T4", 2, 16), "Westbound"),
				directed(row(piccadilly, "Uxbridge", 3, 12), "Westbound"),
				directed(row(piccadilly, "Arnos Grove", 3), "Eastbound"),
				directed(row(piccadilly, "Cockfosters", 5, 6), "Eastbound"),
			},
			opts: render.Options{Layout: render.LayoutRows, Group: render.GroupDirection},
		},
		"rows_train": {
			rows: []transit.Row{
				directed(row(piccadilly, "Heathrow T4", 2, 16), "Westbound"),
				directed(row(piccadilly, "Uxbridge", 3, 12), "Westbound"),
				directed(row(piccadilly, "Rayners Lane", 6), "Westbound"),
			},
			opts: render.Options{Layout: render.LayoutRows, Group: render.GroupTrain},
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
	// Every cell has all four of its regions, which is the per-layout limit.
	var full []transit.Row
	for i, name := range []string{"732", "736", "744", "783", "727"} {
		full = append(full, row(transit.Line{ID: name, ShortName: name, Color: "#FFCC00", Mode: transit.ModeBus}, "", 20+i))
	}
	tests := map[string]struct {
		layout render.Layout
		rows   []transit.Row
	}{
		"departure": {render.LayoutDeparture, rows},
		"rows":      {render.LayoutRows, rows},
		"grid":      {render.LayoutGrid, rows},
		"full grid": {render.LayoutGrid, full},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			l := render.TC002(tt.rows, now, render.Options{Layout: tt.layout, MaxRows: 5})
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
}

// Widths were computed from the firmware's font data, by lit pixels.
func TestFontWidth(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"Malmö", 21},
		{"São Sebastião", 47},
		{"Santa Apolónia", 49},
		{"München Hbf", 44},
		{"München Hbf W", 52},
		{" Malmö ", 21},
		{"Mombasa doMonte", 63},
		// Beyond Latin-1, and glyphs that fill or leave their last column.
		{"Œ", 5},
		{"Ж", 5},
		{"—", 4},
		{"ą", 4},
		{"Í", 2},
	}
	for _, tt := range tests {
		if got := render.FontWidth(render.AccentFont, tt.s); got != tt.want {
			t.Errorf("FontWidth(matrix-light6, %q) = %d, want %d", tt.s, got, tt.want)
		}
	}
	// Other fonts keep the fixed 4 px per character.
	if got := render.FontWidth("small", "Mombasa doMonte"); got != render.TextWidth("Mombasa doMonte") {
		t.Errorf("FontWidth(small, ...) = %d, want %d", got, render.TextWidth("Mombasa doMonte"))
	}
}

func TestViews(t *testing.T) {
	type view struct {
		Name             string
		Shown, Overflows bool
	}
	tests := []struct {
		name string
		rows []transit.Row
		opts render.Options
		want []view
	}{
		{
			name: "departure overflows past the display width",
			rows: []transit.Row{row(azul, "Mombasa doMonte", 2)},
			want: []view{{"Mombasa doMonte", true, true}},
		},
		{
			// 50 px in matrix-light6's real glyphs, so it fits the 52 px row.
			name: "accented name is measured with its real glyphs",
			rows: []transit.Row{row(azul, "Santa Apolónia", 2)},
			want: []view{{"Santa Apolónia", true, false}},
		},
		{
			name: "label makes it fit",
			rows: []transit.Row{row(azul, "Mombasa doMonte", 2)},
			opts: render.Options{Labels: map[string]string{"Mombasa doMonte": "Mombasa"}},
			want: []view{{"Mombasa", true, false}},
		},
		{
			name: "departure hides all but one row",
			rows: []transit.Row{row(azul, "Santa Apolónia", 1), row(azul, "Reboleira", 4)},
			want: []view{{"Santa Apolónia", false, false}, {"Reboleira", true, false}},
		},
		{
			// Fits the full width in matrix-light6, but not the box beside the badge and times.
			name: "rows overflows its narrower box",
			rows: []transit.Row{row(azul, "Santa Apolónia", 2), row(azul, "Baixa", 3)},
			opts: render.Options{Layout: render.LayoutRows},
			want: []view{{"Santa Apolónia", true, true}, {"Baixa", true, false}},
		},
		{
			name: "grid lists each line once and never overflows",
			rows: []transit.Row{row(bus, "Hospital de Santa Maria", 1), row(navy, "Sé", 2), row(bus, "Cacilhas", 3), row(plain, "A", 4), row(verde, "B", 5), row(azul, "C", 6)},
			opts: render.Options{Layout: render.LayoutGrid},
			want: []view{{"", true, false}, {"Sé", true, false}, {"A", true, false}, {"B", true, false}, {"C", false, false}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []view
			for _, v := range render.Views(tt.rows, now, tt.opts) {
				got = append(got, view{v.Name, v.Shown, v.Overflows})
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Views mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Trips added in realtime can lack a headsign; they must not replace the
// named destination on the departure layout.
func TestRowsWithoutHeadsignComeLast(t *testing.T) {
	blank, named := row(azul, "", 1), row(azul, "Reboleira", 4)
	tests := map[string]struct {
		rows []transit.Row
		opts render.Options
		want []string
	}{
		"departure":            {rows: []transit.Row{blank, named}, want: []string{"Reboleira"}},
		"rows":                 {rows: []transit.Row{blank, named}, opts: render.Options{Layout: render.LayoutRows}, want: []string{"Reboleira", ""}},
		"earlier line":         {rows: []transit.Row{row(azul, "", 1), row(verde, "Telheiras", 4)}, want: []string{"Telheiras"}},
		"nothing else to show": {rows: []transit.Row{blank}, want: []string{""}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var got []string
			for _, r := range render.Visible(tt.rows, tt.opts) {
				got = append(got, r.Headsign)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("headsigns (-want +got):\n%s", diff)
			}
		})
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
		{"dark fill, light number", navy, []string{"rectFill", "760 " + render.TextColor}},
		{"no color, plain number", plain, []string{"15E " + render.TextColor}},
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

// A canceled trip never pushes the next one a rider can catch out of its
// row, even when the destination then needs the room.
func TestRowsLayoutKeepsFirstCatchableTrip(t *testing.T) {
	tests := []struct {
		name string
		row  transit.Row
		want []string
	}{
		{"canceled first", canceled(row(bus, "Cacilhas", 3, 12), 0), []string{"3", "12"}},
		{"canceled later", canceled(row(bus, "Cacilhas", 3, 12), 1), []string{"3"}},
		{"all canceled", canceled(canceled(row(bus, "Cacilhas", 3, 12), 0), 1), []string{"3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := render.TC002([]transit.Row{tt.row}, now, render.Options{Layout: render.LayoutRows})
			if diff := cmp.Diff(tt.want, texts(l, "r0-min")); diff != "" {
				t.Errorf("times (-want +got):\n%s", diff)
			}
		})
	}
}

// The marks are placed for the small font, so other fonts get none, and
// canceled trips, which would read as catchable without a strike, are left
// out.
func TestMarksOnlyWithSmallFont(t *testing.T) {
	rows := []transit.Row{canceled(row(bus, "Sé", 3, 8, 18), 1)}
	for _, layout := range []render.Layout{render.LayoutDeparture, render.LayoutRows} {
		t.Run(string(layout), func(t *testing.T) {
			l := render.TC002(rows, now, render.Options{Layout: layout, Font: "large"})
			minID := "min"
			if layout == render.LayoutRows {
				minID = "r0-min"
			}
			if diff := cmp.Diff([]string{"3", "18"}, texts(l, minID)); diff != "" {
				t.Errorf("times (-want +got):\n%s", diff)
			}
			for _, r := range l.Regions {
				if strings.HasSuffix(r.ID, "marks") {
					t.Errorf("region %q drawn with the large font", r.ID)
				}
			}
		})
	}
	if canceled := rows[0].Departures[1].Status; canceled != transit.StatusCanceled {
		t.Error("TC002 modified the caller's rows")
	}
}

// gridCells returns each cell's badge name and times, in region order. A
// cell without a name region is listed as "".
func gridCells(t *testing.T, rows []transit.Row) []string {
	t.Helper()
	l := render.TC002(rows, now, render.Options{Layout: render.LayoutGrid})
	var cells []string
	index := map[string]int{}
	for _, r := range l.Regions {
		cell, _, ok := strings.Cut(r.ID, "-")
		if !ok || !strings.HasPrefix(cell, "c") {
			t.Fatalf("region %q is not part of a cell", r.ID)
		}
		i, ok := index[cell]
		if !ok {
			i = len(cells)
			index[cell] = i
			cells = append(cells, "")
		}
		if r.Text == nil {
			continue
		}
		if r.Text.Fragments == nil {
			cells[i] += r.Text.Plain
		}
		for _, f := range r.Text.Fragments {
			cells[i] += " " + f.Text
		}
	}
	return cells
}

func TestGridCells(t *testing.T) {
	line := func(name string) transit.Line {
		return transit.Line{ID: name, ShortName: name, Mode: transit.ModeBus}
	}
	tests := []struct {
		name string
		rows []transit.Row
		want []string
	}{
		{
			"one next time per line",
			[]transit.Row{row(line("727"), "", 5, 16, 26)},
			[]string{"727 5"},
		},
		{
			"the soonest four, in line order",
			[]transit.Row{row(line("783"), "", 1), row(line("736"), "", 2), row(line("732"), "", 3), row(line("744"), "", 4), row(line("727"), "", 5)},
			[]string{"732 3", "736 2", "744 4", "783 1"},
		},
		{
			"a missing headsign does not move a line",
			[]transit.Row{row(line("727"), "", 1), row(line("738"), "Alto Sto. Amaro", 2)},
			[]string{"727 1", "738 2"},
		},
		{
			"destinations of one line share a cell",
			[]transit.Row{row(line("727"), "Restelo", 4), row(line("738"), "", 6), row(line("727"), "Belém", 9)},
			[]string{"727 4", "738 6"},
		},
		{
			"canceled first trip gives way",
			[]transit.Row{canceled(row(line("727"), "", 3, 12), 0)},
			[]string{"727 12"},
		},
		{
			"canceled across destinations gives way",
			[]transit.Row{canceled(row(line("727"), "Restelo", 3), 0), row(line("727"), "Belém", 9)},
			[]string{"727 9"},
		},
		{
			"all canceled keeps the first",
			[]transit.Row{canceled(canceled(row(line("727"), "", 3, 12), 0), 1)},
			[]string{"727 3"},
		},
		{
			"under a minute is a number",
			[]transit.Row{row(line("727"), "", 0)},
			[]string{"727 0"},
		},
		{
			"a time that does not fit is left out",
			[]transit.Row{row(line("3703"), "", 120)},
			[]string{"3703"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, gridCells(t, tt.rows)); diff != "" {
				t.Errorf("cells (-want +got):\n%s", diff)
			}
		})
	}
}

// A badge that depended on the minutes would change shape between pushes.
func TestGridBadgeIgnoresMinutes(t *testing.T) {
	badge := func(mins int) []awtrix.Region {
		l := render.TC002([]transit.Row{row(regional, "", mins)}, now, render.Options{Layout: render.LayoutGrid})
		var out []awtrix.Region
		for _, r := range l.Regions {
			if strings.HasPrefix(r.ID, "c0-badge") {
				out = append(out, r)
			}
		}
		return out
	}
	if diff := cmp.Diff(badge(10), badge(9)); diff != "" {
		t.Errorf("badge at 10 and 9 minutes (-10 +9):\n%s", diff)
	}
	if got := badge(9); len(got) != 1 || got[0].Text == nil || got[0].Text.Plain != "3703" || got[0].Color != "#C61D23" {
		t.Errorf("badge = %+v, want 3703 unfilled in its line color", got)
	}
}

func TestGridDoesNotModifyRows(t *testing.T) {
	rows := []transit.Row{row(bus, "A", 1, 9), row(bus, "B", 4)}
	render.TC002(rows, now, render.Options{Layout: render.LayoutGrid})
	if diff := cmp.Diff([]transit.Row{row(bus, "A", 1, 9), row(bus, "B", 4)}, rows); diff != "" {
		t.Errorf("rows changed (-want +got):\n%s", diff)
	}
}

func TestNameFont(t *testing.T) {
	tests := []struct {
		name, headsign string
		opts           render.Options
		font, valign   string
	}{
		{"plain", "Odivelas", render.Options{}, "small", ""},
		{"accent above", "São Sebastião", render.Options{}, render.AccentFont, ""},
		{"accented capital", "Águas Livres", render.Options{}, render.AccentFont, ""},
		// A cedilla hangs below the letter, so small has room for it.
		{"cedilla", "Praca do Comercio, Praça", render.Options{}, "small", ""},
		// Extended-A accents get a row of their own in small.
		{"extended-A", "Žižkov", render.Options{}, "small", ""},
		{"label decides", "São", render.Options{Labels: map[string]string{"São": "Sao"}}, "small", ""},
		{"name_font overrides", "Odivelas", render.Options{NameFont: "large"}, "large", ""},
		{"own font is kept", "São Sebastião", render.Options{Font: "large"}, "large", ""},
	}
	for _, tt := range tests {
		for _, layout := range []render.Layout{render.LayoutDeparture, render.LayoutRows} {
			t.Run(tt.name+" "+string(layout), func(t *testing.T) {
				opts := tt.opts
				opts.Layout = layout
				l := render.TC002([]transit.Row{row(azul, tt.headsign, 3, 8)}, now, opts)
				for _, r := range l.Regions {
					switch {
					case strings.HasSuffix(r.ID, "sign"):
						if r.Font != tt.font || r.VAlign != tt.valign {
							t.Errorf("name font %q valign %q, want %q %q", r.Font, r.VAlign, tt.font, tt.valign)
						}
					case strings.HasSuffix(r.ID, "min"):
						want := tt.opts.Font
						if want == "" {
							want = "small"
						}
						if r.Font != want {
							t.Errorf("times font %q, want %q", r.Font, want)
						}
					}
				}
			})
		}
	}
}

func TestEmptyTextUsesNameFont(t *testing.T) {
	l := render.TC002(nil, now, render.Options{EmptyText: "Não há"})
	if f := l.Regions[0].Font; f != render.AccentFont {
		t.Errorf("font %q, want %q", f, render.AccentFont)
	}
}

func TestGroups(t *testing.T) {
	westT4 := directed(row(piccadilly, "Heathrow T4", 2, 16), "Westbound")
	westUx := directed(row(piccadilly, "Uxbridge", 3, 12), "Westbound")
	eastAG := directed(row(piccadilly, "Arnos Grove", 4), "Eastbound")
	eastCf := directed(row(piccadilly, "Cockfosters", 5, 6), "Eastbound")
	type shown struct {
		Name string
		Mins []int
	}
	tests := map[string]struct {
		rows []transit.Row
		opts render.Options
		want []shown
	}{
		"destination keeps the soonest destinations": {
			rows: []transit.Row{westT4, westUx, eastAG, eastCf},
			opts: render.Options{Layout: render.LayoutRows},
			want: []shown{{"Heathrow T4", []int{2, 16}}, {"Uxbridge", []int{3, 12}}},
		},
		"destination orders by name, not direction": {
			rows: []transit.Row{directed(row(azul, "Zeta", 1), "0"), directed(row(azul, "Alpha", 2), "1")},
			want: []shown{{"Alpha", []int{2}}},
		},
		"direction shows each way, in a fixed order": {
			rows: []transit.Row{westT4, westUx, eastAG, eastCf},
			opts: render.Options{Layout: render.LayoutRows, Group: render.GroupDirection},
			want: []shown{{"Arnos Grove", []int{4, 5, 6}}, {"Heathrow T4", []int{2, 3, 12, 16}}},
		},
		"direction names the next trip a rider can catch": {
			rows: []transit.Row{canceled(directed(row(piccadilly, "Heathrow T4", 1), "Westbound"), 0), westUx},
			opts: render.Options{Layout: render.LayoutRows, Group: render.GroupDirection},
			want: []shown{{"Uxbridge", []int{1, 3, 12}}},
		},
		"direction on the departure layout": {
			rows: []transit.Row{westT4, westUx},
			opts: render.Options{Group: render.GroupDirection},
			want: []shown{{"Heathrow T4", []int{2, 3, 12, 16}}},
		},
		"rows without a direction stay by destination": {
			rows: []transit.Row{row(bus, "Cacilhas", 1), row(bus, "Sé", 2)},
			opts: render.Options{Layout: render.LayoutRows, Group: render.GroupDirection},
			want: []shown{{"Cacilhas", []int{1}}, {"Sé", []int{2}}},
		},
		"train shows the next trips in time order": {
			rows: []transit.Row{westT4, westUx, eastAG},
			opts: render.Options{Layout: render.LayoutRows, Group: render.GroupTrain},
			want: []shown{{"Heathrow T4", []int{2}}, {"Uxbridge", []int{3}}},
		},
		"train leaves out canceled trips": {
			rows: []transit.Row{canceled(directed(row(piccadilly, "Heathrow T4", 1, 9), "Westbound"), 0), westUx},
			opts: render.Options{Layout: render.LayoutRows, Group: render.GroupTrain},
			want: []shown{{"Uxbridge", []int{3}}, {"Heathrow T4", []int{9}}},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var got []shown
			for _, r := range render.Visible(tt.rows, tt.opts) {
				s := shown{Name: r.Headsign}
				for _, d := range r.Departures {
					s.Mins = append(s.Mins, d.MinutesUntil(now))
				}
				got = append(got, s)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("visible rows (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateGroup(t *testing.T) {
	tests := []struct {
		opts render.Options
		ok   bool
	}{
		{render.Options{Group: render.GroupDestination, Layout: render.LayoutGrid}, true},
		{render.Options{Group: render.GroupDirection}, true},
		{render.Options{Group: render.GroupDirection, Layout: render.LayoutRows}, true},
		{render.Options{Group: render.GroupTrain, Layout: render.LayoutRows}, true},
		{render.Options{Group: render.GroupTrain}, false},
		{render.Options{Group: render.GroupDirection, Layout: render.LayoutGrid}, false},
		{render.Options{Group: render.GroupTrain, Layout: render.LayoutGrid}, false},
		{render.Options{Group: "line"}, false},
	}
	for _, tt := range tests {
		if err := tt.opts.Validate(); (err == nil) != tt.ok {
			t.Errorf("Validate(%+v) = %v, want ok %v", tt.opts, err, tt.ok)
		}
	}
}
