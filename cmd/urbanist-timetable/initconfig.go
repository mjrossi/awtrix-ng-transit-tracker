package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/fold"
	"github.com/mjrossi/urbanist-timetable/internal/termout"
	"github.com/mjrossi/urbanist-timetable/internal/tomlout"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

// maxListed caps the stops a search lists, so a short search does not fill
// the screen.
const maxListed = 20

func initCommand() *cli.Command {
	return &cli.Command{
		Name:  "init",
		Usage: "create a configuration file with a system and a stop",
		Description: "At a terminal without flags, asks for a system, searches for your stop, and\n" +
			"saves it as a board that `departures` shows without arguments. With flags,\n" +
			"or when input is not a terminal, --system and --stop are required. Writes\n" +
			"to --config, and does not replace a file that exists unless --force.\n\n" +
			"   urbanist-timetable config init\n" +
			"   urbanist-timetable config init --system mta-subway --stop 127N --lines 1,2,3\n" +
			"   urbanist-timetable config init --system bart --stop EMBR --stdout",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "system", Usage: "a system ID from `systems`"},
			&cli.StringSliceFlag{Name: "stop", Usage: "a stop ID for the board"},
			&cli.StringSliceFlag{Name: "lines", Usage: "only these lines (IDs or short names)"},
			&cli.StringFlag{Name: "name", Usage: "the board's name (default: from the stop)"},
			&cli.BoolFlag{Name: "force", Usage: "replace a configuration file that exists"},
			&cli.BoolFlag{Name: "stdout", Usage: "print the configuration instead of writing it"},
		},
		Action: initConfig,
	}
}

// initAnswers is what a new configuration holds: one system, and one board
// of its stops.
type initAnswers struct {
	system catalog.System
	stops  []transit.Stop
	lines  []string
	board  string
}

func initConfig(ctx context.Context, c *cli.Command) error {
	path := c.String("config")
	toStdout := c.Bool("stdout")
	if !toStdout && !c.Bool("force") {
		if _, err := os.Stat(path); err == nil {
			return newUsageError(c, "%s already exists; edit it (`urbanist-timetable config example` explains every setting), or replace it with --force", path)
		}
	}

	var (
		a   initAnswers
		err error
	)
	if c.IsSet("system") || !isTerminal(c.Root().Reader) || !isTerminal(c.Root().ErrWriter) {
		a, err = initFromFlags(c)
	} else {
		p := prompter{
			in:  bufio.NewReader(c.Root().Reader),
			out: c.Root().ErrWriter,
			open: func(name string) (transit.Provider, error) {
				return openSource(ctx, path, name, newDirectory(c), providerOptions(c)...)
			},
		}
		a, err = p.ask(ctx)
	}
	if err != nil {
		return err
	}

	src, err := renderConfig(a)
	if err != nil {
		return err
	}
	if toStdout {
		_, err := io.WriteString(c.Root().Writer, src)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config init: %w", err)
	}
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		return fmt.Errorf("config init: %w", err)
	}
	w := c.Root().Writer
	fmt.Fprintf(w, "Saved %s.\n", path)
	if missing := missingEnv(a.system); len(missing) > 0 {
		fmt.Fprintf(w, "Set %s in the environment, then try: urbanist-timetable departures\n", strings.Join(missing, " and "))
	} else {
		fmt.Fprintln(w, "Try: urbanist-timetable departures")
	}
	return nil
}

func initFromFlags(c *cli.Command) (initAnswers, error) {
	id, stops := c.String("system"), c.StringSlice("stop")
	if id == "" || len(stops) == 0 {
		return initAnswers{}, newUsageError(c, "give --system and --stop, such as `config init --system mta-subway --stop 127N`; find stop IDs with `stops search`")
	}
	sys, ok := catalog.Get(id)
	if !ok {
		return initAnswers{}, unknownSystemError(id)
	}
	a := initAnswers{system: sys, lines: c.StringSlice("lines"), board: c.String("name")}
	for _, s := range dedupe(stops) {
		a.stops = append(a.stops, transit.Stop{ID: s})
	}
	if a.board == "" {
		a.board = slug(sys.ID + "-" + stops[0])
	}
	if !awtrix.ValidAppName(a.board) {
		return initAnswers{}, newUsageError(c, "board name %q must be 1-32 letters, digits, - or _", a.board)
	}
	return a, nil
}

// missingEnv lists the environment variables sys needs that are not set.
func missingEnv(sys catalog.System) []string {
	missing := sys.Missing(envSettings(sys))
	for i, k := range missing {
		missing[i] = sys.EnvName(k)
	}
	return missing
}

