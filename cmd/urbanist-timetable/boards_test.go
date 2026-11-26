package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// boardsConfig has two boards and no clock: boards serve the terminal too.
const boardsConfig = `[providers.demo]
[[providers.demo.lines]]
id = "azul"
headsign = "Reboleira"
headway = "6m"
[[providers.demo.lines]]
id = "verde"
headsign = "Telheiras"
headway = "7m"

[[boards]]
name = "blue"
[[boards.stops]]
provider = "demo"
stop = "s1"
lines = ["azul"]

[[boards]]
name = "green"
per_row = 1
[[boards.stops]]
provider = "demo"
stop = "s2"
lines = ["verde"]
`

func TestDeparturesShowsSavedBoards(t *testing.T) {
	out, _, err := runCLIOut(t, boardsConfig, "departures")
	if err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(out, "\n\n")
	if len(blocks) != 2 || !strings.HasPrefix(blocks[0], "blue: s1 ") || !strings.HasPrefix(blocks[1], "green: s2 ") {
		t.Fatalf("output %q, want the blue then the green board", out)
	}
	if strings.Contains(blocks[0], "Telheiras") || !strings.Contains(blocks[0], "Reboleira") {
		t.Errorf("the blue board ignored its lines filter: %q", blocks[0])
	}
	// per_row = 1 leaves one time after the destination.
	for _, l := range strings.Split(strings.TrimSpace(blocks[1]), "\n")[1:] {
		if strings.HasPrefix(l, "* ") {
			continue
		}
		if f := strings.Fields(l); len(f) != 3 {
			t.Errorf("green row %q, want one departure", l)
		}
	}
}

func TestDeparturesShowsOneBoardByName(t *testing.T) {
	out, _, err := runCLIOut(t, boardsConfig, "departures", "green")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "green: s2 ") || strings.Contains(out, "blue") {
		t.Errorf("output %q, want only the green board", out)
	}
}

func TestDeparturesFlagsOverrideBoardSettings(t *testing.T) {
	out, _, err := runCLIOut(t, boardsConfig, "departures", "--lines", "verde", "blue")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Telheiras") || strings.Contains(out, "Reboleira") {
		t.Errorf("output %q, want --lines in place of the board's lines", out)
	}
	_, stderr, err := runCLIOut(t, boardsConfig, "departures", "--lines", "x", "blue")
	if err != nil || !strings.HasPrefix(stderr, "note: board blue: the filters hid ") {
		t.Errorf("stderr %q, err %v", stderr, err)
	}
}

func TestDeparturesBoardsJSON(t *testing.T) {
	out, _, err := runCLIOut(t, boardsConfig, "departures", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var boards []struct {
		Board, Source string
		Departures    []any
	}
	if err := json.Unmarshal([]byte(out), &boards); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if len(boards) != 2 || boards[0].Board != "blue" || boards[1].Board != "green" || boards[0].Source != "demo" || len(boards[0].Departures) == 0 {
		t.Errorf("boards %+v", boards)
	}
}

func TestDeparturesKeepsTheBoardsThatWork(t *testing.T) {
	src := carrisServer(t) + strings.Replace(boardsConfig, "[[boards]]\nname = \"green\"", "[[boards]]\nname = \"broken\"\n[[boards.stops]]\nprovider = \"cm\"\nstop = \"999999\"\n\n[[boards]]\nname = \"green\"", 1)
	out, stderr, err := runCLIOut(t, src, "departures")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "blue: ") || !strings.Contains(out, "green: ") || strings.Contains(out, "broken") {
		t.Errorf("output %q", out)
	}
	if !strings.HasPrefix(stderr, "warning: board broken: stop 999999: ") {
		t.Errorf("stderr %q", stderr)
	}
}

func TestDeparturesWithoutStops(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.toml")
	tests := map[string]struct {
		src  string
		args []string
		want string
	}{
		"no boards":       {"[providers.demo]\n", []string{"departures"}, "no saved boards in "},
		"a source":        {boardsConfig, []string{"departures", "demo"}, "give one or more stop IDs after demo; find them with `urbanist-timetable stops search demo <words>`"},
		"a system":        {boardsConfig, []string{"departures", "sf-bart"}, "after sf-bart"},
		"a board typo":    {boardsConfig, []string{"departures", "gren"}, `no board named "gren" in `},
		"typo suggestion": {boardsConfig, []string{"departures", "gren"}, `did you mean "green"?`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := runCLIOut(t, tt.src, tt.args...)
			var usage *usageError
			if !errors.As(err, &usage) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want a usage error with %q", err, tt.want)
			}
		})
	}
	t.Run("no file", func(t *testing.T) {
		t.Setenv("TIMETABLE_CONFIG", missing)
		cmd := newRootCommand()
		err := cmd.Run(t.Context(), []string{"urbanist-timetable", "departures"})
		if err == nil || !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "config init") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestDeparturesReportsEachFailureOnce(t *testing.T) {
	src := carrisServer(t) + `
[[boards]]
name = "one"
[[boards.stops]]
provider = "cm"
stop = "999998"

[[boards]]
name = "two"
[[boards.stops]]
provider = "cm"
stop = "999999"
`
	_, stderr, err := runCLIOut(t, src, "departures")
	if err == nil || !strings.Contains(err.Error(), "board one: ") || !strings.Contains(err.Error(), "board two: ") {
		t.Errorf("err = %v, want both boards named", err)
	}
	if stderr != "" {
		t.Errorf("stderr %q repeats the error", stderr)
	}
}
