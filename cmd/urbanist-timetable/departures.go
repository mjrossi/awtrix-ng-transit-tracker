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
		ArgsUsage: "<source> <stop>...",
		Description: "<source> is a [providers.NAME] block in the config, a system from `systems`,\n" +
			"or a provider that needs no settings. With --realtime there is no source.\n" +
			"gtfsrt sources show only departures with realtime predictions.\n" +
			"The --json shape may change before 1.0.",
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
		return errors.New("--watch and --json cannot be used together")
	case watching && c.Duration("interval") < config.MinPollInterval:
		return fmt.Errorf("--interval must be at least %s", config.MinPollInterval)
	case watching && !isTerminal(w):
		return errors.New("--watch needs a terminal")
	}

	args := c.Args().Slice()
	var (
		source   string
		settings map[string]any
	)
	if urls := c.StringSlice("realtime"); len(urls) > 0 {
		source = "gtfsrt"
		settings = map[string]any{"realtime_urls": toAny(urls)}
		if s := c.String("static"); s != "" {
			settings["static_url"] = s
		}
	} else {
		if len(args) < 2 {
			return newUsageError(c, departuresUsage)
		}
		source, args = args[0], args[1:]
	}
	if len(args) == 0 {
		return newUsageError(c, "give one or more stop IDs after --realtime")
	}
	stops := dedupe(args)

	var (
		p   transit.Provider
		err error
	)
	if settings != nil {
		p, err = providers.Open(source, settings, cacheOptions(c)...)
	} else {
		var path string
		if path, err = queryConfig(c); err != nil {
			return err
		}
		p, err = openSource(path, source, cacheOptions(c)...)
	}
	if err != nil {
		return err
	}

	q := query{
		provider: p,
		source:   source,
		stops:    stops,
		window:   c.Duration("window"),
		filter: transit.Filter{
			Lines:        c.StringSlice("lines"),
			Headsigns:    c.StringSlice("headsigns"),
			MinLead:      c.Duration("min-lead"),
			KeepCanceled: c.Bool("canceled"),
		},
		json:     c.Bool("json"),
		opts:     termout.Options{Color: isTerminal(w) && os.Getenv("NO_COLOR") == "", PerRow: c.Int("per-row"), Limit: c.Int("limit")},
		warnings: c.Root().ErrWriter,
	}
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

type query struct {
	provider transit.Provider
	source   string
	stops    []string
	window   time.Duration
	filter   transit.Filter
	json     bool
	opts     termout.Options
	warnings io.Writer
}

// show fetches every stop and renders the result. It fails only when every
// stop fails; otherwise problems are warnings.
func (q query) show(ctx context.Context, now time.Time) (string, error) {
	b := termout.Board{Source: q.source, At: now, Window: q.window}
	var (
		failed  []error
		unknown int
		hidden  int
		lines   []string
	)
	for _, stop := range q.stops {
		deps, err := q.provider.Departures(ctx, transit.Query{StopID: stop, Now: now, Window: q.window})
		var partial *transit.PartialError
		switch {
		case errors.As(err, &partial):
			b.Partial = true
			fmt.Fprintf(q.warnings, "warning: stop %s: %s\n", termout.Clean(stop), termout.Clean(err.Error()))
		case err != nil:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if errors.Is(err, transit.ErrUnknownStop) {
				unknown++
			}
			failed = append(failed, fmt.Errorf("stop %s: %w", stop, err))
			continue
		}
		kept := q.filter.Apply(now, deps)
		hidden += len(deps) - len(kept)
		for _, d := range deps {
			name := cmp.Or(d.Line.ShortName, d.Line.ID)
			if !slices.Contains(lines, name) {
				lines = append(lines, name)
			}
		}
		b.Departures = append(b.Departures, kept...)
	}
	if len(failed) == len(q.stops) {
		err := errors.Join(failed...)
		if unknown == len(q.stops) {
			return "", &unknownStopsError{source: q.source, stops: q.stops, err: err}
		}
		return "", err
	}
	for _, err := range failed {
		b.Partial = true
		fmt.Fprintf(q.warnings, "warning: %s\n", termout.Clean(err.Error()))
	}
	b.Title = q.title(ctx, now)
	narrowed := len(q.filter.Lines) > 0 || len(q.filter.Headsigns) > 0 || q.filter.MinLead > 0
	if len(b.Departures) == 0 && hidden > 0 && narrowed && !q.json {
		slices.Sort(lines)
		fmt.Fprintf(q.warnings, "note: the filters hid %d departures; lines here: %s\n", hidden, termout.Clean(strings.Join(lines, ", ")))
	}

	var out bytes.Buffer
	var err error
	if q.json {
		err = termout.JSON(&out, b)
	} else {
		err = termout.Text(&out, b, q.opts)
	}
	return out.String(), err
}

// title names the stops where the provider already holds names, and gives
// IDs otherwise; a name is not worth a search or a metered request.
func (q query) title(ctx context.Context, now time.Time) string {
	names := slices.Clone(q.stops)
	if namer, ok := q.provider.(transit.StopNamer); ok {
		for i, id := range names {
			if n, ok := namer.StopName(ctx, id, now); ok {
				names[i] = n
			}
		}
	}
	return strings.Join(names, " / ")
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

// isTerminal reports whether w is a terminal, so color and redrawing make
// sense there.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
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