// errInputEnded is returned when the person stops answering, such as with
// Ctrl-D.
var errInputEnded = errors.New("config init: input ended before the configuration was complete")

// prompter asks for the answers at a terminal. open builds a source by
// name, so tests can give a fake.
type prompter struct {
	in   *bufio.Reader
	out  io.Writer
	open func(name string) (transit.Provider, error)
}

func (p prompter) ask(ctx context.Context) (initAnswers, error) {
	sys, err := p.askSystem(ctx)
	if err != nil {
		return initAnswers{}, err
	}
	a := initAnswers{system: sys}

	var prov transit.Provider
	if missing := missingEnv(sys); len(missing) > 0 {
		fmt.Fprintf(p.out, "\n%s needs %s. The file will read them from the environment; set them to search for stops.\n", sys.ID, strings.Join(missing, " and "))
	} else if prov, err = p.open(sys.ID); err != nil {
		fmt.Fprintf(p.out, "\nCould not open %s: %s\n", sys.ID, oneLine(err))
	}

	stop, err := p.askStop(ctx, prov)
	if err != nil {
		return initAnswers{}, err
	}
	a.stops = []transit.Stop{stop}

	if a.lines, err = p.askLines(ctx, prov, stop.ID); err != nil {
		return initAnswers{}, err
	}
	if a.board, err = p.askBoard(ctx, slug(cmp.Or(stop.Name, stop.ID))); err != nil {
		return initAnswers{}, err
	}
	return a, nil
}

