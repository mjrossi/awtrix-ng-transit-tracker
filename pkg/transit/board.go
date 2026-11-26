package transit

import (
	"slices"
	"strings"
	"time"
)

// Filter narrows a stop's departures to the ones a rider cares about.
// Zero values match everything.
type Filter struct {
	// Lines keeps only these line IDs or short names (case-insensitive).
	Lines []string
	// Headsigns keeps only departures whose headsign contains one of these
	// substrings (case-insensitive).
	Headsigns []string
	// MinLead drops departures sooner than this, such as the walk to the
	// stop.
	MinLead time.Duration
	// KeepCanceled keeps canceled departures instead of dropping them.
	KeepCanceled bool
}

// Narrows reports whether f asks for less than every departure: lines,
// headsigns or a minimum lead. Dropping canceled departures, the default,
// does not count.
func (f Filter) Narrows() bool {
	return len(f.Lines) > 0 || len(f.Headsigns) > 0 || f.MinLead > 0
}

// Apply returns the departures that pass the filter, in their input order.
func (f Filter) Apply(now time.Time, deps []Departure) []Departure {
	out := make([]Departure, 0, len(deps))
	for _, d := range deps {
		if f.keep(now, d) {
			out = append(out, d)
		}
	}
	return out
}

func (f Filter) keep(now time.Time, d Departure) bool {
	if d.Status == StatusCanceled && !f.KeepCanceled {
		return false
	}
	if d.Time().Sub(now) < f.MinLead {
		return false
	}
	if len(f.Lines) > 0 && !slices.ContainsFunc(f.Lines, func(l string) bool {
		return strings.EqualFold(l, d.Line.ID) || strings.EqualFold(l, d.Line.ShortName)
	}) {
		return false
	}
	if len(f.Headsigns) > 0 && !slices.ContainsFunc(f.Headsigns, func(h string) bool {
		return strings.Contains(strings.ToLower(d.Headsign), strings.ToLower(h))
	}) {
		return false
	}
	return true
}

// Row is one line and direction with its upcoming departures, which is what
// a display shows on one row.
type Row struct {
	Line       Line
	Headsign   string
	Departures []Departure
}

// Group merges departures from any number of stops into rows keyed by line
// and headsign. Rows are ordered by their soonest departure, each row holds
// at most perRow departures (zero means no cap), and at most maxRows rows
// are returned (zero means no cap).
func Group(deps []Departure, maxRows, perRow int) []Row {
	sorted := slices.Clone(deps)
	SortByTime(sorted)

	type key struct{ line, headsign string }
	index := map[key]int{}
	var rows []Row
	for _, d := range sorted {
		k := key{d.Line.ID, d.Headsign}
		i, ok := index[k]
		if !ok {
			i = len(rows)
			index[k] = i
			rows = append(rows, Row{Line: d.Line, Headsign: d.Headsign})
		}
		if perRow == 0 || len(rows[i].Departures) < perRow {
			rows[i].Departures = append(rows[i].Departures, d)
		}
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}
