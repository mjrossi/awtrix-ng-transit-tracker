package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/clockout"
	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/directory"
	"github.com/mjrossi/urbanist-timetable/internal/fold"
	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/internal/scheduler"
	"github.com/mjrossi/urbanist-timetable/internal/termout"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/registry"
)

func clockCommand() *cli.Command {
	return &cli.Command{
		Name:     "clock",
		Usage:    "show boards on an AWTRIX NG clock",
		Commands: []*cli.Command{runCommand(), previewCommand()},
	}
}

func runCommand() *cli.Command {
	return &cli.Command{
		Name:  "run",
		Usage: "poll providers and push every board to the clock until stopped",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "state",
				Usage:   "file that remembers the configured boards, so ones removed from the config also leave the clock",
				Sources: cli.EnvVars("TIMETABLE_STATE"),
			},
		},
		Action: runBoards,
	}
}

// setup returns the logger and configuration named by the global flags.
func setup(c *cli.Command) (*slog.Logger, *config.Config, error) {
	cfg, err := loadConfig(c)
	if err != nil {
		return nil, nil, err
	}
	return newLogger(c), cfg, nil
}

// loadConfig loads the whole configuration, for commands that need one.
func loadConfig(c *cli.Command) (*config.Config, error) {
	path := c.String("config")
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &noConfigError{path: path, err: err}
	}
	return cfg, err
}

func runBoards(ctx context.Context, c *cli.Command) error {
	log, cfg, err := setup(c)
	if err != nil {
		return err
	}
	if err := cfg.RequireClock(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	clock, err := newClock(cfg)
	if err != nil {
		return err
	}
	provs, err := buildProviders(cfg.Providers, providerOptions(c)...)
	if err != nil {
		return err
	}
	runners, err := newRunners(cfg, provs, newClockOutput(cfg, clock), log, nil)
	if err != nil {
		return err
	}
	logStartup(ctx, log, cfg, clock.Host())
	logQuotaUses(ctx, log, cfg.QuotaUses(provs))
	var wg sync.WaitGroup
	if path := c.String("state"); path != "" {
		wg.Go(func() { newCleanup(cfg, path, clock, log).Run(ctx) })
	} else {
		log.InfoContext(ctx, "no state file; boards removed from the config keep their place on the clock")
	}
	err = scheduler.RunAll(ctx, runners)
	wg.Wait()
	log.InfoContext(ctx, "stopped")
	return err
}

func logStartup(ctx context.Context, log *slog.Logger, cfg *config.Config, clockHost string) {
	log.InfoContext(ctx, "starting",
		slog.String("version", version),
		slog.Int("boards", len(cfg.Boards)),
		slog.String("clock", clockHost),
		slog.Any("quiet_hours", cfg.QuietHours))
	for _, b := range cfg.Boards {
		stops := make([]string, len(b.Stops))
		for i, s := range b.Stops {
			stops[i] = s.Provider + ":" + s.Stop
		}
		log.InfoContext(ctx, "board configured",
			slog.String("board", b.Name),
			slog.Any("stops", stops),
			slog.Any("active_hours", b.ActiveHours),
			slog.Duration("poll_interval", b.PollInterval.D()),
			slog.Duration("lifetime", b.Clock.Lifetime.D()))
	}
}

func logQuotaUses(ctx context.Context, log *slog.Logger, uses []config.QuotaUse) {
	for _, u := range uses {
		attrs := []any{slog.Any("providers", u.Providers), slog.Int("requests_per_month", u.Requests), slog.Int("monthly_quota", u.Quota)}
		if u.Over() {
			log.WarnContext(ctx, "estimated requests exceed the provider's monthly quota; raise refresh_interval or narrow active_hours", attrs...)
		} else {
			log.InfoContext(ctx, "estimated provider requests", attrs...)
		}
	}
}

func previewCommand() *cli.Command {
	return &cli.Command{
		Name:          "preview",
		Usage:         "fetch once and print each board's rows and payload, optionally pushing it",
		ShellComplete: completeBoardFlag,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "board", Usage: "only this board"},
			&cli.BoolFlag{Name: "push", Usage: "also push the payload to the clock"},
			&cli.BoolFlag{Name: "json", Usage: "print only the JSON payload"},
		},
		Action: preview,
	}
}

