package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/termout"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

// clock is the command's time source; tests replace it.
var clock = time.Now

// clearScreen moves the cursor home and clears the terminal.
const clearScreen = "\x1b[H\x1b[2J"

const departuresUsage = "give a source and one or more stop IDs, such as `departures nyc-subway 127N`"

func departuresCommand() *cli.Command {
	return &cli.Command{
		Name:      "departures",
		Usage:     "print the next departures at one or more stops",
		ArgsUsage: "[<board> | <source> <stop>...]",
		Description: "Without arguments, prints every [[boards]] entry in the config; with one, that\n" +
			"board. Otherwise <source> is a [providers.NAME] block in the config, a system\n" +
			"from `systems`, or a provider that needs no settings. With --realtime there is\n" +
			"no source. Flags replace a board's settings. gtfsrt sources show only\n" +
			"departures with realtime predictions. The --json shape may change before 1.0.\n\n" +
			"   urbanist-timetable departures\n" +
			"   urbanist-timetable departures nyc-subway 127N\n" +
			"   urbanist-timetable departures nyc-subway 127N --lines 1,2,3 --watch\n" +
			"   urbanist-timetable departures --static URL.zip --realtime URL.pb STOP_ID",
		ShellComplete: completeFirst(completeSources(true)),
		Flags: []cli.Flag{
			&cli.StringSliceFlag{Name: "lines", Usage: "only these lines (IDs or short names)"},
			&cli.StringSliceFlag{Name: "headsigns", Usage: "only destinations containing one of these"},
			&cli.DurationFlag{Name: "min-lead", Usage: "leave out departures sooner than this, such as the walk to the stop"},
			&cli.BoolFlag{Name: "canceled", Usage: "include canceled departures, marked x"},
			&cli.DurationFlag{Name: "window", Value: config.DefaultWindow, Usage: "how far ahead to look"},
			&cli.IntFlag{Name: "per-row", Value: config.DefaultPerRow, Usage: "departures per row; 0 for all"},
			&cli.IntFlag{Name: "limit", Usage: "rows to show; 0 for all"},
			&cli.BoolFlag{Name: "json", Usage: "print JSON instead of text"},
			&cli.BoolFlag{Name: "watch", Usage: "redraw every --interval until stopped"},
			&cli.DurationFlag{Name: "interval", Value: 30 * time.Second, Usage: "time between redraws with --watch"},
			&cli.StringSliceFlag{Name: "realtime", Usage: "a GTFS-Realtime TripUpdates URL, instead of a source"},
			&cli.StringFlag{Name: "static", Usage: "the GTFS schedule zip that goes with --realtime"},
		},
		Action: departures,
	}
}

func departures(ctx context.Context, c *cli.Command) error {
	w := c.Root().Writer
	watching := c.Bool("watch")
	switch {
	case watching && c.Bool("json"):
		return newUsageError(c, "--watch and --json cannot be used together")
	case watching && c.Duration("interval") < config.MinPollInterval:
		return newUsageError(c, "--interval must be at least %s", config.MinPollInterval)
	case watching && !isTerminal(w):
		return errors.New("--watch needs a terminal")
	}

	var (
		q   query
		err error
	)
	args := c.Args().Slice()
	switch {
	case len(c.StringSlice("realtime")) > 0:
		q, err = feedQuery(c, args)
	case len(args) >= 2:
		q, err = sourceQuery(c, args[0], args[1:])
	default:
		q, err = boardsQuery(c, args)
	}
	if err != nil {
		return err
	}
	q.json = c.Bool("json")
	q.opts = termout.Options{Color: isTerminal(w) && os.Getenv("NO_COLOR") == "", Limit: c.Int("limit")}
	q.warnings = c.Root().ErrWriter

	if watching {
		t := time.NewTicker(c.Duration("interval"))
		defer t.Stop()
		return watch(ctx, w, clock(), t.C, q.show)
	}
	out, err := q.show(ctx, clock())
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out)
	return err
}

// feedQuery queries stops in the GTFS-Realtime feed named by --realtime.
func feedQuery(c *cli.Command, stops []string) (query, error) {
	if len(stops) == 0 {
		return query{}, newUsageError(c, "give one or more stop IDs after --realtime")
	}
	settings := map[string]any{"realtime_urls": toAny(c.StringSlice("realtime"))}
	if s := c.String("static"); s != "" {
		settings["static_url"] = s
	}
	p, err := providers.Open("gtfsrt", settings, cacheOptions(c)...)
	if err != nil {
		return query{}, err
	}
	return adHocQuery(c, "gtfsrt", p, stops), nil
}

// sourceQuery queries stops of a source named on the command line.
func sourceQuery(c *cli.Command, source string, stops []string) (query, error) {
	path, err := queryConfig(c)
	if err != nil {
		return query{}, err
	}
	p, err := openSource(path, source, cacheOptions(c)...)
	if err != nil {
		return query{}, err
	}
	return adHocQuery(c, source, p, stops), nil
}

