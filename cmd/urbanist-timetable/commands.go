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
	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/internal/scheduler"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

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
		Action: func(ctx context.Context, c *cli.Command) error {
			log, err := newLogger(c)
			if err != nil {
				return err
			}
			cfg, err := config.Load(c.String("config"))
			if err != nil {
				return err
			}
			clock, err := newClock(cfg)
			if err != nil {
				return err
			}
			runners, err := newRunners(cfg, newClockOutput(cfg, clock), log, nil)
			if err != nil {
				return err
			}
			log.InfoContext(ctx, "starting",
				slog.String("version", version),
				slog.Int("boards", len(runners)),
				slog.String("clock", clock.Host()),
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
		},
	}
}

func previewCommand() *cli.Command {
	return &cli.Command{
		Name:  "preview",
		Usage: "fetch once and print each board's rows and payload, optionally pushing it",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "board", Usage: "only this board"},
			&cli.BoolFlag{Name: "push", Usage: "also push the payload to the clock"},
			&cli.BoolFlag{Name: "json", Usage: "print only the JSON payload"},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			log, err := newLogger(c)
			if err != nil {
				return err
			}
			cfg, err := config.Load(c.String("config"))
			if err != nil {
				return err
			}
			if name := c.String("board"); name != "" {
				cfg.Boards = filterBoards(cfg.Boards, name)
				if len(cfg.Boards) == 0 {
					return fmt.Errorf("no board named %q", name)
				}
			}
			var pusher clockout.Pusher
			if c.Bool("push") {
				if pusher, err = newClock(cfg); err != nil {
					return err
				}
			}
			out := newClockOutput(cfg, pusher)
			runners, err := newRunners(cfg, out, log, nil)
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
				if err := writePreview(os.Stdout, b, out, snap, c.Bool("json")); err != nil {
					return err
				}
				if c.Bool("push") {
					if err := out.Publish(ctx, snap); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
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
		Usage: "configuration tools",
		Commands: []*cli.Command{{
			Name:  "validate",
			Usage: "check the configuration file and build its providers and clock client",
			Action: func(_ context.Context, c *cli.Command) error {
				cfg, err := config.Load(c.String("config"))
				if err != nil {
					return err
				}
				if _, err := buildProviders(cfg); err != nil {
					return err
				}
				if _, err := newClock(cfg); err != nil {
					return err
				}
				fmt.Printf("%s: OK (%d boards)\n", c.String("config"), len(cfg.Boards))
				return nil
			},
		}},
	}
}

func systemsCommand() *cli.Command {
	return &cli.Command{
		Name:  "systems",
		Usage: "list ready-made transit systems, or show how to configure one",
		Action: func(_ context.Context, _ *cli.Command) error {
			all, err := catalog.All()
			if err != nil {
				return err
			}
			for _, s := range all {
				needs := ""
				if len(s.RequiredOptions) > 0 {
					needs = "  (needs " + strings.Join(s.RequiredOptions, ", ") + ")"
				}
				fmt.Printf("%-28s %-22s %-24s %s%s\n", s.ID, s.Name, s.City, strings.Join(s.Modes, ", "), needs)
			}
			fmt.Println("\nShow how to configure one: urbanist-timetable systems show <id>")
			return nil
		},
		Commands: []*cli.Command{{
			Name:      "show",
			Usage:     "print a system's details and a config snippet",
			ArgsUsage: "<id>",
			Action: func(_ context.Context, c *cli.Command) error {
				id := c.Args().First()
				s, ok := catalog.Get(id)
				if !ok {
					return fmt.Errorf("unknown system %q; run `urbanist-timetable systems`", id)
				}
				fmt.Printf("# %s, %s (%s)\n", s.Name, s.City, s.Country)
				fmt.Printf("# Status: %s, last verified %s. Data: %s.\n", s.Status, s.Verified, s.DataLicense)
				fmt.Printf("# More: %s\n", s.InfoURL)
				for line := range strings.SplitSeq(strings.TrimSpace(s.Notes), "\n") {
					fmt.Printf("# %s\n", line)
				}
				fmt.Printf("\n[providers.%s]\nsystem = %q\n", s.ID, s.ID)
				for _, k := range s.RequiredOptions {
					env := strings.ToUpper(strings.ReplaceAll(s.ID+"_"+k, "-", "_"))
					fmt.Printf("%s = \"${%s}\"\n", k, env)
				}
				fmt.Printf("\n# Find stop IDs: urbanist-timetable stops search %s <words...>\n", s.ID)
				fmt.Printf("# Example stop: %s\n", s.SampleStop)
				return nil
			},
		}},
	}
}

func stopsCommand() *cli.Command {
	return &cli.Command{
		Name:  "stops",
		Usage: "find stop IDs",
		Commands: []*cli.Command{{
			Name:      "search",
			Usage:     "search a provider's stops by name or ID",
			ArgsUsage: "<provider> <words...>",
			Action: func(ctx context.Context, c *cli.Command) error {
				if c.Args().Len() < 2 {
					return errors.New("usage: urbanist-timetable stops search <provider> <words...>")
				}
				name := c.Args().First()
				search := strings.Join(c.Args().Tail(), " ")
				p, err := providerForSearch(c.String("config"), name)
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
				if len(stops) == 0 {
					fmt.Println("no stops found")
				}
				for _, s := range stops {
					fmt.Printf("%-10s %s\n", s.ID, s.Name)
				}
				return nil
			},
		}},
	}
}

// providerForSearch uses the named provider's settings from the config
// file. Without a matching block, name may be a catalog system or a
// provider type that needs no settings, so search works before a config
// exists. A config file that exists but does not load is an error, since
// silently searching with other settings would mislead.
func providerForSearch(path, name string) (transit.Provider, error) {
	raw := map[string]any{}
	if _, ok := catalog.Get(name); ok {
		raw["system"] = name
	}
	cfg, err := config.Load(path)
	switch {
	case err == nil:
		if r, ok := cfg.Providers[name]; ok {
			raw = r
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	typ, opts, err := config.ResolveProvider(name, raw)
	if err != nil {
		return nil, err
	}
	return providers.Registry().New(typ, opts)
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
