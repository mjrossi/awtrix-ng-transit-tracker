// Package render turns departure rows into AWTRIX NG payloads.
package render

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
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
	// AccentColor marks live minutes, and nothing else.
	AccentColor string
	// RuleColor is for decoration that carries no meaning: the hairline
	// under the destination and the dots between times.
	RuleColor string
	// EmptyText is shown when there are no departures.
	EmptyText string
}

// Defaults for Options. The colors are the Night theme of the maintainer's
// Lexicon palette: on an LED matrix the unlit pixels are the background,
// so a light theme does not apply.
const (
	DefaultFont        = "small"
	DefaultTextColor   = "#EAE4D9"
	DefaultDimColor    = "#A0998E"
	DefaultAccentColor = "#D9955A"
	DefaultRuleColor   = "#5B4535"
	DefaultEmptyText   = "No departures"
)

func (o Options) withDefaults() Options {
	o.Layout = cmp.Or(o.Layout, LayoutDeparture)
	o.Overflow = cmp.Or(o.Overflow, OverflowScroll)
	o.Font = cmp.Or(o.Font, DefaultFont)
	o.TextColor = cmp.Or(o.TextColor, DefaultTextColor)
	o.DimColor = cmp.Or(o.DimColor, DefaultDimColor)
	o.AccentColor = cmp.Or(o.AccentColor, DefaultAccentColor)
	o.RuleColor = cmp.Or(o.RuleColor, DefaultRuleColor)
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
	// timeGap is the blank columns between two times: the glyph's own
	// trailing column plus the two-space spacer.
	timeGap = 1 + 2*spaceWidth
)

// Rows of an 8 px row that the drawn marks use. The small font's capitals
// fill rows 1 to 5 (the firmware's "where text sits" table), which leaves
// row 0 for the live dot and row 7 for the hairline.
const (
	topRow  = 0
	midRow  = 3
	ruleRow = rowHeight - 1
)

// marks reports whether the live dots, separators and strikes are drawn.
// They are placed for the small font's 4 px cells and rows; other fonts
// would put them on top of the glyphs.
func (o Options) marks() bool { return o.Font == DefaultFont }

// unlit is drawn as black, which on an LED matrix is a pixel left off. A
// badge number in it is cut out of the badge's fill.
const unlit = "#000000"

// TC002 renders rows for the 52 × 16 display. rows should be ordered
// soonest first, as transit.Group returns them.
func TC002(rows []transit.Row, now time.Time, opts Options) *awtrix.Layout {
	opts = opts.withDefaults()
	if len(rows) == 0 {
		return &awtrix.Layout{Version: 1, Regions: []awtrix.Region{{
			ID:       "empty",
			Box:      awtrix.Box{0, 0, awtrix.TC002Width, awtrix.TC002Height},
			Text:     awtrix.PlainText(opts.EmptyText),
			Font:     opts.Font,
			Color:    opts.DimColor,
			Scroll:   opts.scroll(),
			TextCase: awtrix.TextCaseAsTyped,
		}}}
	}
	shown := Visible(rows, opts)
	if !opts.marks() {
		// Without its strike a canceled trip reads as one to catch.
		for i, r := range shown {
			shown[i].Departures = slices.DeleteFunc(slices.Clone(r.Departures), func(d transit.Departure) bool {
				return d.Status == transit.StatusCanceled
			})
		}
	}
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
	y := awtrix.TC002Height - rowHeight
	badge, bw := badgeRegions("badge", y, r.Line, opts)
	tx := bw + gap
	// The destination has its own row here, so "Due" costs it nothing.
	times := fitTimes(r.Departures, now, opts, awtrix.TC002Width-tx, true)
	regions := []awtrix.Region{
		opts.nameRegion("sign", awtrix.Box{0, 0, awtrix.TC002Width, rowHeight}, r.Headsign),
		{
			ID:   "rule",
			Box:  awtrix.Box{0, ruleRow, awtrix.TC002Width, 1},
			Draw: times.rule(tx, opts),
		},
	}
	regions = append(regions, badge...)
	regions = append(regions, times.regions("min", "marks", tx, y, opts)...)
	return &awtrix.Layout{Version: 1, Regions: regions}
}

