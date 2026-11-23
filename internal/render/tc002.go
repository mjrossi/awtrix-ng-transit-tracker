// Package render turns departure rows into AWTRIX NG payloads.
package render

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/awtrix"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit"
)

// Layouts.
const (
	// LayoutDeparture shows one line and direction like a platform sign:
	// the destination across the top, the line and next times below.
	LayoutDeparture = "departure"
	// LayoutRows shows one line and direction per row, two rows at most.
	LayoutRows = "rows"
)

// Overflow modes for names wider than their box.
const (
	OverflowScroll = "scroll"
	OverflowClip   = "clip"
)

// Options tunes the TC002 renderer. Zero values take the defaults below.
type Options struct {
	Layout string
	// MaxRows caps rows in LayoutRows; the display fits two.
	MaxRows int
	// Labels replaces headsigns for display, such as
	// "Santa Apolónia" → "Sta Apolónia".
	Labels map[string]string
	// Overflow says what a name wider than its box does.
	Overflow string
	// Font is an AWTRIX NG font name; see GET /api/v1/capabilities.
	Font string
	// TextColor is used for names and live minutes.
	TextColor string
	// DimColor is used for schedule-only minutes and the empty message.
	DimColor string
	// EmptyText is shown when there are no departures.
	EmptyText string
}

// Defaults for Options.
const (
	DefaultFont      = "small"
	DefaultTextColor = "#FFFFFF"
	DefaultDimColor  = "#7F7F7F"
	DefaultEmptyText = "No departures"
)

func (o Options) withDefaults() Options {
	o.Layout = cmp.Or(o.Layout, LayoutDeparture)
	o.Overflow = cmp.Or(o.Overflow, OverflowScroll)
	o.Font = cmp.Or(o.Font, DefaultFont)
	o.TextColor = cmp.Or(o.TextColor, DefaultTextColor)
	o.DimColor = cmp.Or(o.DimColor, DefaultDimColor)
	o.EmptyText = cmp.Or(o.EmptyText, DefaultEmptyText)
	if o.MaxRows <= 0 || o.MaxRows > maxRows {
		o.MaxRows = maxRows
	}
	return o
}

const (
	rowHeight = 8
	maxRows   = awtrix.TC002Height / rowHeight
	gap       = 2
	barWidth  = 2
	maxBadgeW = 17
	// minuteGap is the space between departure times, wider than a font
	// space so "3 8 18" reads as three numbers.
	minuteGap = 4
)

// TC002 renders rows for the 52 × 16 display. rows should be ordered
// soonest first, as transit.Group returns them.
func TC002(rows []transit.Row, now time.Time, opts Options) *awtrix.Layout {
	opts = opts.withDefaults()
	if len(rows) == 0 {
		return &awtrix.Layout{Version: 1, Regions: []awtrix.Region{{
			ID:     "empty",
			Box:    awtrix.Box{0, 0, awtrix.TC002Width, awtrix.TC002Height},
			Text:   awtrix.PlainText(opts.EmptyText),
			Font:   opts.Font,
			Color:  opts.DimColor,
			Scroll: opts.scroll(),
		}}}
	}
	shown := Visible(rows, opts)
	if opts.Layout == LayoutRows {
		return rowsLayout(shown, now, opts)
	}
	return departureLayout(shown[0], now, opts)
}

// Visible returns the rows a layout shows, in display order. rows should
// be ordered soonest first, as transit.Group returns them.
//
// Rows are ordered by line and headsign rather than by time, so they do not
// swap places between pushes while the same ones are on screen. The
// departure layout shows the first such row, so a stop served in both
// directions keeps one destination instead of flipping to whichever
// departs next.
func Visible(rows []transit.Row, opts Options) []transit.Row {
	opts = opts.withDefaults()
	n := len(rows)
	if opts.Layout == LayoutRows {
		n = min(n, opts.MaxRows)
	}
	out := slices.Clone(rows[:n])
	slices.SortStableFunc(out, func(a, b transit.Row) int {
		return cmp.Or(cmp.Compare(a.Line.ShortName, b.Line.ShortName), cmp.Compare(a.Headsign, b.Headsign))
	})
	if opts.Layout != LayoutRows {
		out = out[:min(len(out), 1)]
	}
	return out
}

// departureLayout puts the destination on the top row, and the line badge
// with the next times on the bottom row.
func departureLayout(r transit.Row, now time.Time, opts Options) *awtrix.Layout {
	badge := badgeRegion("badge", awtrix.TC002Height-rowHeight, r.Line, opts)
	tx := badge.Box[2] + gap
	frags := minuteFragments(r.Departures, now, opts, awtrix.TC002Width-tx)
	return &awtrix.Layout{Version: 1, Regions: []awtrix.Region{
		{
			ID:     "sign",
			Box:    awtrix.Box{0, 0, awtrix.TC002Width, rowHeight},
			Text:   awtrix.PlainText(opts.label(r.Headsign)),
			Font:   opts.Font,
			Color:  opts.TextColor,
			Align:  awtrix.AlignStart,
			Scroll: opts.scroll(),
		},
		badge,
		{
			ID:     "min",
			Box:    awtrix.Box{tx, rowHeight, awtrix.TC002Width - tx, rowHeight},
			Text:   awtrix.Fragments(frags...),
			Font:   opts.Font,
			Align:  awtrix.AlignStart,
			Scroll: &awtrix.Scroll{Mode: awtrix.ScrollStatic},
		},
	}}
}

