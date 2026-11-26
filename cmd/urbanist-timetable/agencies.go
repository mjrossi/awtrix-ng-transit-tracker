package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/directory"
	"github.com/mjrossi/urbanist-timetable/internal/termout"
)

func agenciesCommand() *cli.Command {
	return &cli.Command{
		Name:  "agencies",
		Usage: "find the transit system of an agency",
		Commands: []*cli.Command{{
			Name:      "search",
			Usage:     "search agencies by name or place, for the systems that carry them",
			ArgsUsage: "<words...>",
			Description: "Searches the verified systems, the Mobility Database's list of feeds, and,\n" +
				"with " + directory.TransitlandKeyVar + " set, Transitland's operators. Each result is an agency\n" +
				"and the system that carries it, whose ID works in `departures`, `stops search`\n" +
				"and `systems show`. A regional or national feed carries many agencies, so its\n" +
				"ID can appear on several rows. The agency is whoever published the data, so\n" +
				"if a name finds nothing, try the city. Countrywide feeds, such as national\n" +
				"railways, are found by the country's name.\n\n" +
				"   urbanist-timetable agencies search lisbon\n" +
				"   urbanist-timetable agencies search metro-north",
			Flags:  []cli.Flag{&cli.BoolFlag{Name: "json", Usage: "print JSON instead of text"}},
			Action: searchAgencies,
		}},
	}
}

func searchAgencies(ctx context.Context, c *cli.Command) error {
	if !c.Args().Present() {
		return newUsageError(c, "give an agency or city to search for, such as `agencies search lisbon`")
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
		fmt.Fprintln(w, "no agencies found; try the city or region, or the country for national railways and other countrywide feeds")
	}
	if len(res.Systems) > 0 {
		termout.Table(w, systemRows(res.Systems, false))
	}
	if res.More > 0 {
		fmt.Fprintf(ew, "%d more; add words to narrow the search\n", res.More)
	}
	if res.NoTransitland {
		fmt.Fprintf(ew, "set %s to also search Transitland\n", directory.TransitlandKeyVar)
	}
	if hasUnsure(res.Systems) {
		fmt.Fprintln(ew, unsureNote)
	}
	if len(res.Systems) > 0 && isTerminal(c.Root().Writer) {
		fmt.Fprintf(ew, "\nFind a stop: urbanist-timetable stops search %s <words...>\n", res.Systems[0].ID)
	}
}

// unsureNote explains dataLabel's "realtime?". A search row cannot tell
// whether the system will read the agency's feeds directly or through
// Transitland: working that out costs requests, so `systems show` does it.
const unsureNote = "realtime? = the agency publishes predictions; read through Transitland they may be missing, and `systems show` tells which way a system is read; departures mark schedule-only times with *"

// dataLabel names what departures a system gives. Through Transitland,
// that the agency publishes predictions does not mean Transitland merges
// them: the CTA's reach Transitland, yet its departures are all scheduled.
func dataLabel(d directory.Data, viaTransitland bool) string {
	if d == directory.DataRealtime && viaTransitland {
		return "realtime?"
	}
	return string(d)
}

func hasUnsure(systems []directory.System) bool {
	return slices.ContainsFunc(systems, func(s directory.System) bool {
		return dataLabel(s.Data, s.Source == directory.SourceTransitland) != string(s.Data)
	})
}

// systemRows lays out search results agency first, each with the system
// that carries it, under a header; numbered adds a column for picking one.
func systemRows(systems []directory.System, numbered bool) [][]string {
	uses := map[string]int{}
	name := func(s directory.System) string {
		if strings.EqualFold(s.Name, s.Agency) {
			return ""
		}
		return s.Name
	}
	named := false
	for _, s := range systems {
		uses[s.ID]++
		named = named || name(s) != ""
	}
	rows := [][]string{{"AGENCY", "SYSTEM", "NAME", "PLACE", "DATA", "NOTES"}}
	for _, s := range systems {
		note := systemNote(s)
		if uses[s.ID] > 1 {
			// A regional or national feed: picking any of these rows
			// gives the same system, with every agency's stops.
			note = strings.TrimPrefix(note+", shared feed", ", ")
		}
		rows = append(rows, []string{shorten(s.Agency, 40), s.ID, shorten(name(s), 28), shorten(s.Place, 30), dataLabel(s.Data, s.Source == directory.SourceTransitland), note})
	}
	if !named {
		for i, r := range rows {
			rows[i] = append(r[:2], r[3:]...)
		}
	}
	if numbered {
		for i := range rows {
			n := ""
			if i > 0 {
				n = strconv.Itoa(i)
			}
			rows[i] = append([]string{"  " + n}, rows[i]...)
		}
	}
	return rows
}

func systemNote(s directory.System) string {
	var notes []string
	if s.Source == directory.SourceCatalog {
		notes = append(notes, "verified")
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