func preview(ctx context.Context, c *cli.Command) error {
	log, cfg, err := setup(c)
	if err != nil {
		return err
	}
	if err := cfg.RequireClock(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if name := c.String("board"); name != "" {
		cfg.Boards = filterBoards(cfg.Boards, name)
		if len(cfg.Boards) == 0 {
			return fmt.Errorf("no board named %q", name)
		}
	}
	push := c.Bool("push")
	var pusher clockout.Pusher
	if push {
		if pusher, err = newClock(cfg); err != nil {
			return err
		}
	}
	out := newClockOutput(cfg, pusher)
	provs, err := buildProviders(cfg.Providers, providerOptions(c)...)
	if err != nil {
		return err
	}
	runners, err := newRunners(cfg, provs, out, log, nil)
	if err != nil {
		return err
	}
	instant := time.Now()
	for i, r := range runners {
		b := cfg.Boards[i]
		snap, ok := r.Snapshot(ctx, instant.In(b.Location()))
		if !ok {
			return fmt.Errorf("board %s: every source failed", b.Name)
		}
		if err := writePreview(c.Root().Writer, b, out, snap, c.Bool("json")); err != nil {
			return err
		}
		if push {
			if err := out.Publish(ctx, snap); err != nil {
				return err
			}
		}
	}
	return nil
}

// writePreview prints a board's rows, unless jsonOnly, then the payload the
// clock would receive.
func writePreview(w io.Writer, b config.Board, out *clockout.Output, snap scheduler.Snapshot, jsonOnly bool) error {
	app, err := out.App(snap)
	if err != nil {
		return err
	}
	if !jsonOnly {
		printRows(w, b, snap.Rows(snap.At), snap.At)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(app)
}

func configCommand() *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "create, check or explain the configuration file",
		Commands: []*cli.Command{initCommand(), {
			Name:  "example",
			Usage: "print an example configuration that explains every setting",
			Action: func(_ context.Context, c *cli.Command) error {
				_, err := io.WriteString(c.Root().Writer, config.Example())
				return err
			},
		}, {
			Name:  "validate",
			Usage: "check the configuration file and build its providers and clock client",
			Action: func(_ context.Context, c *cli.Command) error {
				cfg, err := loadConfig(c)
				if err != nil {
					return err
				}
				provs, err := buildProviders(cfg.Providers, providerOptions(c)...)
				if err != nil {
					return err
				}
				if cfg.Clock.URL != "" {
					if _, err := newClock(cfg); err != nil {
						return err
					}
				}
				w := c.Root().Writer
				fmt.Fprintf(w, "%s: OK (%d providers, %d boards)\n", c.String("config"), len(cfg.Providers), len(cfg.Boards))
				if cfg.Clock.URL == "" && len(cfg.Boards) > 0 {
					fmt.Fprintln(w, "no [clock]: boards show in `departures`; add one for `clock run`")
				}
				writeQuotaUses(w, cfg.QuotaUses(provs))
				return nil
			},
		}},
	}
}

// writeQuotaUses prints the estimate for each quota. Going over is a
// warning, not an error: the configuration is valid, and the estimate is
// approximate.
func writeQuotaUses(w io.Writer, uses []config.QuotaUse) {
	for _, u := range uses {
		names := "providers." + strings.Join(u.Providers, ", providers.")
		fmt.Fprintf(w, "%s: about %d requests a month, quota %d\n", names, u.Requests, u.Quota)
		if u.Over() {
			fmt.Fprintf(w, "warning: %s would exceed the monthly quota; raise refresh_interval or narrow active_hours\n", names)
		}
	}
}