func adHocQuery(c *cli.Command, source string, p transit.Provider, stops []string) query {
	f := flagFilter(c, transit.Filter{})
	bq := boardQuery{source: source, window: c.Duration("window"), perRow: c.Int("per-row")}
	for _, stop := range dedupe(stops) {
		bq.stops = append(bq.stops, stopSource{block: source, provider: p, stop: stop, filter: f})
	}
	return query{boards: []boardQuery{bq}}
}

// boardsQuery queries the boards in the configuration: all of them, or the
// one named.
func boardsQuery(c *cli.Command, args []string) (query, error) {
	path := c.String("config")
	cfg, err := loadConfig(c)
	var noConfig *noConfigError
	switch {
	case errors.As(err, &noConfig):
		if len(args) == 1 {
			return query{}, oneArgumentError(c, path, args[0])
		}
		return query{}, newUsageError(c, "no saved boards (%s does not exist); %s, or save boards with `urbanist-timetable config init`", path, departuresUsage)
	case err != nil:
		return query{}, err
	}
	boards := cfg.Boards
	if len(args) == 1 {
		boards = filterBoards(boards, args[0])
		if len(boards) == 0 {
			return query{}, oneArgumentError(c, path, args[0])
		}
	}
	if len(boards) == 0 {
		return query{}, newUsageError(c, "no saved boards in %s; %s, or save boards with `urbanist-timetable config init`", path, departuresUsage)
	}

	used := *cfg
	used.Providers = map[string]map[string]any{}
	for _, b := range boards {
		for _, s := range b.Stops {
			used.Providers[s.Provider] = cfg.Providers[s.Provider]
		}
	}
	provs, err := buildProviders(&used, cacheOptions(c)...)
	if err != nil {
		return query{}, err
	}

	var q query
	for _, b := range boards {
		bq := boardQuery{name: b.Name, window: b.Window.D(), perRow: b.PerRow}
		if c.IsSet("window") {
			bq.window = c.Duration("window")
		}
		if c.IsSet("per-row") {
			bq.perRow = c.Int("per-row")
		}
		var blocks []string
		for _, s := range b.Stops {
			bq.stops = append(bq.stops, stopSource{block: s.Provider, provider: provs[s.Provider], stop: s.Stop, filter: flagFilter(c, s.Filter())})
			if !slices.Contains(blocks, s.Provider) {
				blocks = append(blocks, s.Provider)
			}
		}
		bq.source = strings.Join(blocks, ", ")
		q.boards = append(q.boards, bq)
	}
	q.list = true
	return q, nil
}

// oneArgumentError explains a single argument that names no board.
func oneArgumentError(c *cli.Command, path, arg string) error {
	if slices.Contains(sourceNames(path, false), arg) {
		return newUsageError(c, "give one or more stop IDs after %s; find them with `urbanist-timetable stops search %s <words>`", arg, arg)
	}
	boards, _ := config.Names(path)
	msg := fmt.Sprintf("no board named %q in %s", arg, path)
	if s := closest(arg, boards); s != "" {
		msg += fmt.Sprintf("; did you mean %q?", s)
	}
	return newUsageError(c, "%s; %s", msg, departuresUsage)
}

// flagFilter is f with the filter flags that were given in place of its
// settings.
func flagFilter(c *cli.Command, f transit.Filter) transit.Filter {
	if c.IsSet("lines") {
		f.Lines = c.StringSlice("lines")
	}
	if c.IsSet("headsigns") {
		f.Headsigns = c.StringSlice("headsigns")
	}
	if c.IsSet("min-lead") {
		f.MinLead = c.Duration("min-lead")
	}
	if c.IsSet("canceled") {
		f.KeepCanceled = c.Bool("canceled")
	}
	return f
}

// query is what one run of `departures` shows: stops from the command line,
// or boards from the configuration.
type query struct {
	boards []boardQuery
	// list is set for boards from the configuration, which print as a JSON
	// array even when there is one.
	list     bool
	json     bool
	opts     termout.Options
	warnings io.Writer
}

// boardQuery is one block of output.
type boardQuery struct {
	// name is the configured board, or empty for stops on the command line.
	name string
	// source names the provider blocks, systems or types queried.
	source string
	stops  []stopSource
	window time.Duration
	perRow int
}

type stopSource struct {
	block    string
	provider transit.Provider
	stop     string
	filter   transit.Filter
}

// show fetches every board and renders the result. It fails only when
// everything fails; otherwise problems are warnings.
func (q query) show(ctx context.Context, now time.Time) (string, error) {
	var (
		out    bytes.Buffer
		boards []termout.Board
		failed []error
	)
	for _, bq := range q.boards {
		b, err := q.fetch(ctx, now, bq)
		switch {
		case ctx.Err() != nil:
			return "", ctx.Err()
		case err != nil && len(q.boards) == 1:
			return "", err
		case err != nil:
			err = fmt.Errorf("board %s: %w", bq.name, err)
			failed = append(failed, err)
			continue
		}
		boards = append(boards, b)
		if !q.json {
			if len(boards) > 1 {
				out.WriteByte('\n')
			}
			opts := q.opts
			opts.PerRow = bq.perRow
			if err := termout.Text(&out, b, opts); err != nil {
				return "", err
			}
		}
	}
	if len(boards) == 0 {
		return "", errors.Join(failed...)
	}
	for _, err := range failed {
		warn(q.warnings, err)
	}
	var err error
	switch {
	case q.json && q.list:
		err = termout.JSONList(&out, boards)
	case q.json:
		err = termout.JSON(&out, boards[0])
	}
	return out.String(), err
}

