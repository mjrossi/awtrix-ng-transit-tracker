// Package termout renders departures for a terminal, as aligned text or as
// JSON for scripts.
package termout

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// Board is what one query shows: the departures at one or more stops.
type Board struct {
	// Name is the configured board shown, or empty for stops given on the
	// command line.
	Name string
	// Source is the provider block, system or type that was queried, or a
	// comma-separated list of them.
	Source string
	// Title names the stops, or gives their IDs.
	Title  string
	At     time.Time
	Window time.Duration
	// Departures are already filtered.
	Departures []transit.Departure
	// Partial is set when part of the data could not be loaded.
	Partial bool
}

// Options shape the text output.
type Options struct {
	// Color draws each line name on its line color, for terminals.
	Color bool
	// PerRow caps departures per row and Limit caps rows; zero means no cap.
	PerRow, Limit int
}

// Text writes b as a title line, then one row per line and destination,
// soonest first, with minutes until each departure.
func Text(w io.Writer, b Board, o Options) error {
	title := Clean(b.Title)
	clock := b.At.Format("15:04")
	rows := transit.Group(b.Departures, o.Limit, o.PerRow)
	if len(rows) == 0 {
		_, err := fmt.Fprintf(w, "%s  %s\nNo departures in the next %d minutes\n%s", title, clock, int(b.Window.Minutes()), partialNote(b))
		return err
	}

	type cells struct {
		line, headsign string
		mins           []string
	}
	var (
		table                  []cells
		lineW, headW, minW     int
		scheduleOnly, canceled bool
	)
	for _, r := range rows {
		c := cells{line: Clean(cmpOr(r.Line.ShortName, r.Line.ID)), headsign: Clean(r.Headsign)}
		for _, d := range r.Departures {
			m := strconv.Itoa(d.MinutesUntil(b.At))
			switch {
			case d.Status == transit.StatusCanceled:
				m += "x"
				canceled = true
			case !d.Realtime():
				m += "*"
				scheduleOnly = true
			}
			c.mins = append(c.mins, m)
			minW = max(minW, len(m))
		}
		lineW = max(lineW, width(c.line))
		headW = max(headW, width(c.headsign))
		table = append(table, c)
	}

	var sb strings.Builder
	lines := make([]string, len(table))
	rowW := 0
	for i, c := range table {
		var row strings.Builder
		if o.Color {
			// A badge: the name padded inside its line color.
			row.WriteString(colored(" "+pad(c.line, lineW)+" ", rows[i].Line.Color) + " ")
		} else {
			row.WriteString(pad(c.line, lineW+2))
		}
		row.WriteString(pad(c.headsign, headW+2))
		for j, m := range c.mins {
			if j > 0 {
				row.WriteByte(' ')
			}
			row.WriteString(strings.Repeat(" ", minW-len(m)) + m)
		}
		lines[i] = strings.TrimRight(row.String(), " ")
		rowW = max(rowW, visibleWidth(lines[i]))
	}
	gap := max(rowW-width(title)-len(clock), 2)
	sb.WriteString(title + strings.Repeat(" ", gap) + clock + "\n")
	for _, l := range lines {
		sb.WriteString(l + "\n")
	}
	if scheduleOnly {
		sb.WriteString("* = schedule only\n")
	}
	if canceled {
		sb.WriteString("x = canceled\n")
	}
	sb.WriteString(partialNote(b))
	_, err := io.WriteString(w, sb.String())
	return err
}

// colored draws name on color with a readable foreground, or returns it
// unchanged when color is not "#RRGGBB".
func colored(name, color string) string {
	color = transit.NormalizeColor(color)
	if color == "" {
		return name
	}
	r, _ := strconv.ParseUint(color[1:3], 16, 8)
	g, _ := strconv.ParseUint(color[3:5], 16, 8)
	bl, _ := strconv.ParseUint(color[5:7], 16, 8)
	fg := "255;255;255"
	if 299*r+587*g+114*bl > 150_000 {
		fg = "0;0;0"
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm\x1b[38;2;%sm%s\x1b[0m", r, g, bl, fg, name)
}

// partialNote says on screen that data is missing, since warnings printed
// elsewhere can scroll away or be cleared by a redraw.
func partialNote(b Board) string {
	if !b.Partial {
		return ""
	}
	return "Some data could not be loaded.\n"
}

// Clean removes control and format characters, because text from feeds and
// servers is not trusted: escape sequences in it would drive the terminal,
// and bidirectional overrides would reorder what it shows.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// width counts characters, so accented names align.
func width(s string) int { return utf8.RuneCountInString(s) }

// visibleWidth is width without the escape sequences colored adds.
func visibleWidth(s string) int {
	n, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape:
			inEscape = r != 'm'
		default:
			n++
		}
	}
	return n
}

func pad(s string, n int) string { return s + strings.Repeat(" ", max(n-width(s), 0)) }

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type jsonBoard struct {
	Board      string          `json:"board,omitempty"`
	Source     string          `json:"source"`
	At         time.Time       `json:"at"`
	Partial    bool            `json:"partial"`
	Departures []jsonDeparture `json:"departures"`
}

type jsonLine struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
	Mode  string `json:"mode"`
}

type jsonDeparture struct {
	Stop      string     `json:"stop"`
	Line      jsonLine   `json:"line"`
	Headsign  string     `json:"headsign"`
	Scheduled time.Time  `json:"scheduled,omitzero"`
	Predicted *time.Time `json:"predicted,omitzero"`
	Status    string     `json:"status"`
	Minutes   int        `json:"minutes"`
}

// JSON writes b as one indented JSON object, departures soonest first. The
// shape may change before 1.0.
func JSON(w io.Writer, b Board) error {
	return encode(w, jsonFor(b))
}

// JSONList writes boards as an indented JSON array of the objects JSON
// writes.
func JSONList(w io.Writer, boards []Board) error {
	out := make([]jsonBoard, len(boards))
	for i, b := range boards {
		out[i] = jsonFor(b)
	}
	return encode(w, out)
}

func encode(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func jsonFor(b Board) jsonBoard {
	deps := slices.Clone(b.Departures)
	transit.SortByTime(deps)
	out := jsonBoard{Board: b.Name, Source: b.Source, At: b.At, Partial: b.Partial, Departures: []jsonDeparture{}}
	for _, d := range deps {
		out.Departures = append(out.Departures, jsonDeparture{
			Stop:      d.StopID,
			Line:      jsonLine{ID: d.Line.ID, Name: cmpOr(d.Line.ShortName, d.Line.ID), Color: d.Line.Color, Mode: string(d.Line.Mode)},
			Headsign:  d.Headsign,
			Scheduled: d.Scheduled,
			Predicted: d.Predicted,
			Status:    string(d.Status),
			Minutes:   d.MinutesUntil(b.At),
		})
	}
	return out
}