// rowsLayout shows one line and direction per row.
func rowsLayout(rows []transit.Row, now time.Time, opts Options) *awtrix.Layout {
	l := &awtrix.Layout{Version: 1}
	// A single row sits in the middle of the display.
	y := (awtrix.TC002Height - len(rows)*rowHeight) / 2
	for i, r := range rows {
		id := func(part string) string { return fmt.Sprintf("r%d-%s", i, part) }
		badge, bw := badgeRegions(id("badge"), y, r.Line, opts)
		sx := bw + gap
		name := opts.label(r.Headsign)

		// Times take what they need, up to half the display, but a second
		// time is shown only if the name still fits beside it: a name
		// that scrolls is harder to read than a later departure is to
		// miss. Canceled trips before the first one a rider can catch
		// don't count, so they never push it out.
		deps := r.Departures
		keep := 1 + slices.IndexFunc(deps, func(d transit.Departure) bool {
			return d.Status != transit.StatusCanceled
		})
		var times timeRow
		var mx int
		for {
			times = fitTimes(deps, now, opts, awtrix.TC002Width/2, false)
			mx = awtrix.TC002Width - max(times.width, charWidth)
			if len(times.items) <= max(keep, 1) || TextWidth(name) <= mx-gap-sx {
				break
			}
			deps = deps[:len(times.items)-1]
		}

		l.Regions = append(l.Regions, badge...)
		l.Regions = append(l.Regions, opts.nameRegion(id("sign"), awtrix.Box{sx, y, mx - gap - sx, rowHeight}, r.Headsign))
		l.Regions = append(l.Regions, times.regions(id("min"), id("marks"), awtrix.TC002Width-times.width, y, opts)...)
		y += rowHeight
	}
	return l
}

func (o Options) nameRegion(id string, box awtrix.Box, headsign string) awtrix.Region {
	return awtrix.Region{
		ID:       id,
		Box:      box,
		Text:     awtrix.PlainText(o.label(headsign)),
		Font:     o.Font,
		Color:    o.TextColor,
		Align:    awtrix.AlignStart,
		Scroll:   o.scroll(),
		TextCase: awtrix.TextCaseAsTyped,
	}
}

// badgeRegions returns the regions identifying a line and their width.
//
// Metro lines, which riders know by color, get a color bar. Other lines
// get their short name cut out of a block of the line's color, like a
// route sign; a line without a color has nothing to fill, so its name is
// plain text. A name too wide for a badge, such as a long route name
// standing in for a missing short one, gets a glyph of the line's mode
// instead: four letters of "Framingham" say less than the line's color.
func badgeRegions(id string, y int, line transit.Line, opts Options) ([]awtrix.Region, int) {
	color := line.Color
	// A glyph or bar in a color too dark to light up would not show; a
	// filled badge copes, because its number switches to cream.
	if color == "" || luminance(color) < minMarkLuminance {
		color = opts.TextColor
	}
	if line.Mode != transit.ModeMetro && line.ShortName != "" && TextWidth(line.ShortName) <= maxBadgeW {
		w := TextWidth(line.ShortName)
		name := awtrix.Region{
			ID:       id,
			Box:      awtrix.Box{0, y, w, rowHeight},
			Text:     awtrix.PlainText(line.ShortName),
			Font:     opts.Font,
			Color:    opts.TextColor,
			Align:    awtrix.AlignStart,
			Scroll:   &awtrix.Scroll{Mode: awtrix.ScrollStatic},
			TextCase: awtrix.TextCaseAsTyped,
		}
		if line.Color == "" {
			return []awtrix.Region{name}, w
		}
		// Regions are drawn in list order, later ones on top, so the name
		// follows its fill.
		name.ID, name.Box[0], name.Color = id+"-name", 1, badgeTextColor(line.Color, opts.TextColor)
		fill := awtrix.Region{
			ID:   id,
			Box:  awtrix.Box{0, y, w + 2, rowHeight},
			Draw: []awtrix.Command{awtrix.RectFill(0, 0, w+2, rowHeight-1, line.Color)},
		}
		return []awtrix.Region{fill, name}, w + 2
	}
	if g, ok := modeGlyphs[line.Mode]; ok {
		return []awtrix.Region{{
			ID:   id,
			Box:  awtrix.Box{0, y, glyphWidth, rowHeight},
			Draw: []awtrix.Command{g.command(color)},
		}}, glyphWidth
	}
	return []awtrix.Region{{
		ID:   id,
		Box:  awtrix.Box{0, y, barWidth, rowHeight},
		Draw: []awtrix.Command{awtrix.RectFill(0, 1, barWidth, rowHeight-2, color)},
	}}, barWidth
}

// badgeTextColor picks whichever of unlit and text contrasts more with
// fill. It ignores agencies' route_text_color: some pair white with a
// bright yellow, which on LEDs is two bright colors side by side.
// minMarkLuminance is the relative luminance below which a line color is
// treated as unlit.
const minMarkLuminance = 0.01

func badgeTextColor(fill, text string) string {
	if contrast(fill, unlit) >= contrast(fill, text) {
		return unlit
	}
	return text
}

// contrast is the WCAG contrast ratio of two "#RRGGBB" colors.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

func luminance(hex string) float64 {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		return 0
	}
	ch := func(shift uint) float64 {
		c := float64((v>>shift)&0xFF) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(16) + 0.7152*ch(8) + 0.0722*ch(0)
}

const glyphWidth = 5

