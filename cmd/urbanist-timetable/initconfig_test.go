package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

func TestRenderConfig(t *testing.T) {
	sys, _ := catalog.Get("lisbon-metro")
	got, err := renderConfig(initAnswers{
		system: sys,
		stops:  []transit.Stop{{ID: "MP", Name: "Marquês de Pombal"}},
		lines:  []string{"Azul", `Q"uote`},
		board:  "marques",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `# Written by ` + "`urbanist-timetable config init`" + `.
# ` + "`urbanist-timetable config example`" + ` explains every setting.

# Metro de Lisboa, Lisbon. More: ` + sys.InfoURL + `
[providers.lisbon-metro]
system = "lisbon-metro"
client_id = "${LISBON_METRO_CLIENT_ID}"
client_secret = "${LISBON_METRO_CLIENT_SECRET}"

# ` + "`urbanist-timetable departures` shows every board; `departures marques`" + `, this one.
[[boards]]
name = "marques"

[[boards.stops]]
provider = "lisbon-metro"
stop = "MP"  # Marquês de Pombal
lines = ["Azul", "Q\"uote"]

# To show the boards on an AWTRIX NG clock, add its address and run
# ` + "`urbanist-timetable clock preview`, then `clock run`" + `:
# [clock]
# url = "http://192.168.1.50"
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("renderConfig mismatch (-want +got):\n%s", diff)
	}
}

func TestSlug(t *testing.T) {
	tests := map[string]string{
		"Times Sq-42 St":          "times-sq-42-st",
		"Marquês de Pombal":       "marques-de-pombal",
		"  ** ":                   "stop",
		"sf-bart-EMBR":            "sf-bart-embr",
		strings.Repeat("ab ", 20): "ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab",
	}
	for in, want := range tests {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigInitFromFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.toml")
	run := func(args ...string) (string, error) {
		cmd := newRootCommand()
		var out, errOut bytes.Buffer
		cmd.Writer, cmd.ErrWriter = &out, &errOut
		cmd.Reader = strings.NewReader("")
		err := cmd.Run(t.Context(), append([]string{"urbanist-timetable", "--config", path, "config", "init"}, args...))
		return out.String(), err
	}

	out, err := run("--system", "sf-bart", "--stop", "EMBR", "--lines", "Red-S")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Saved " + path + ".\nTry: urbanist-timetable departures\n"; out != want {
		t.Errorf("output %q, want %q", out, want)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Boards) != 1 || cfg.Boards[0].Name != "sf-bart-embr" || cfg.Boards[0].Stops[0].Stop != "EMBR" || cfg.Boards[0].Stops[0].Lines[0] != "Red-S" {
		t.Errorf("boards %+v", cfg.Boards)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, %v", fi.Mode(), err)
	}

	if _, err := run("--system", "sf-bart", "--stop", "MONT"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second init: err = %v, want a refusal", err)
	}
	if _, err := run("--system", "sf-bart", "--stop", "MONT", "--name", "work", "--force"); err != nil {
		t.Errorf("--force: %v", err)
	}
	if cfg, _ := config.Load(path); cfg == nil || cfg.Boards[0].Name != "work" {
		t.Errorf("--force did not replace the file")
	}

	out, err = run("--system", "lisbon-metro", "--stop", "MP", "--stdout")
	if err != nil || !strings.Contains(out, `client_id = "${LISBON_METRO_CLIENT_ID}"`) {
		t.Errorf("--stdout: %q, %v", out, err)
	}

	usage := map[string][]string{
		"no flags":       nil,
		"no stop":        {"--system", "sf-bart"},
		"unknown system": {"--system", "sf-brat", "--stop", "x"},
		"bad name":       {"--system", "sf-bart", "--stop", "x", "--name", "my board"},
	}
	for name, args := range usage {
		t.Run(name, func(t *testing.T) {
			if _, err := run(append(args, "--stdout")...); err == nil {
				t.Error("no error")
			}
		})
	}
}

// fakeSource searches a fixed list of stops and has one line at each.
type fakeSource struct{ stops []transit.Stop }

func (f fakeSource) SearchStops(_ context.Context, search string) ([]transit.Stop, error) {
	var out []transit.Stop
	for _, s := range f.stops {
		if strings.Contains(strings.ToLower(s.Name), strings.ToLower(search)) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f fakeSource) Departures(_ context.Context, q transit.Query) ([]transit.Departure, error) {
	return []transit.Departure{
		{Line: transit.Line{ID: "2", ShortName: "2"}, StopID: q.StopID, Scheduled: q.Now.Add(time.Minute)},
		{Line: transit.Line{ID: "1", ShortName: "1"}, StopID: q.StopID, Scheduled: q.Now.Add(2 * time.Minute)},
	}, nil
}

func TestPrompterAsksForEverything(t *testing.T) {
	// A wrong system number, a search with no match, an empty answer to
	// the stop list (search again), then choices.
	input := "99\nnyc-subway\nnowhere\ntimes\n\ntimes\n2\n1, 2\nmy board\n\n"
	var out bytes.Buffer
	var opened string
	p := prompter{
		in:  bufio.NewReader(strings.NewReader(input)),
		out: &out,
		open: func(name string) (transit.Provider, error) {
			opened = name
			return fakeSource{stops: []transit.Stop{{ID: "127", Name: "Times Sq-42 St"}, {ID: "R16", Name: "Times Sq-42 St"}}}, nil
		},
	}
	a, err := p.ask(t.Context())
	if err != nil {
		t.Fatalf("%v; prompts:\n%s", err, out.String())
	}
	if opened != "nyc-subway" || a.system.ID != "nyc-subway" {
		t.Errorf("opened %q, system %q", opened, a.system.ID)
	}
	want := initAnswers{system: a.system, stops: []transit.Stop{{ID: "R16", Name: "Times Sq-42 St"}}, lines: []string{"1", "2"}, board: "times-sq-42-st"}
	if diff := cmp.Diff(want, a, cmp.AllowUnexported(initAnswers{}), cmp.Comparer(func(a, b catalog.System) bool { return a.ID == b.ID })); diff != "" {
		t.Errorf("answers (-want +got):\n%s", diff)
	}
	for _, s := range []string{"Give a number from 1 to", "No stops found", "Lines here now: 1, 2.", "Use 1-32 letters"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("prompts lack %q:\n%s", s, out.String())
		}
	}
}

func TestPrompterWithoutKeysAsksForAStopID(t *testing.T) {
	t.Setenv("LISBON_METRO_CLIENT_ID", "")
	t.Setenv("LISBON_METRO_CLIENT_SECRET", "")
	var out bytes.Buffer
	p := prompter{
		in:  bufio.NewReader(strings.NewReader("lisbon-metro\nMP\n\nmp\n")),
		out: &out,
		open: func(string) (transit.Provider, error) {
			t.Error("opened a source that lacks its keys")
			return nil, errors.New("no")
		},
	}
	a, err := p.ask(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if a.stops[0].ID != "MP" || a.board != "mp" || !strings.Contains(out.String(), "needs LISBON_METRO_CLIENT_ID and LISBON_METRO_CLIENT_SECRET") {
		t.Errorf("answers %+v, prompts:\n%s", a, out.String())
	}
}

func TestPrompterStopsWhenInputEnds(t *testing.T) {
	p := prompter{in: bufio.NewReader(strings.NewReader("nyc-subway\n")), out: &bytes.Buffer{}, open: func(string) (transit.Provider, error) {
		return fakeSource{}, nil
	}}
	if _, err := p.ask(t.Context()); !errors.Is(err, errInputEnded) {
		t.Errorf("err = %v, want errInputEnded", err)
	}
}