// rowsLayout shows one line and direction per row.
func rowsLayout(rows []transit.Row, now time.Time, opts Options) *awtrix.Layout {
	l := &awtrix.Layout{Version: 1}
	// A single row sits in the middle of the display.
	y := (awtrix.TC002Height - len(rows)*rowHeight) / 2
	for i, r := range rows {
		id := func(part string) string { return fmt.Sprintf("r%d-%s", i, part) }
		badge := badgeRegion(id("badge"), y, r.Line, opts)
		sx := badge.Box[2] + gap

		// Times take what they need, up to half the display; the name gets
		// the rest.
		frags := minuteFragments(r.Departures, now, opts, awtrix.TC002Width/2)
		mw := max(fragmentsWidth(frags), charWidth)
		mx := awtrix.TC002Width - mw

		l.Regions = append(l.Regions,
			badge,
			awtrix.Region{
				ID:     id("sign"),
				Box:    awtrix.Box{sx, y, mx - gap - sx, rowHeight},
				Text:   awtrix.PlainText(opts.label(r.Headsign)),
				Font:   opts.Font,
				Color:  opts.TextColor,
				Align:  awtrix.AlignStart,
				Scroll: opts.scroll(),
			},
			awtrix.Region{
				ID:     id("min"),
				Box:    awtrix.Box{mx, y, mw, rowHeight},
				Text:   awtrix.Fragments(frags...),
				Font:   opts.Font,
				Align:  awtrix.AlignEnd,
				Scroll: &awtrix.Scroll{Mode: awtrix.ScrollStatic},
			},
		)
		y += rowHeight
	}
	return l
}

// badgeRegion is a color bar for metro lines, which riders know by color,
// and the line's short name in its color for everything else. Names too
// wide for the badge, such as a long route name standing in for a missing
// short one, also get the bar: four letters of "Framingham" say less than
// the line's color.
func badgeRegion(id string, y int, line transit.Line, opts Options) awtrix.Region {
	color := cmp.Or(line.Color, opts.TextColor)
	if line.Mode == transit.ModeMetro || line.ShortName == "" || TextWidth(line.ShortName) > maxBadgeW {
		return awtrix.Region{
			ID:   id,
			Box:  awtrix.Box{0, y, barWidth, rowHeight},
			Draw: []awtrix.Command{awtrix.RectFill(0, 1, barWidth, rowHeight-2, color)},
		}
	}
	return awtrix.Region{
		ID:     id,
		Box:    awtrix.Box{0, y, TextWidth(line.ShortName), rowHeight},
		Text:   awtrix.PlainText(line.ShortName),
		Font:   opts.Font,
		Color:  color,
		Align:  awtrix.AlignStart,
		Scroll: &awtrix.Scroll{Mode: awtrix.ScrollStatic},
	}
}

// minuteFragments lists minutes until each departure, live predictions in
// TextColor and schedule-only times in DimColor, keeping only as many as fit
// in width pixels so times never scroll or get cut.
func minuteFragments(deps []transit.Departure, now time.Time, opts Options, width int) []awtrix.Fragment {
	var frags []awtrix.Fragment
	used := 0
	for _, d := range deps {
		s := strconv.Itoa(d.MinutesUntil(now))
		w := TextWidth(s)
		if len(frags) > 0 {
			w += minuteGap
		}
		if used+w > width {
			break
		}
		if len(frags) > 0 {
			frags = append(frags, awtrix.Fragment{Text: spacer})
		}
		c := opts.DimColor
		if d.Realtime() {
			c = opts.TextColor
		}
		frags = append(frags, awtrix.Fragment{Text: s, Color: c})
		used += w
	}
	return frags
}

// spacer is minuteGap wide: two spaces of spaceWidth.
const spacer = "  "

func fragmentsWidth(frags []awtrix.Fragment) int {
	w := 0
	for _, f := range frags {
		if f.Text == spacer {
			w += minuteGap
			continue
		}
		w += TextWidth(f.Text)
	}
	return w
}

func (o Options) label(headsign string) string {
	if l, ok := o.Labels[headsign]; ok {
		return l
	}
	return headsign
}

func (o Options) scroll() *awtrix.Scroll {
	if o.Overflow == OverflowClip {
		return &awtrix.Scroll{Mode: awtrix.ScrollStatic}
	}
	return &awtrix.Scroll{Mode: awtrix.ScrollWrap}
}

// Font metrics of AWTRIX NG's small and large fonts, from the firmware's
// visual reference: every character advances 4 px, a space 2 px. The
// Matrix fonts are proportional, so widths are approximate for them.
const (
	charWidth  = 4
	spaceWidth = 2
)

// TextWidth returns the width of s in pixels, without the trailing
// one-pixel gap after the last character.
func TextWidth(s string) int {
	w := 0
	for _, r := range s {
		if r == ' ' {
			w += spaceWidth
		} else {
			w += charWidth
		}
	}
	if w > 0 && s[len(s)-1] != ' ' {
		w--
	}
	return w
}

// Fits reports whether s fits on one full-width row without scrolling.
func Fits(s string) bool { return TextWidth(s) <= awtrix.TC002Width }