func stopsCommand() *cli.Command {
	return &cli.Command{
		Name:  "stops",
		Usage: "find stop IDs",
		Commands: []*cli.Command{{
			Name:      "search",
			Usage:     "search a source's stops by name or ID",
			ArgsUsage: "<source> <words...>",
			Description: "<source> is a system ID from `agencies search` or a [providers.NAME] block\n" +
				"in the config. Prints stop IDs for `departures`.\n\n" +
				"   urbanist-timetable stops search mta-subway times sq\n" +
				"   urbanist-timetable stops search bart embarcadero",
			Flags:         []cli.Flag{&cli.BoolFlag{Name: "json", Usage: "print JSON instead of text"}},
			ShellComplete: completeFirst(completeSources(false)),
			Action: func(ctx context.Context, c *cli.Command) error {
				if c.Args().Len() < 2 {
					return newUsageError(c, "give a source and words to search for, such as `stops search mta-subway times sq`")
				}
				name := c.Args().First()
				search := strings.Join(c.Args().Tail(), " ")
				path, err := queryConfig(c)
				if err != nil {
					return err
				}
				p, err := openSource(ctx, path, name, newDirectory(c), providerOptions(c)...)
				if err != nil {
					return err
				}
				searcher, ok := p.(transit.StopSearcher)
				if !ok {
					return fmt.Errorf("provider %q cannot search stops", name)
				}
				stops, err := searcher.SearchStops(ctx, search)
				if err != nil {
					return err
				}
				return writeStops(ctx, c, name, p, stops)
			},
		}},
	}
}

// queryConfig returns the configuration path for commands that work
// without a file. A path given with --config must exist; TIMETABLE_CONFIG
// may name one that does not, as the Docker image does by default.
func queryConfig(c *cli.Command) (string, error) {
	path := c.String("config")
	if !c.IsSet("config") || os.Getenv("TIMETABLE_CONFIG") == path {
		return path, nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "", &noConfigError{path: path, err: err}
	}
	return path, nil
}

// openSource builds the provider name refers to: a [providers.NAME] block
// in the file at path, else a catalog system, with required options read
// from <SYSTEM>_<OPTION> environment variables, else, with dir, a
// Mobility Database or Transitland system, else a provider type that needs
// no settings. Only that block is read, so a query works before the rest
// of the configuration does.
func openSource(ctx context.Context, path, name string, dir *directory.Directory, opts ...providers.Option) (transit.Provider, error) {
	settings, found, err := config.LoadProvider(path, name)
	if err != nil {
		return nil, err
	}
	if found {
		return providers.Open(name, settings, opts...)
	}
	if sys, ok := catalog.Get(name); ok {
		return providers.Open(name, envSettings(sys), opts...)
	}
	if dir != nil && directory.IsID(name) {
		return openDirectorySystem(ctx, dir, name, opts...)
	}
	p, err := providers.Open(name, map[string]any{}, opts...)
	if err == nil {
		return p, nil
	}
	serr := &sourceError{name: name, configPath: path, err: err}
	if errors.Is(err, registry.ErrUnknownProvider) {
		serr.suggestion = closest(name, sourceNames(path, false))
	}
	return nil, serr
}

// envSettings is the settings block for sys, with each required or
// optional option that is set in the environment.
func envSettings(sys catalog.System) map[string]any {
	settings := map[string]any{"system": sys.ID}
	for _, k := range sys.EnvOptions() {
		if v := os.Getenv(sys.EnvName(k)); v != "" {
			settings[k] = v
		}
	}
	return settings
}

// sourceNames lists what can name a source: the file's provider blocks and
// catalog systems, and with boards the file's boards. Provider types are
// left out: every one either needs settings or is behind a system.
func sourceNames(path string, boards bool) []string {
	boardNames, blocks := config.Names(path)
	var names []string
	if boards {
		names = boardNames
	}
	names = append(names, blocks...)
	return append(names, systemIDs()...)
}