// fetch fetches one board's stops. It fails only when every stop fails.
func (q query) fetch(ctx context.Context, now time.Time, bq boardQuery) (termout.Board, error) {
	b := termout.Board{Name: bq.name, Source: bq.source, At: now, Window: bq.window}
	var (
		failed   []error
		unknown  int
		hidden   int
		narrowed bool
		lines    []string
	)
	for _, s := range bq.stops {
		deps, err := s.provider.Departures(ctx, transit.Query{StopID: s.stop, Now: now, Window: bq.window})
		var partial *transit.PartialError
		switch {
		case errors.As(err, &partial):
			b.Partial = true
			warn(q.warnings, fmt.Errorf("stop %s: %w", s.stop, err))
		case err != nil:
			if errors.Is(err, transit.ErrUnknownStop) {
				unknown++
			}
			failed = append(failed, fmt.Errorf("stop %s: %w", s.stop, err))
			continue
		}
		kept := s.filter.Apply(now, deps)
		hidden += len(deps) - len(kept)
		narrowed = narrowed || len(s.filter.Lines) > 0 || len(s.filter.Headsigns) > 0 || s.filter.MinLead > 0
		for _, d := range deps {
			name := cmp.Or(d.Line.ShortName, d.Line.ID)
			if !slices.Contains(lines, name) {
				lines = append(lines, name)
			}
		}
		b.Departures = append(b.Departures, kept...)
	}
	if len(failed) == len(bq.stops) {
		err := errors.Join(failed...)
		if unknown == len(bq.stops) && bq.name == "" {
			stops := make([]string, len(bq.stops))
			for i, s := range bq.stops {
				stops[i] = s.stop
			}
			return b, &unknownStopsError{source: bq.source, stops: stops, err: err}
		}
		return b, err
	}
	for _, err := range failed {
		b.Partial = true
		warn(q.warnings, err)
	}
	b.Title = title(ctx, now, bq)
	if len(b.Departures) == 0 && hidden > 0 && narrowed && !q.json {
		slices.Sort(lines)
		prefix := "note: "
		if bq.name != "" {
			prefix += "board " + bq.name + ": "
		}
		fmt.Fprintf(q.warnings, "%sthe filters hid %d departures; lines here: %s\n", prefix, hidden, termout.Clean(strings.Join(lines, ", ")))
	}
	return b, nil
}

// warn prints err as one warning line.
func warn(w io.Writer, err error) {
	fmt.Fprintf(w, "warning: %s\n", oneLine(err))
}

// title names a board's stops where the provider already holds names, and
// gives IDs otherwise; a name is not worth a search or a metered request.
// A configured board's name comes first.
func title(ctx context.Context, now time.Time, bq boardQuery) string {
	names := make([]string, len(bq.stops))
	for i, s := range bq.stops {
		names[i] = s.stop
		if namer, ok := s.provider.(transit.StopNamer); ok {
			if n, ok := namer.StopName(ctx, s.stop, now); ok {
				names[i] = n
			}
		}
	}
	t := strings.Join(slices.Compact(names), " / ")
	if bq.name != "" {
		t = bq.name + ": " + t
	}
	return t
}

// watch draws show's output at start and on every tick until ctx is done.
// After a failed refresh it keeps the last good output and says how old it
// is.
func watch(ctx context.Context, w io.Writer, start time.Time, tick <-chan time.Time, show func(context.Context, time.Time) (string, error)) error {
	var last string
	var lastAt time.Time
	draw := func(now time.Time) {
		out, err := show(ctx, now)
		switch {
		case err == nil:
			last, lastAt = out, now
			fmt.Fprint(w, clearScreen+out)
		case ctx.Err() != nil:
		case last != "":
			fmt.Fprintf(w, "%s%slast updated %s ago: %s\n", clearScreen, last, now.Sub(lastAt).Round(time.Second), termout.Clean(err.Error()))
		default:
			fmt.Fprintf(w, "%serror: %s\n", clearScreen, termout.Clean(err.Error()))
		}
	}
	draw(start)
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-tick:
			draw(now)
		}
	}
}

// isTerminal reports whether f, a reader or writer, is a terminal, so
// color, redrawing and prompts make sense there.
func isTerminal(f any) bool {
	file, ok := f.(*os.File)
	if !ok {
		return false
	}
	fi, err := file.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func dedupe(stops []string) []string {
	var out []string
	for _, s := range stops {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
