package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/directory"
	"github.com/mjrossi/urbanist-timetable/internal/termout"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

func agenciesCommand() *cli.Command {
	return &cli.Command{
		Name:  "agencies",
		Usage: "find transit systems beyond the ready-made ones",
		Commands: []*cli.Command{{
			Name:      "search",
			Usage:     "search agencies and places for systems",
			ArgsUsage: "<words...>",
			Description: "Searches the ready-made systems, the Mobility Database's list of feeds, and,\n" +
				"with " + directory.TransitlandKeyVar + " set, Transitland's operators. Each result is a system\n" +
				"whose ID works in `departures`, `stops search` and `systems show`.\n" +
				"The agency is whoever published the data, so search by place too.\n\n" +
				"   urbanist-timetable agencies search lisbon\n" +
				"   urbanist-timetable agencies search metro-north",
			Flags: []cli.Flag{&cli.BoolFlag{Name: "json", Usage: "print JSON instead of text"}},
			Action: func(ctx context.Context, c *cli.Command) error {
				if !c.Args().Present() {
					return newUsageError(c, "give words to search for, such as `agencies search lisbon`")
				}
				res, err := newDirectory(c).Search(ctx, strings.Join(c.Args().Slice(), " "))
				for _, p := range res.Problems {
					fmt.Fprintf(c.Root().ErrWriter, "warning: %s\n", termout.Clean(p.Error()))
				}
				if err != nil && len(res.Systems) == 0 {
					return err
				}
				if c.Bool("json") {
					return writeSystemsJSON(c.Root().Writer, res.Systems)
				}
				writeSystems(c, res)
				return nil
			},
		}},
	}
}

func writeSystemsJSON(w io.Writer, systems []directory.System) error {
	if systems == nil {
		systems = []directory.System{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(systems)
}

func writeSystems(c *cli.Command, res directory.Results) {
	w, ew := c.Root().Writer, c.Root().ErrWriter
	if len(res.Systems) == 0 {
		fmt.Fprintln(w, "no systems found; try the city or region instead")
	}
	rows := make([][]string, 0, len(res.Systems))
	for _, s := range res.Systems {
		rows = append(rows, []string{s.ID, shorten(s.Title(), 44), shorten(s.Place, 32), string(s.Data), systemNote(s)})
	}
	termout.Table(w, rows)
	if res.More > 0 {
		fmt.Fprintf(ew, "%d more; add words to narrow the search\n", res.More)
	}
	if res.NoTransitland {
		fmt.Fprintf(ew, "set %s to also search Transitland\n", directory.TransitlandKeyVar)
	}
	if len(res.Systems) > 0 && isTerminal(c.Root().Writer) {
		fmt.Fprintf(ew, "\nFind a stop: urbanist-timetable stops search %s <words...>\n", res.Systems[0].ID)
	}
}

func systemNote(s directory.System) string {
	var notes []string
	if s.Source == directory.SourceCatalog {
		notes = append(notes, "ready-made")
	}
	if s.Source == directory.SourceTransitland {
		notes = append(notes, "via Transitland")
	}
	var needs []string
	for _, v := range s.Needs {
		if os.Getenv(v) != "" {
			v += " (set)"
		}
		needs = append(needs, v)
	}
	if len(needs) > 0 {
		notes = append(notes, "needs "+strings.Join(needs, ", "))
	}
	return strings.Join(notes, ", ")
}

// shorten cuts s to n characters, ending in "…" when cut.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// newDirectory searches with the cache directory and Transitland key the
// command line and environment give.
func newDirectory(c *cli.Command) *directory.Directory {
	dir := c.String("cache-dir")
	if dir == "off" {
		dir = ""
	}
	return &directory.Directory{
		CacheDir: dir, TransitlandKey: os.Getenv(directory.TransitlandKeyVar), Log: newLogger(c), Now: clock,
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
	settings, err := config.ExpandSettings(id, b.Settings())
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
	fmt.Fprintf(w, " (%s)\n# %s\n", sys.Data, termout.Clean(b.Comment))
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
	block, err := directoryBlock(id, b)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%s", block)
	fmt.Fprintf(w, "\n# Find stop IDs: urbanist-timetable stops search %s <words...>\n", id)
	return nil
}

// directoryBlock renders b as a [providers] block named name.
func directoryBlock(name string, b directory.Block) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[providers.%s]\n", tomlKey(name))
	settings := b.Settings()
	keys := []string{"type", "static_url", "realtime_urls", "feed", "api_key", "headers"}
	for _, k := range keys {
		v, ok := settings[k]
		if !ok {
			continue
		}
		s, err := tomlValue(v)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "%s = %s\n", k, s)
	}
	return sb.String(), nil
}

// tomlKey quotes a key that is not a bare TOML key, such as one with "~".
func tomlKey(k string) string {
	for _, r := range k {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return tomlString(k)
		}
	}
	return k
}

func tomlValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return tomlString(x), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, err := tomlValue(e)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		var parts []string
		for _, k := range slices.Sorted(maps.Keys(x)) {
			s, err := tomlValue(x[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, tomlString(k)+" = "+s)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	}
	return "", fmt.Errorf("cannot write %T as TOML", v)
}