// writeStops prints search results, and at a terminal the command that
// uses them.
func writeStops(ctx context.Context, c *cli.Command, source string, p transit.Provider, stops []transit.Stop) error {
	w := c.Root().Writer
	if c.Bool("json") {
		return termout.StopsJSON(w, stops)
	}
	if len(stops) == 0 {
		fmt.Fprintln(w, "no stops found")
		return nil
	}
	termout.Stops(w, stops, false, sharedNameLines(ctx, p, stops))
	if isTerminal(w) {
		fmt.Fprintf(c.Root().ErrWriter, "\nnext: urbanist-timetable departures %s <ID>\n", source)
	}
	return nil
}

// maxLinedStops caps the stops sharedNameLines asks about: each can cost
// a request against a quota.
const maxLinedStops = 10

// sharedNameLines asks p which lines serve the stops whose names repeat,
// such as one stop per side of a street, and where they go from each, so
// they can be told apart. It gives nothing when p cannot tell, or the
// names are all different; a failure only loses the hint.
func sharedNameLines(ctx context.Context, p transit.Provider, stops []transit.Stop) map[string][]transit.StopLine {
	liner, ok := p.(transit.StopLiner)
	if !ok {
		return nil
	}
	count := map[string]int{}
	for _, s := range stops {
		count[fold.String(s.Name)]++
	}
	var ids []string
	for _, s := range stops {
		if count[fold.String(s.Name)] > 1 && len(ids) < maxLinedStops {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	lines, _ := liner.StopLines(ctx, ids, clock())
	return lines
}

func filterBoards(boards []config.Board, name string) []config.Board {
	for _, b := range boards {
		if b.Name == name {
			return []config.Board{b}
		}
	}
	return nil
}

func printRows(w io.Writer, b config.Board, rows []transit.Row, now time.Time) {
	opts := b.Clock.Render()
	fmt.Fprintf(w, "# %s (%s layout) at %s\n", b.Name, opts.Layout, now.Format("15:04:05 MST"))
	if len(rows) == 0 {
		fmt.Fprintln(w, "#   (no departures)")
	}
	scrolls := false
	for _, v := range render.Views(rows, now, opts) {
		mins := make([]string, len(v.Row.Departures))
		for i, d := range v.Row.Departures {
			mins[i] = strconv.Itoa(d.MinutesUntil(now))
			if !d.Realtime() {
				mins[i] += "*"
			}
		}
		mark := ""
		switch {
		case !v.Shown:
			mark = " (not shown)"
		case v.Overflows:
			mark, scrolls = " ~", true
		}
		fmt.Fprintf(w, "#   %-8s %-24s %s%s\n", v.Row.Line.ShortName, v.Name, strings.Join(mins, " "), mark)
	}
	fmt.Fprintln(w, "#   (* = schedule only)")
	if scrolls {
		fmt.Fprintln(w, "#   (~ = too wide for its space; set a shorter name under [boards.clock.labels])")
	}
}

func cacheCommand() *cli.Command {
	return &cli.Command{
		Name:  "cache",
		Usage: "manage the GTFS schedules kept between runs",
		Commands: []*cli.Command{{
			Name:  "clear",
			Usage: "delete the cached GTFS schedules; the next run downloads them again",
			Action: func(_ context.Context, c *cli.Command) error {
				dir := cacheDir(c)
				if dir == "" {
					return newUsageError(c, "the schedule cache is off (--cache-dir off); there is nothing to clear")
				}
				n, freed, err := providers.ClearScheduleCache(dir, clock())
				listFreed, listErr := directory.ClearCache(dir)
				freed += listFreed
				err = errors.Join(err, listErr)
				w := c.Root().Writer
				switch {
				case n == 1:
					fmt.Fprintf(w, "removed 1 schedule (%.1f MB) from %s\n", float64(freed)/(1<<20), dir)
				case n > 1:
					fmt.Fprintf(w, "removed %d schedules (%.1f MB) from %s\n", n, float64(freed)/(1<<20), dir)
				case err == nil:
					fmt.Fprintf(w, "no cached schedules in %s\n", dir)
				}
				return err
			},
		}},
	}
}