// glyph is pixel art on rows 1 to 5 of a row, where the capitals sit.
type glyph [5]string

func (g glyph) command(color string) awtrix.Command {
	var xy []int
	for r, line := range g {
		for x, c := range line {
			if c == '#' {
				xy = append(xy, x, 1+r)
			}
		}
	}
	return awtrix.Pixels(color, xy...)
}

var modeGlyphs = map[transit.Mode]glyph{
	transit.ModeBus:   {".....", "####.", "#.#.#", "#####", ".#.#."},
	transit.ModeTram:  {"..#..", "#####", "#.#.#", "#####", ".#.#."},
	transit.ModeRail:  {".###.", "#.#.#", "#####", "#.#.#", "#...#"},
	transit.ModeFerry: {"..#..", ".###.", "#####", ".###.", "#.#.#"},
}

// timeRow is the departure times that fit in a row, laid out from x = 0.
type timeRow struct {
	items []timeItem
	// width includes the live dot or strike overhang after the last time.
	width int
}

type timeItem struct {
	text     string
	x        int
	color    string
	live     bool
	canceled bool
}

// fitTimes keeps as many departures as fit in width pixels, so times never
// scroll or get cut. due shows a departure under a minute away as "Due".
func fitTimes(deps []transit.Departure, now time.Time, opts Options, width int, due bool) timeRow {
	var t timeRow
	end := 0
	for _, d := range deps {
		m := d.MinutesUntil(now)
		text := strconv.Itoa(m)
		if due && m == 0 {
			text = "Due"
		}
		x := 0
		if len(t.items) > 0 {
			x = end + timeGap
		}
		canceled := d.Status == transit.StatusCanceled
		live := d.Realtime() && !canceled
		w := x + TextWidth(text)
		if (live || canceled) && opts.marks() {
			// Room for the live dot, or for the strike's overhang.
			w++
		}
		if w > width {
			break
		}
		color := opts.DimColor
		if live {
			color = opts.TextColor
		}
		t.items = append(t.items, timeItem{text: text, x: x, color: color, live: live, canceled: canceled})
		end, t.width = x+TextWidth(text), w
	}
	return t
}

// rule draws the hairline under the destination, broken around the live
// dots of times at x so a dot on the row below does not merge into it.
func (t timeRow) rule(x int, opts Options) []awtrix.Command {
	var cmds []awtrix.Command
	from := 0
	for _, it := range t.items {
		if !it.live || !opts.marks() {
			continue
		}
		dot := x + it.x + TextWidth(it.text)
		if dot-1 > from {
			cmds = append(cmds, awtrix.RectFill(from, 0, dot-1-from, 1, opts.RuleColor))
		}
		from = dot + 2
	}
	if from < awtrix.TC002Width {
		cmds = append(cmds, awtrix.RectFill(from, 0, awtrix.TC002Width-from, 1, opts.RuleColor))
	}
	return cmds
}

// regions returns the times as one text region at x, plus a drawing for
// the live dots, strikes and separators when there are any. The drawing
// starts a column left of the times so a strike can overhang the first.
func (t timeRow) regions(textID, marksID string, x, y int, opts Options) []awtrix.Region {
	var frags []awtrix.Fragment
	var marks []awtrix.Command
	for i, it := range t.items {
		if i > 0 {
			frags = append(frags, awtrix.Fragment{Text: spacer})
		}
		frags = append(frags, awtrix.Fragment{Text: it.text, Color: it.color})
		mx, w := 1+it.x, TextWidth(it.text)
		if it.live {
			marks = append(marks, awtrix.Pixel(mx+w, topRow, opts.AccentColor))
		}
		if it.canceled {
			marks = append(marks, awtrix.RectFill(mx-1, midRow, w+2, 1, opts.TextColor))
		}
		if i < len(t.items)-1 {
			marks = append(marks, awtrix.Pixel(mx+w+timeGap/2, midRow, opts.RuleColor))
		}
	}
	regions := []awtrix.Region{{
		ID:       textID,
		Box:      awtrix.Box{x, y, max(t.width, 1), rowHeight},
		Text:     awtrix.Fragments(frags...),
		Font:     opts.Font,
		Align:    awtrix.AlignStart,
		Scroll:   &awtrix.Scroll{Mode: awtrix.ScrollStatic},
		TextCase: awtrix.TextCaseAsTyped,
	}}
	if len(marks) > 0 && opts.marks() {
		regions = append(regions, awtrix.Region{
			ID:   marksID,
			Box:  awtrix.Box{x - 1, y, min(t.width+2, awtrix.TC002Width-x+1), rowHeight},
			Draw: marks,
		})
	}
	return regions
}

// spacer separates departure times. Two font spaces rather than one, so
// "3 8 18" reads as three numbers, with room for a dot between them.
const spacer = "  "

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