func (p prompter) askSystem(ctx context.Context) (catalog.System, error) {
	all, err := catalog.All()
	if err != nil {
		return catalog.System{}, err
	}
	fmt.Fprintln(p.out, "Systems:")
	for i, s := range all {
		fmt.Fprintf(p.out, "  %d  %-28s %s, %s\n", i+1, s.ID, s.Title(), s.City)
	}
	fmt.Fprintln(p.out, "Not listed? `urbanist-timetable config example` shows how to add any agency with GTFS-Realtime, or Transitland.")
	for {
		answer, err := p.line(ctx, fmt.Sprintf("System [1-%d]: ", len(all)))
		if err != nil {
			return catalog.System{}, err
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(all) {
			return all[n-1], nil
		}
		if s, ok := catalog.Get(answer); ok {
			return s, nil
		}
		fmt.Fprintf(p.out, "Give a number from 1 to %d, or a system ID.\n", len(all))
	}
}

// askStop searches for a stop where prov can search, and otherwise asks for
// its ID.
func (p prompter) askStop(ctx context.Context, prov transit.Provider) (transit.Stop, error) {
	searcher, ok := prov.(transit.StopSearcher)
	if !ok {
		id, err := p.required(ctx, "\nStop ID (from the agency's site or app): ")
		return transit.Stop{ID: id}, err
	}
	for {
		words, err := p.required(ctx, "\nSearch for your stop (name or ID): ")
		if err != nil {
			return transit.Stop{}, err
		}
		stops, err := searcher.SearchStops(ctx, words)
		if ctx.Err() != nil {
			return transit.Stop{}, ctx.Err()
		}
		if err != nil {
			fmt.Fprintf(p.out, "Search failed: %s\n", oneLine(err))
			continue
		}
		if len(stops) == 0 {
			fmt.Fprintln(p.out, "No stops found; try fewer or other words.")
			continue
		}
		shown := stops[:min(len(stops), maxListed)]
		termout.Stops(p.out, shown, true)
		if len(stops) > len(shown) {
			fmt.Fprintf(p.out, "  … and %d more; search with more words to narrow them.\n", len(stops)-len(shown))
		}
		answer, err := p.line(ctx, fmt.Sprintf("Stop [1-%d], or Enter to search again: ", len(shown)))
		if err != nil {
			return transit.Stop{}, err
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(shown) {
			return shown[n-1], nil
		}
		if i := slices.IndexFunc(stops, func(s transit.Stop) bool { return s.ID == answer }); i >= 0 {
			return stops[i], nil
		}
	}
}

// askLines offers to keep some lines, naming the ones the stop has now if
// a fetch finds them.
func (p prompter) askLines(ctx context.Context, prov transit.Provider, stop string) ([]string, error) {
	fmt.Fprintln(p.out)
	if prov != nil {
		// A failed fetch only loses the hint, so its error is ignored.
		deps, _ := prov.Departures(ctx, transit.Query{StopID: stop, Now: clock(), Window: config.DefaultWindow})
		if names := lineNames(deps); len(names) > 0 {
			fmt.Fprintf(p.out, "Lines here now: %s.\n", strings.Join(names, ", "))
		}
	}
	answer, err := p.line(ctx, "Only some lines? Comma-separated, or Enter for all: ")
	if err != nil {
		return nil, err
	}
	var lines []string
	for l := range strings.SplitSeq(answer, ",") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

func (p prompter) askBoard(ctx context.Context, suggested string) (string, error) {
	for {
		answer, err := p.line(ctx, fmt.Sprintf("\nBoard name [%s]: ", suggested))
		if err != nil {
			return "", err
		}
		if answer == "" {
			answer = suggested
		}
		if awtrix.ValidAppName(answer) {
			return answer, nil
		}
		fmt.Fprintln(p.out, "Use 1-32 letters, digits, - or _.")
	}
}

// line prints prompt and returns the trimmed answer, or ctx's error when it
// is canceled first.
//
// main catches Ctrl-C to cancel ctx, and a blocked read cannot see that, so
// the read runs in a goroutine. On cancel that goroutine is left blocked
// until input arrives or the process exits; no later call starts another,
// because a canceled ctx stays canceled, so it never shares p.in.
func (p prompter) line(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fmt.Fprint(p.out, prompt)
	type result struct {
		s   string
		err error
	}
	read := make(chan result, 1)
	go func() {
		s, err := p.in.ReadString('\n')
		read <- result{s, err}
	}()
	select {
	case <-ctx.Done():
		fmt.Fprintln(p.out)
		return "", ctx.Err()
	case r := <-read:
		if r.err != nil && (r.s == "" || !errors.Is(r.err, io.EOF)) {
			fmt.Fprintln(p.out)
			return "", errInputEnded
		}
		return strings.TrimSpace(r.s), nil
	}
}

// required asks until the answer is not empty.
func (p prompter) required(ctx context.Context, prompt string) (string, error) {
	for {
		s, err := p.line(ctx, prompt)
		if err != nil || s != "" {
			return s, err
		}
	}
}

// renderConfig writes the answers as a commented configuration file, and
// checks that it loads.
func renderConfig(a initAnswers) (string, error) {
	var b strings.Builder
	b.WriteString("# Written by `urbanist-timetable config init`.\n")
	b.WriteString("# `urbanist-timetable config example` explains every setting.\n\n")
	fmt.Fprintf(&b, "# %s, %s. More: %s\n", a.system.Title(), a.system.City, a.system.InfoURL)
	b.WriteString(providerBlock(a.system))
	fmt.Fprintf(&b, "\n# `urbanist-timetable departures` shows every board; `departures %s`, this one.\n", a.board)
	b.WriteString("[[boards]]\n")
	fmt.Fprintf(&b, "name = %s\n", tomlout.String(a.board))
	for _, s := range a.stops {
		b.WriteString("\n[[boards.stops]]\n")
		fmt.Fprintf(&b, "provider = %s\n", tomlout.String(a.system.ID))
		fmt.Fprintf(&b, "stop = %s", tomlout.String(s.ID))
		if name := termout.Clean(s.Name); name != "" {
			fmt.Fprintf(&b, "  # %s", name)
		}
		b.WriteString("\n")
		if len(a.lines) > 0 {
			quoted := make([]string, len(a.lines))
			for i, l := range a.lines {
				quoted[i] = tomlout.String(l)
			}
			fmt.Fprintf(&b, "lines = [%s]\n", strings.Join(quoted, ", "))
		}
	}
	b.WriteString("\n# To show the boards on an AWTRIX NG clock, add its address and run\n")
	b.WriteString("# `urbanist-timetable clock preview`, then `clock run`:\n")
	b.WriteString("# [clock]\n")
	b.WriteString("# url = \"http://192.168.1.50\"\n")

	src := b.String()
	anyValue := func(string) (string, bool) { return "x", true }
	if _, err := config.Parse([]byte(src), anyValue); err != nil {
		return "", fmt.Errorf("config init made an invalid file: %w", err)
	}
	return src, nil
}

// providerBlock is a [providers] block for sys, reading the settings it
// requires from the environment.
func providerBlock(sys catalog.System) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[providers.%s]\nsystem = %s\n", sys.ID, tomlout.String(sys.ID))
	for _, k := range sys.RequiredOptions {
		fmt.Fprintf(&b, "%s = %s\n", k, tomlout.String("${"+sys.EnvName(k)+"}"))
	}
	return b.String()
}

// slug turns s into a board name: lowercase letters, digits and dashes, at
// most 32 characters.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range fold.String(s) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 32 {
		out = strings.TrimRight(out[:32], "-")
	}
	if out == "" {
		return "stop"
	}
	return out
}
