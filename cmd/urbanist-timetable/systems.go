package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/directory"
	"github.com/mjrossi/urbanist-timetable/internal/termout"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

func systemsCommand() *cli.Command {
	return &cli.Command{
		Name:  "systems",
		Usage: "list the verified transit systems, or show how to configure one",
		Description: "A system is one schedule an agency publishes, with its realtime feeds. Its ID\n" +
			"works in `departures` and `stops search` without a configuration file. Without\n" +
			"a subcommand, lists the verified systems: checked against their live feeds,\n" +
			"with short IDs. Those that need keys read them from the variables listed.\n" +
			"`agencies search` finds the system of any other agency.\n\n" +
			"   urbanist-timetable systems show mta-subway\n" +
			"   urbanist-timetable systems show mdb-2929",
		Action: listSystems,
		Commands: []*cli.Command{
			{
				Name:      "show",
				Usage:     "print a system's details and a config snippet",
				ArgsUsage: "<id>",
				Description: "Prints what a system covers, the environment variables it needs,\n" +
					"and a [providers] block to paste into the configuration. Takes any\n" +
					"system ID, verified or from `agencies search`.\n\n" +
					"   urbanist-timetable systems show mta-subway\n" +
					"   urbanist-timetable systems show mdb-2929",
				ShellComplete: completeFirst(completeSystems),
				Action:        showSystem,
			},
		},
	}
}

func listSystems(_ context.Context, c *cli.Command) error {
	if c.Args().Present() {
		return unknownSystemsArgument(c)
	}
	w := c.Root().Writer
	all, err := catalog.All()
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "Verified systems (for any other agency: urbanist-timetable agencies search <agency or city>)")
	for _, s := range all {
		needs := ""
		if len(s.RequiredOptions) > 0 {
			vars := make([]string, len(s.RequiredOptions))
			for i, k := range s.RequiredOptions {
				vars[i] = s.EnvName(k)
				if os.Getenv(vars[i]) != "" {
					vars[i] += " (set)"
				}
			}
			needs = "  needs " + strings.Join(vars, ", ")
		}
		fmt.Fprintf(w, "%-28s %-22s %-24s %s%s\n", s.ID, s.Title(), s.City, strings.Join(s.Modes, ", "), needs)
	}
	fmt.Fprintln(w, "\nShow how to configure one: urbanist-timetable systems show <id>")
	return nil
}

func showSystem(ctx context.Context, c *cli.Command) error {
	if c.Args().Len() != 1 {
		return newUsageError(c, "give one system ID, such as `systems show bart`")
	}
	id := c.Args().First()
	s, ok := catalog.Get(id)
	if !ok && directory.IsID(id) {
		return showDirectorySystem(ctx, c, id)
	}
	if !ok {
		return unknownSystemError(id)
	}
	w := c.Root().Writer
	fmt.Fprintf(w, "# %s, %s (%s)\n", s.Title(), s.City, s.Country)
	fmt.Fprintf(w, "# Status: %s, last verified %s. Data: %s.\n", s.Status, s.Verified, s.DataLicense)
	fmt.Fprintf(w, "# More: %s\n", s.InfoURL)
	for line := range strings.SplitSeq(strings.TrimSpace(s.Notes), "\n") {
		fmt.Fprintf(w, "# %s\n", line)
	}
	fmt.Fprintf(w, "\n%s", providerBlock(s))
	fmt.Fprintf(w, "\n# Find stop IDs: urbanist-timetable stops search %s <words...>\n", s.ID)
	fmt.Fprintf(w, "# Example stop: %s\n", s.SampleStop)
	return nil
}

func unknownSystemError(id string) error {
	if to, ok := catalog.Renamed(id); ok {
		return fmt.Errorf("%w %q: it was renamed %q", catalog.ErrUnknownSystem, id, to)
	}
	return fmt.Errorf("%w %q%s", catalog.ErrUnknownSystem, id, didYouMean(id, systemIDs()))
}

// unknownSystemsArgument answers `systems ID`, which is most often meant as
// `systems show ID`.
func unknownSystemsArgument(c *cli.Command) error {
	arg := c.Args().First()
	if _, ok := catalog.Get(arg); ok && c.Args().Len() == 1 {
		return newUsageError(c, "unknown command %q; did you mean `systems show %s`?", arg, arg)
	}
	return unknownCommandError(c, arg)
}

// newDirectory searches with the cache directory and Transitland key the
// command line and environment give.
func newDirectory(c *cli.Command) *directory.Directory {
	return &directory.Directory{
		CacheDir: cacheDir(c), TransitlandKey: os.Getenv(directory.TransitlandKeyVar), Log: newLogger(c), Now: clock,
		MobilityDBURL: directoryURLs.mobilityDB, TransitlandURL: directoryURLs.transitland,
	}
}

// directoryURLs replace the directories' addresses in tests.
var directoryURLs struct{ mobilityDB, transitland string }

// openDirectorySystem builds the provider for a directory ID, reading the
// keys its block needs from the environment.
func openDirectorySystem(ctx context.Context, dir *directory.Directory, id string, opts ...providers.Option) (transit.Provider, error) {
	sys, b, err := dir.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	settings, err := config.ExpandSettings(id, b.Settings(), sys.Needs)
	if err != nil {
		if sys.KeyInfo != "" {
			return nil, fmt.Errorf("%w (get a key: %s)", err, sys.KeyInfo)
		}
		return nil, err
	}
	return providers.Open(id, settings, opts...)
}

// showDirectorySystem prints a directory system's block for `systems show`.
func showDirectorySystem(ctx context.Context, c *cli.Command, id string) error {
	dir := newDirectory(c)
	sys, b, err := dir.Resolve(ctx, id)
	if err != nil {
		return err
	}
	w := c.Root().Writer
	fmt.Fprintf(w, "# %s", termout.Clean(sys.Title()))
	if sys.Place != "" {
		fmt.Fprintf(w, ", %s", termout.Clean(sys.Place))
	}
	fmt.Fprintf(w, " (%s)\n# %s\n", dataLabel(sys.Data, b.Type == "transitland"), termout.Clean(b.Comment))
	if len(sys.Needs) > 0 {
		fmt.Fprintf(w, "# Needs %s", strings.Join(sys.Needs, ", "))
		if sys.KeyInfo != "" {
			fmt.Fprintf(w, "; get a key at %s", termout.Clean(sys.KeyInfo))
		}
		fmt.Fprintln(w)
	}
	if sys.StaticURL != "" {
		if n, ok := dir.ScheduleSize(ctx, sys.StaticURL); ok && n > directory.LargeSchedule {
			fmt.Fprintf(w, "# The schedule is %d MB, which a small machine such as a NAS may struggle to load.\n", n>>20)
		}
	}
	block, err := b.TOML(id)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%s", block)
	fmt.Fprintf(w, "\n# Find stop IDs: urbanist-timetable stops search %s <words...>\n", id)
	return nil
}
