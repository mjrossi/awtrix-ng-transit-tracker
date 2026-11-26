package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestPrintErrorPutsJoinedErrorsOnTheirOwnLines(t *testing.T) {
	tests := map[string]struct {
		err  error
		want string
	}{
		"one":    {errors.New("boom"), "error: boom\n"},
		"joined": {errors.Join(errors.New("stop a: unknown"), errors.New("stop b: unknown")), "error:\n  stop a: unknown\n  stop b: unknown\n"},
		"control characters": {
			errors.New("bad \x1b[2Jreply"), "error: bad [2Jreply\n",
		},
		"with a hint": {
			&usageError{msg: "no", command: "urbanist-timetable departures"},
			"error: no\nhint: run `urbanist-timetable departures --help`\n",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printError(&buf, tt.err)
			if diff := cmp.Diff(tt.want, buf.String()); diff != "" {
				t.Errorf("printError mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClosest(t *testing.T) {
	names := []string{"departures", "stops", "systems", "config", "clock"}
	tests := map[string]string{
		"departuers": "departures",
		"sytems":     "systems",
		"Clock":      "clock",
		"weather":    "",
		"":           "",
	}
	for in, want := range tests {
		if got := closest(in, names); got != want {
			t.Errorf("closest(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMistakesGetAHint runs commands with common mistakes and checks the
// error and the hint printed with it.
func TestMistakesGetAHint(t *testing.T) {
	tests := map[string]struct {
		args      []string
		wantErr   string
		wantHint  string
		usageOnly bool
	}{
		"unknown command": {
			args: []string{"departuers"}, wantErr: `unknown command "departuers"; did you mean "departures"?`,
			wantHint: "run `urbanist-timetable --help`",
		},
		"unknown subcommand": {
			args: []string{"stops", "serch"}, wantErr: `did you mean "search"?`,
			wantHint: "run `urbanist-timetable stops --help`",
		},
		"system without show": {
			args: []string{"systems", "sf-bart"}, wantErr: "did you mean `systems show sf-bart`?",
		},
		"bad flag value": {
			args: []string{"departures", "--window", "1x", "demo", "s1"}, wantErr: `invalid value "1x" for --window:`,
			wantHint: "run `urbanist-timetable departures --help`",
		},
		"unknown flag": {
			args: []string{"departures", "--line", "1", "demo", "s1"}, wantErr: "unknown flag --line; did you mean --lines?",
		},
		"show without an ID": {
			args: []string{"systems", "show"}, wantErr: "give one system ID",
			wantHint: "run `urbanist-timetable systems show --help`",
		},
		"show a typo": {
			args: []string{"systems", "show", "sf-brat"}, wantErr: `unknown system "sf-brat"; did you mean "sf-bart"?`,
			wantHint: "run `urbanist-timetable systems`",
		},
		"search without words": {
			args: []string{"stops", "search", "demo"}, wantErr: "stops search nyc-subway times sq",
		},
		"unknown source": {
			args: []string{"departures", "sf-brat", "EMBR"}, wantErr: `unknown provider "sf-brat"`,
			wantHint: `did you mean "sf-bart"?`,
		},
		"source that needs settings": {
			args: []string{"departures", "transitland", "s1"}, wantErr: "api_key is required",
			wantHint: "transitland needs settings: add a [providers.transitland] block to ",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := runCLIOut(t, "", tt.args...)
			if err == nil {
				t.Fatal("no error")
			}
			var buf bytes.Buffer
			printError(&buf, err)
			if !strings.Contains(buf.String(), tt.wantErr) {
				t.Errorf("printed %q, want %q", buf.String(), tt.wantErr)
			}
			if !strings.Contains(hint(err), tt.wantHint) {
				t.Errorf("hint %q, want %q", hint(err), tt.wantHint)
			}
		})
	}
}

func TestNoArgumentsPrintsHelp(t *testing.T) {
	out, _, err := runCLIOut(t, "")
	if err != nil || !strings.Contains(out, "COMMANDS:") {
		t.Errorf("err = %v, output %q", err, out)
	}
}

func TestExplicitConfigMustExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.toml")
	run := func() error {
		cmd := newRootCommand()
		var out bytes.Buffer
		cmd.Writer, cmd.ErrWriter = &out, &out
		return cmd.Run(t.Context(), []string{"urbanist-timetable", "--config", missing, "stops", "search", "demo", "x"})
	}
	var nc *noConfigError
	if err := run(); !errors.As(err, &nc) {
		t.Errorf("--config naming a missing file: err = %v, want noConfigError", err)
	}

	// The Docker image names a default through the environment, which need
	// not exist.
	t.Setenv("TIMETABLE_CONFIG", missing)
	if err := run(); errors.As(err, &nc) {
		t.Errorf("TIMETABLE_CONFIG naming a missing file: err = %v", err)
	}
}

func TestCommandsNeedingAConfigSayHowToMakeOne(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.toml")
	for _, args := range [][]string{{"config", "validate"}, {"clock", "preview"}} {
		cmd := newRootCommand()
		var out bytes.Buffer
		cmd.Writer, cmd.ErrWriter = &out, &out
		err := cmd.Run(t.Context(), append([]string{"urbanist-timetable", "--config", missing}, args...))
		if want := "no configuration file at " + missing; err == nil || err.Error() != want {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
		if !strings.Contains(hint(err), "config init") {
			t.Errorf("%v: hint %q", args, hint(err))
		}
	}
}

func TestDeparturesSaysWhatTheFiltersHid(t *testing.T) {
	_, stderr, err := runCLIOut(t, demoConfig, "departures", "--lines", "x", "demo", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stderr, "note: the filters hid ") || !strings.HasSuffix(stderr, "lines here: azul\n") {
		t.Errorf("stderr %q", stderr)
	}
	if _, stderr, _ := runCLIOut(t, demoConfig, "departures", "--json", "--lines", "x", "demo", "s1"); stderr != "" {
		t.Errorf("--json printed %q", stderr)
	}
}

func TestCompletion(t *testing.T) {
	src := demoConfig + "[providers.cm]\nsystem = \"lisbon-carris-metropolitana\"\n"
	tests := map[string]struct {
		args    []string
		want    []string
		notWant []string
	}{
		"departures": {[]string{"departures"}, []string{"metro", "demo", "cm", "sf-bart", "gtfsrt"}, nil},
		"search":     {[]string{"stops", "search"}, []string{"demo", "sf-bart"}, []string{"metro"}},
		"show":       {[]string{"systems", "show"}, []string{"sf-bart"}, []string{"demo"}},
		"stop":       {[]string{"departures", "demo"}, nil, []string{"demo", "help"}},
		"board flag": {[]string{"clock", "preview", "--board"}, []string{"metro"}, []string{"demo"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			out, _, err := runCLIOut(t, src, append(tt.args, "--generate-shell-completion")...)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(out, "\n")
			if len(dedupe(lines)) != len(lines) {
				t.Errorf("completions repeat a name: %q", lines)
			}
			for _, w := range tt.want {
				if !slices.Contains(lines, w) {
					t.Errorf("completions %q lack %q", lines, w)
				}
			}
			for _, w := range tt.notWant {
				if slices.Contains(lines, w) {
					t.Errorf("completions %q include %q", lines, w)
				}
			}
		})
	}
}

func TestConfigExample(t *testing.T) {
	out, _, err := runCLIOut(t, "", "config", "example")
	if err != nil || !strings.Contains(out, "[[boards]]") {
		t.Errorf("err = %v, output starts %.80q", err, out)
	}
}
