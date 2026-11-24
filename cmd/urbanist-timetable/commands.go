package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
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
			log.InfoContext(ctx, "starting", slog.Int("boards", len(runners)), slog.String("clock", clockHost(cfg.Clock.URL)))
			return scheduler.RunAll(ctx, runners)
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
				now := instant.In(cfg.Boards[i].Location())
				snap, ok := r.Snapshot(ctx, now)
				if !ok {
					return fmt.Errorf("board %s: every source failed", cfg.Boards[i].Name)
				}
				app, err := out.App(snap)
				if err != nil {
					return err
				}
				if !c.Bool("json") {
					printRows(os.Stdout, cfg.Boards[i], snap.Rows(now, true), now)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(app); err != nil {
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

// clockHost is the clock URL's host, for logs: the URL may embed
// basic-auth credentials, which the clock client accepts.
func clockHost(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return "(invalid URL)"
	}
	return p.Host
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
	fmt.Fprintf(w, "# %s (%s layout) at %s\n", b.Name, b.Clock.Layout, now.Format("15:04:05 MST"))
	if len(rows) == 0 {
		fmt.Fprintln(w, "#   (no departures)")
	}
	scrolls := false
	opts := b.Clock.Render()
	visible := render.Visible(rows, opts)
	for _, r := range rows {
		mins := make([]string, len(r.Departures))
		for i, d := range r.Departures {
			mins[i] = strconv.Itoa(d.MinutesUntil(now))
			if !d.Realtime() {
				mins[i] += "*"
			}
		}
		name := opts.Label(r.Headsign)
		mark := ""
		switch {
		case !slices.ContainsFunc(visible, func(v transit.Row) bool { return v.Line.ID == r.Line.ID && v.Headsign == r.Headsign }):
			mark = " (not shown)"
		case b.Clock.Layout == render.LayoutDeparture && !render.Fits(name):
			mark, scrolls = " ~", true
		}
		fmt.Fprintf(w, "#   %-8s %-24s %s%s\n", r.Line.ShortName, name, strings.Join(mins, " "), mark)
	}
	fmt.Fprintln(w, "#   (* = schedule only)")
	if scrolls {
		fmt.Fprintln(w, "#   (~ = too wide for the display; set a shorter name under [boards.clock.labels])")
	}
}
