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

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/directory"
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
		Description: "At a terminal without flags, asks for an agency or city, searches\n" +
			"for your stop, and saves it as a board that `departures` shows without\n" +
			"arguments. With flags, or when input is not a terminal, --system and\n" +
			"--stop are required. Writes to --config, and does not replace a file that\n" +
			"exists unless --force.\n\n" +
			"   urbanist-timetable config init\n" +
			"   urbanist-timetable config init --system mta-subway --stop 127N --lines 1,2,3\n" +
			"   urbanist-timetable config init --system bart --stop EMBR --stdout",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "system", Usage: "a system ID from `systems` or `agencies search`"},
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
	system chosenSystem
	stops  []transit.Stop
	lines  []string
	board  string
}

// chosenSystem is the system a new configuration uses: a catalog entry or a
// directory result.
type chosenSystem struct {
	// source opens the system: its catalog or directory ID.
	source string
	// name names its [providers] block, which boards refer to.
	name    string
	title   string
	place   string
	info    string
	needs   []string
	keyInfo string
	// block is the [providers] block, with any comment before it.
	block string
	// warning is printed once the system is chosen.
	warning string
	// found and foundBlock are the directory's system and block, for as.
	found      *directory.System
	foundBlock directory.Block
}

// as names a directory system after the search row it was picked from. A
// regional or national feed carries many agencies, and the lookup by ID
// cannot know which one the person was looking for.
func (c chosenSystem) as(row directory.System) (chosenSystem, error) {
	if c.found == nil || row.Agency == "" {
		return c, nil
	}
	sys := *c.found
	sys.Agency, sys.Name, sys.Place = row.Agency, row.Name, cmp.Or(row.Place, sys.Place)
	out, err := directoryChoice(sys, c.foundBlock)
	out.warning = c.warning
	return out, err
}

func catalogChoice(sys catalog.System) chosenSystem {
	c := chosenSystem{source: sys.ID, name: sys.ID, title: sys.Title(), place: sys.City, info: sys.InfoURL, block: providerBlock(sys)}
	for _, k := range sys.RequiredOptions {
		c.needs = append(c.needs, sys.EnvName(k))
	}
	return c
}

func directoryChoice(sys directory.System, b directory.Block) (chosenSystem, error) {
	name := sys.BlockName()
	text, err := b.TOML(name)
	if err != nil {
		return chosenSystem{}, err
	}
	return chosenSystem{
		source: sys.ID, name: name, title: sys.Title(), place: sys.Place,
		needs: sys.Needs, keyInfo: sys.KeyInfo,
		block: "# " + termout.Clean(b.Comment) + "\n" + text,
		found: &sys, foundBlock: b,
	}, nil
}

// missing lists the environment variables the system needs that are not
// set.
func (c chosenSystem) missing() []string {
	var out []string
	for _, v := range c.needs {
		if os.Getenv(v) == "" {
			out = append(out, v)
		}
	}
	return out
}

