package termout_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/internal/termout"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

var update = flag.Bool("update", false, "rewrite golden files")

var at = time.Date(2026, 11, 25, 22, 31, 0, 0, time.FixedZone("EST", -5*3600))

var (
	one   = transit.Line{ID: "1", ShortName: "1", Color: "#D82233", Mode: transit.ModeMetro}
	two   = transit.Line{ID: "2", ShortName: "2", Color: "#D82233", Mode: transit.ModeMetro}
	three = transit.Line{ID: "3", ShortName: "3", Color: "#D82233", Mode: transit.ModeMetro}
	bus   = transit.Line{ID: "735", ShortName: "735", Mode: transit.ModeBus}
)

// dep departs in m minutes. Live departures carry a prediction.
func dep(l transit.Line, headsign string, m int, live bool) transit.Departure {
	t := at.Add(time.Duration(m) * time.Minute)
	d := transit.Departure{Line: l, Headsign: headsign, StopID: "127N", Scheduled: t, Status: transit.StatusScheduled}
	if live {
		d.Predicted = &t
	}
	return d
}

func board(deps ...transit.Departure) termout.Board {
	return termout.Board{Source: "mta-subway", Title: "Times Sq-42 St", At: at, Window: 90 * time.Minute, Departures: deps}
}

var live = board(
	dep(one, "Van Cortlandt Park-242 St", 2, true), dep(two, "Wakefield-241 St", 4, true),
	dep(three, "Harlem-148 St", 6, true), dep(one, "Van Cortlandt Park-242 St", 9, true),
	dep(two, "Wakefield-241 St", 14, true), dep(one, "Van Cortlandt Park-242 St", 16, true),
	dep(three, "Harlem-148 St", 18, true),
)

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", name, diff)
	}
}

func TestText(t *testing.T) {
	canceled := dep(bus, "Cais do Sodré", 12, false)
	canceled.Status = transit.StatusCanceled
	tests := map[string]struct {
		b    termout.Board
		opts termout.Options
	}{
		"live":          {live, termout.Options{PerRow: 3}},
		"schedule_only": {board(dep(bus, "Cais do Sodré", 3, false), dep(bus, "Cais do Sodré", 15, true)), termout.Options{}},
		"canceled":      {board(dep(bus, "Cais do Sodré", 3, true), canceled), termout.Options{}},
		"color":         {board(dep(one, "Van Cortlandt Park-242 St", 2, true), dep(bus, "Cais do Sodré", 5, true)), termout.Options{Color: true}},
		"limit":         {live, termout.Options{Limit: 1, PerRow: 2}},
		"accents":       {board(dep(bus, "São Sebastião", 1, true), dep(bus, "Reboleira", 4, true)), termout.Options{}},
		"empty":         {board(), termout.Options{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := termout.Text(&out, tt.b, tt.opts); err != nil {
				t.Fatal(err)
			}
			golden(t, name, out.Bytes())
		})
	}
}

func TestJSON(t *testing.T) {
	b := board(dep(bus, "Cais do Sodré", 3, false), dep(one, "Van Cortlandt Park-242 St", 2, true))
	b.Partial = true
	var out bytes.Buffer
	if err := termout.JSON(&out, b); err != nil {
		t.Fatal(err)
	}
	golden(t, "json", out.Bytes())

	out.Reset()
	if err := termout.JSON(&out, board()); err != nil {
		t.Fatal(err)
	}
	var v struct{ Departures []any }
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.Departures == nil {
		t.Errorf("empty board: %s, %v; want departures to be []", out.String(), err)
	}
}

func TestControlCharactersAreRemoved(t *testing.T) {
	b := termout.Board{Title: "A\x1b[2J", At: at, Window: time.Hour, Departures: []transit.Departure{{
		Line: transit.Line{ID: "1", ShortName: "1\n"}, Headsign: "B\x1b]0;x\x07\u009bC", Scheduled: at.Add(time.Minute), Status: transit.StatusScheduled,
	}}}
	var out bytes.Buffer
	if err := termout.Text(&out, b, termout.Options{}); err != nil {
		t.Fatal(err)
	}
	for _, r := range out.String() {
		if unicode.IsControl(r) && r != '\n' {
			t.Errorf("control character %U in %q", r, out.String())
		}
	}
}

func TestCleanRemovesControlAndFormatCharacters(t *testing.T) {
	if got := termout.Clean("a\x1b]0;x\x07b\u202ec\u009bd\ne"); got != "a]0;xbcde" {
		t.Errorf("Clean = %q", got)
	}
}

func TestPartialBoardSaysSo(t *testing.T) {
	b := board(dep(bus, "Cais do Sodré", 3, true))
	b.Partial = true
	var out bytes.Buffer
	if err := termout.Text(&out, b, termout.Options{}); err != nil {
		t.Fatal(err)
	}
	golden(t, "partial", out.Bytes())
	out.Reset()
	empty := board()
	empty.Partial = true
	if err := termout.Text(&out, empty, termout.Options{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Some data could not be loaded")) {
		t.Errorf("empty partial board: %q", out.String())
	}
}

func TestTable(t *testing.T) {
	var b bytes.Buffer
	termout.Table(&b, [][]string{{"mdb-53", "BART", "realtime"}, {"tld-716", "Metro de Lisboa\a", "schedule"}, {"x", "", ""}})
	want := "mdb-53   BART             realtime\ntld-716  Metro de Lisboa  schedule\nx\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}

func TestStopsWithLines(t *testing.T) {
	var b bytes.Buffer
	stops := []transit.Stop{{ID: "431", Name: "3rd Ave & Pike St"}, {ID: "433", Name: "3rd Ave & Pike St"}, {ID: "9", Name: "Pike Place"}}
	var many []transit.Line
	for _, n := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"} {
		many = append(many, transit.Line{ID: n, ShortName: n})
	}
	termout.Stops(&b, stops, true, map[string][]transit.Line{
		"431": {{ID: "r-c", ShortName: "C"}, {ID: "r-e", ShortName: "E"}},
		"433": many,
	})
	want := "  1  431  3rd Ave & Pike St  C, E\n" +
		"  2  433  3rd Ave & Pike St  1, 2, 3, 4, 5, 6, 7, 8 and 2 more\n" +
		"  3  9    Pike Place\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}