// resolveChoice finds a system by catalog or directory ID.
func resolveChoice(ctx context.Context, dir *directory.Directory, id string) (chosenSystem, error) {
	if sys, ok := catalog.Get(id); ok {
		return catalogChoice(sys), nil
	}
	if !directory.IsID(id) {
		return chosenSystem{}, unknownSystemError(id)
	}
	sys, b, err := dir.Resolve(ctx, id)
	if err != nil {
		return chosenSystem{}, err
	}
	c, err := directoryChoice(sys, b)
	if err != nil {
		return chosenSystem{}, err
	}
	if sys.StaticURL != "" {
		if n, ok := dir.ScheduleSize(ctx, sys.StaticURL); ok && n > directory.LargeSchedule {
			c.warning = fmt.Sprintf("warning: %s's schedule is %d MB, which a small machine such as a NAS may struggle to load", id, n>>20)
		}
	}
	return c, nil
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
	dir := newDirectory(c)
	if c.IsSet("system") || !isTerminal(c.Root().Reader) || !isTerminal(c.Root().ErrWriter) {
		a, err = initFromFlags(ctx, c, dir)
	} else {
		p := prompter{
			in:  bufio.NewReader(c.Root().Reader),
			out: c.Root().ErrWriter,
			open: func(name string) (transit.Provider, error) {
				return openSource(ctx, path, name, dir, providerOptions(c)...)
			},
			find: dir.Search,
			resolve: func(ctx context.Context, id string) (chosenSystem, error) {
				return resolveChoice(ctx, dir, id)
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
		if _, err := io.WriteString(c.Root().Writer, src); err != nil {
			return err
		}
		if missing := a.system.missing(); len(missing) > 0 {
			printKeyHint(c.Root().ErrWriter, a.system, missing, "")
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config init: %w", err)
	}
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		return fmt.Errorf("config init: %w", err)
	}
	w := c.Root().Writer
	fmt.Fprintf(w, "Saved %s.\n", path)
	if missing := a.system.missing(); len(missing) > 0 {
		printKeyHint(w, a.system, missing, ", then try: urbanist-timetable departures")
	} else {
		fmt.Fprintln(w, "Try: urbanist-timetable departures")
	}
	return nil
}

// printKeyHint says which variables to set and where to get a key.
func printKeyHint(w io.Writer, sys chosenSystem, missing []string, then string) {
	fmt.Fprintf(w, "Set %s in the environment%s\n", strings.Join(missing, " and "), then)
	if sys.keyInfo != "" {
		fmt.Fprintf(w, "Get a key at %s\n", termout.Clean(sys.keyInfo))
	}
}

func initFromFlags(ctx context.Context, c *cli.Command, dir *directory.Directory) (initAnswers, error) {
	id, stops := c.String("system"), c.StringSlice("stop")
	if id == "" || len(stops) == 0 {
		return initAnswers{}, newUsageError(c, "give --system and --stop, such as `config init --system mta-subway --stop 127N`; find stop IDs with `stops search`")
	}
	sys, err := resolveChoice(ctx, dir, id)
	if err != nil {
		return initAnswers{}, err
	}
	if sys.warning != "" {
		fmt.Fprintln(c.Root().ErrWriter, sys.warning)
	}
	a := initAnswers{system: sys, lines: c.StringSlice("lines"), board: c.String("name")}
	for _, s := range dedupe(stops) {
		a.stops = append(a.stops, transit.Stop{ID: s})
	}
	if a.board == "" {
		a.board = slug(sys.name + "-" + stops[0])
	}
	if !awtrix.ValidAppName(a.board) {
		return initAnswers{}, newUsageError(c, "board name %q must be 1-32 letters, digits, - or _", a.board)
	}
	return a, nil
}

// errInputEnded is returned when the person stops answering, such as with
// Ctrl-D.
var errInputEnded = errors.New("config init: input ended before the configuration was complete")

// prompter asks for the answers at a terminal. open builds a source by
// name, find searches for systems and resolve looks one up by ID, so tests
// can give fakes.
type prompter struct {
	in      *bufio.Reader
	out     io.Writer
	open    func(name string) (transit.Provider, error)
	find    func(ctx context.Context, words string) (directory.Results, error)
	resolve func(ctx context.Context, id string) (chosenSystem, error)
}

func (p prompter) ask(ctx context.Context) (initAnswers, error) {
	sys, err := p.askSystem(ctx)
	if err != nil {
		return initAnswers{}, err
	}
	a := initAnswers{system: sys}
	// Before the stop search, which downloads the schedule.
	if sys.warning != "" {
		fmt.Fprintln(p.out, sys.warning)
	}

	var prov transit.Provider
	if missing := sys.missing(); len(missing) > 0 {
		fmt.Fprintf(p.out, "\n%s needs %s. The file will read them from the environment; set them to search for stops.\n", sys.source, strings.Join(missing, " and "))
		if sys.keyInfo != "" {
			fmt.Fprintf(p.out, "Get a key at %s\n", termout.Clean(sys.keyInfo))
		}
	} else if prov, err = p.open(sys.source); err != nil {
		fmt.Fprintf(p.out, "\nCould not open %s: %s\n", sys.source, oneLine(err))
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

func (p prompter) askSystem(ctx context.Context) (chosenSystem, error) {
	fmt.Fprintln(p.out, "Find your transit system by agency or city, such as \"lisbon\" or \"mta\", or give a system ID.")
	words := ""
	for {
		if words == "" {
			var err error
			if words, err = p.required(ctx, "Agency or city: "); err != nil {
				return chosenSystem{}, err
			}
		}
		c, err := p.resolve(ctx, words)
		switch {
		case err == nil:
			return c, nil
		case ctx.Err() != nil:
			return chosenSystem{}, ctx.Err()
		case directory.IsID(words) && !errors.Is(err, directory.ErrUnknownID):
			// An ID that exists but could not be looked up, such as
			// offline: searching for it as words would hide why.
			fmt.Fprintf(p.out, "Could not use %s: %s\n", words, oneLine(err))
			words = ""
			continue
		}
		fmt.Fprintln(p.out, "Searching…")
		res, err := p.find(ctx, words)
		if ctx.Err() != nil {
			return chosenSystem{}, ctx.Err()
		}
		for _, problem := range res.Problems {
			fmt.Fprintf(p.out, "warning: %s\n", oneLine(problem))
		}
		if err != nil || len(res.Systems) == 0 {
			fmt.Fprintln(p.out, "No systems found; try the city or region, or the country for national railways.")
			if res.NoTransitland {
				fmt.Fprintf(p.out, "Set %s to also search Transitland.\n", directory.TransitlandKeyVar)
			}
			words = ""
			continue
		}
		termout.Table(p.out, systemRows(res.Systems, true))
		if res.More > 0 {
			fmt.Fprintf(p.out, "  … and %d more; search with more words to narrow them.\n", res.More)
		}
		if res.NoTransitland {
			fmt.Fprintf(p.out, "  Set %s to also search Transitland.\n", directory.TransitlandKeyVar)
		}
		if hasUnsure(res.Systems) {
			fmt.Fprintln(p.out, "  "+unsureNote)
		}
		chosen, next, err := p.pickSystem(ctx, res.Systems)
		switch {
		case err != nil:
			return chosenSystem{}, err
		case chosen != nil:
			return *chosen, nil
		}
		words = next
	}
}

// pickSystem asks for a number from systems. It returns the chosen system,
// or else the words to search for next: an answer that is not a number, or
// empty to ask for an agency again.
func (p prompter) pickSystem(ctx context.Context, systems []directory.System) (*chosenSystem, string, error) {
	for {
		answer, err := p.line(ctx, fmt.Sprintf("System [1-%d], other words to search, or Enter to start over: ", len(systems)))
		if err != nil {
			return nil, "", err
		}
		n, isNumber := number(answer)
		switch {
		case !isNumber:
			return nil, answer, nil
		case n < 1 || n > len(systems):
			fmt.Fprintf(p.out, "Give a number from 1 to %d.\n", len(systems))
			continue
		}
		row := systems[n-1]
		c, err := p.resolve(ctx, row.ID)
		if err == nil {
			c, err = c.as(row)
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if err != nil {
			fmt.Fprintf(p.out, "Could not use %s: %s\n", row.ID, oneLine(err))
			continue
		}
		return &c, "", nil
	}
}

// number parses an answer that is a whole number.
func number(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
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
		termout.Stops(p.out, shown, true, sharedNameLines(ctx, prov, shown))
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
	fmt.Fprintf(&b, "# %s", termout.Clean(a.system.title))
	if a.system.place != "" {
		fmt.Fprintf(&b, ", %s", termout.Clean(a.system.place))
	}
	if a.system.info != "" {
		fmt.Fprintf(&b, ". More: %s", a.system.info)
	}
	b.WriteString("\n")
	b.WriteString(a.system.block)
	fmt.Fprintf(&b, "\n# `urbanist-timetable departures` shows every board; `departures %s`, this one.\n", a.board)
	b.WriteString("[[boards]]\n")
	fmt.Fprintf(&b, "name = %s\n", tomlout.String(a.board))
	for _, s := range a.stops {
		b.WriteString("\n[[boards.stops]]\n")
		fmt.Fprintf(&b, "provider = %s\n", tomlout.String(a.system.name))
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
	return cmp.Or(fold.Slug(s, '-', 32), "stop")
}
