// Package render turns departure rows into AWTRIX NG payloads.
package render

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// Layout is how a board's rows are arranged on the display.
type Layout string

// Layouts.
const (
	// LayoutDeparture shows one line and direction like a platform sign:
	// the destination across the top, the line and next times below.
	LayoutDeparture Layout = "departure"
	// LayoutRows shows one line and direction per row, two rows at most.
	LayoutRows Layout = "rows"
)

// Overflow is what a name wider than its box does.
type Overflow string

// Overflow modes.
const (
	OverflowScroll Overflow = "scroll"
	OverflowClip   Overflow = "clip"
)

// Options tunes the TC002 renderer. Zero values take the defaults below.
type Options struct {
	Layout Layout
	// MaxRows caps rows in LayoutRows; the display fits two.
	MaxRows int
	// Labels replaces headsigns for display, such as
	// "Santa Apolónia" → "Sta Apolónia".
	Labels   map[string]string
	Overflow Overflow
	// Font is an AWTRIX NG font name; see GET /api/v1/capabilities.
	Font string
	// NameFont is the font for destination names and EmptyText. Empty
	// picks one per name: Font, or AccentFont for a name the small font
	// draws badly.
	NameFont string
	// EmptyText is shown when there are no departures, drawn in NameFont.
	EmptyText string
}

// Defaults for Options.
const (
	DefaultFont      = "small"
	DefaultEmptyText = "No departures"
	// AccentFont replaces the small font for names with accents above
	// Latin-1 letters. The small font fits those accents by shortening the
	// letter, without a blank row between them, so "ã" reads as a blob.
	AccentFont = "matrix-light6"
)

// Colors are the Night theme of the maintainer's Lexicon palette: on an LED
// matrix the unlit pixels are the background, so a light theme does not
// apply.
const (
	// TextColor is used for names and live minutes.
	TextColor = "#EAE4D9"
	// DimColor is used for schedule-only minutes and the empty message.
	DimColor = "#A0998E"
	// AccentColor marks live minutes, and nothing else.
	AccentColor = "#D9955A"
	// RuleColor is for decoration that carries no meaning: the hairline
	// under the destination and the dots between times.
	RuleColor = "#5B4535"
)

// Validate reports a layout, overflow mode or row count the renderer does
// not support. Empty values are valid and take the defaults.
func (o Options) Validate() error {
	var errs []error
	if o.Layout != "" && o.Layout != LayoutDeparture && o.Layout != LayoutRows {
		errs = append(errs, fmt.Errorf("layout %q must be %q or %q", o.Layout, LayoutDeparture, LayoutRows))
	}
	if o.Overflow != "" && o.Overflow != OverflowScroll && o.Overflow != OverflowClip {
		errs = append(errs, fmt.Errorf("overflow %q must be %q or %q", o.Overflow, OverflowScroll, OverflowClip))
	}
	if o.MaxRows < 0 {
		errs = append(errs, errors.New("max_rows must not be negative"))
	}
	return errors.Join(errs...)
}

// WithDefaults fills empty fields with the renderer's defaults.
func (o Options) WithDefaults() Options {
	o.Layout = cmp.Or(o.Layout, LayoutDeparture)
	o.Overflow = cmp.Or(o.Overflow, OverflowScroll)
	o.Font = cmp.Or(o.Font, DefaultFont)
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
	opts = opts.WithDefaults()
	if len(rows) == 0 {
		return &awtrix.Layout{Version: 1, Regions: []awtrix.Region{{
			ID:       "empty",
			Box:      awtrix.Box{0, 0, awtrix.TC002Width, awtrix.TC002Height},
			Text:     awtrix.PlainText(opts.EmptyText),
			Font:     opts.nameFont(opts.EmptyText),
			Color:    DimColor,
			Scroll:   opts.scroll(),
			TextCase: awtrix.TextCaseAsTyped,
		}}}
	}
	shown := opts.prepare(Visible(rows, opts))
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
// departs next. Rows without a headsign come last, so a trip whose
// destination is unknown does not take that place.
func Visible(rows []transit.Row, opts Options) []transit.Row {
	idx := visible(rows, opts.WithDefaults())
	out := make([]transit.Row, len(idx))
	for i, j := range idx {
		out[i] = rows[j]
	}
	return out
}

// visible returns the indexes in rows of the rows Visible returns.
func visible(rows []transit.Row, opts Options) []int {
	n := len(rows)
	if opts.Layout == LayoutRows {
		n = min(n, opts.MaxRows)
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		ra, rb := rows[a], rows[b]
		if (ra.Headsign == "") != (rb.Headsign == "") {
			if ra.Headsign == "" {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(ra.Line.ShortName, rb.Line.ShortName), cmp.Compare(ra.Headsign, rb.Headsign))
	})
	if opts.Layout != LayoutRows {
		idx = idx[:min(len(idx), 1)]
	}
	return idx
}

// prepare drops canceled trips from rows when no strike can be drawn:
// without it a canceled trip reads as one to catch.
func (o Options) prepare(rows []transit.Row) []transit.Row {
	if o.marks() {
		return rows
	}
	out := slices.Clone(rows)
	for i, r := range out {
		out[i].Departures = slices.DeleteFunc(slices.Clone(r.Departures), func(d transit.Departure) bool {
			return d.Status == transit.StatusCanceled
		})
	}
	return out
}

// RowView is how a layout treats one row.
type RowView struct {
	Row transit.Row
	// Name is the headsign as displayed, after Labels.
	Name  string
	Shown bool
	// Overflows reports that Name is wider than the box the layout gives
	// it, so it scrolls or is clipped.
	Overflows bool
}

// Views reports, for each of rows in order, whether the layout shows it and
// whether its name fits.
func Views(rows []transit.Row, now time.Time, opts Options) []RowView {
	opts = opts.WithDefaults()
	out := make([]RowView, len(rows))
	for i, r := range rows {
		out[i] = RowView{Row: r, Name: opts.Label(r.Headsign)}
	}
	idx := visible(rows, opts)
	shown := make([]transit.Row, len(idx))
	for i, j := range idx {
		shown[i] = rows[j]
	}
	shown = opts.prepare(shown)
	for i, j := range idx {
		v := &out[j]
		v.Shown = true
		box := awtrix.TC002Width
		if opts.Layout == LayoutRows {
			_, bw := badgeRegions("", 0, shown[i].Line, opts)
			_, box = opts.fitRow(shown[i], now, bw+gap)
		}
		v.Overflows = FontWidth(opts.nameFont(v.Name), v.Name) > box
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
		times, nameW := opts.fitRow(r, now, sx)
		l.Regions = append(l.Regions, badge...)
		l.Regions = append(l.Regions, opts.nameRegion(id("sign"), awtrix.Box{sx, y, nameW, rowHeight}, r.Headsign))
		l.Regions = append(l.Regions, times.regions(id("min"), id("marks"), awtrix.TC002Width-times.width, y, opts)...)
		y += rowHeight
	}
	return l
}

// fitRow splits a rows-layout row whose name starts at x between its times
// and its name, returning the times and the name's width.
//
// Times take what they need, up to half the display, but a second time is
// shown only if the name still fits beside it: a name that scrolls is harder
// to read than a later departure is to miss. Canceled trips before the first
// one a rider can catch don't count, so they never push it out.
func (o Options) fitRow(r transit.Row, now time.Time, x int) (timeRow, int) {
	label := o.Label(r.Headsign)
	name := FontWidth(o.nameFont(label), label)
	deps := r.Departures
	keep := 1 + slices.IndexFunc(deps, func(d transit.Departure) bool {
		return d.Status != transit.StatusCanceled
	})
	for {
		times := fitTimes(deps, now, o, awtrix.TC002Width/2, false)
		w := awtrix.TC002Width - max(times.width, charWidth) - gap - x
		if len(times.items) <= max(keep, 1) || name <= w {
			return times, w
		}
		deps = deps[:len(times.items)-1]
	}
}

func (o Options) nameRegion(id string, box awtrix.Box, headsign string) awtrix.Region {
	name := o.Label(headsign)
	return awtrix.Region{
		ID:       id,
		Box:      box,
		Text:     awtrix.PlainText(name),
		Font:     o.nameFont(name),
		Color:    TextColor,
		Align:    awtrix.AlignStart,
		Scroll:   o.scroll(),
		TextCase: awtrix.TextCaseAsTyped,
	}
}

func (o Options) nameFont(name string) string {
	if o.NameFont != "" {
		return o.NameFont
	}
	if o.Font == DefaultFont && strings.ContainsFunc(name, accentAbove) {
		return AccentFont
	}
	return o.Font
}

// accentAbove reports whether c is a Latin-1 letter with a mark above it.
// Marks on other letters get a row of their own in the small font.
func accentAbove(c rune) bool {
	return c >= 'À' && c <= 'ÿ' && !strings.ContainsRune("ÆÇÐ×ØÞßæçð÷øþ", c)
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
		color = TextColor
	}
	if line.Mode != transit.ModeMetro && line.ShortName != "" && TextWidth(line.ShortName) <= maxBadgeW {
		w := TextWidth(line.ShortName)
		name := awtrix.Region{
			ID:       id,
			Box:      awtrix.Box{0, y, w, rowHeight},
			Text:     awtrix.PlainText(line.ShortName),
			Font:     opts.Font,
			Color:    TextColor,
			Align:    awtrix.AlignStart,
			Scroll:   &awtrix.Scroll{Mode: awtrix.ScrollStatic},
			TextCase: awtrix.TextCaseAsTyped,
		}
		if line.Color == "" {
			return []awtrix.Region{name}, w
		}
		// Regions are drawn in list order, later ones on top, so the name
		// follows its fill.
		name.ID, name.Box[0], name.Color = id+"-name", 1, badgeTextColor(line.Color)
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

// minMarkLuminance is the relative luminance below which a line color is
// treated as unlit.
const minMarkLuminance = 0.01

// badgeTextColor picks whichever of unlit and TextColor contrasts more with
// fill. It ignores agencies' route_text_color: some pair white with a
// bright yellow, which on LEDs is two bright colors side by side.
func badgeTextColor(fill string) string {
	if contrast(fill, unlit) >= contrast(fill, TextColor) {
		return unlit
	}
	return TextColor
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
		color := DimColor
		if live {
			color = TextColor
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
			cmds = append(cmds, awtrix.RectFill(from, 0, dot-1-from, 1, RuleColor))
		}
		from = dot + 2
	}
	if from < awtrix.TC002Width {
		cmds = append(cmds, awtrix.RectFill(from, 0, awtrix.TC002Width-from, 1, RuleColor))
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
			marks = append(marks, awtrix.Pixel(mx+w, topRow, AccentColor))
		}
		if it.canceled {
			marks = append(marks, awtrix.RectFill(mx-1, midRow, w+2, 1, TextColor))
		}
		if i < len(t.items)-1 {
			marks = append(marks, awtrix.Pixel(mx+w+timeGap/2, midRow, RuleColor))
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

// Label returns the display name for headsign: its entry in Labels, or the
// headsign itself.
func (o Options) Label(headsign string) string {
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
// visual reference: every character advances 4 px, a space 2 px.
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

// FontWidth returns the width of s in pixels as drawn in font, from its
// first to its last lit column. Only AccentFont is measured per glyph; other
// fonts are measured like the small one.
func FontWidth(font, s string) int {
	if font != AccentFont {
		return TextWidth(s)
	}
	w, trim := 0, 0
	for _, r := range strings.TrimSpace(s) {
		w += cmp.Or(accentFontAdvance[r], charWidth)
		trim = 1 - accentFontOverhang[r]
	}
	return w - trim
}

// accentFontAdvance has AccentFont's advances that are not charWidth, for
// the letters and punctuation of European languages. They are the firmware's:
// MatrixLight6.bdf of trip5/Matrix-Fonts with the glyphs AWTRIX NG adds.
var accentFontAdvance = func() map[rune]int {
	m := map[rune]int{}
	for px, glyphs := range map[int]string{
		2: " \u00a0!',.:;Iil|¡¦·¹ÎİıļΙιΙіі•",
		3: "()1`©²´¸Ììíįĺŀſɭί–",
		5: "&@GHNOQ~«»ÐÑØßďĐđĜĞĠĢħĲĳĴĵŁŃŅŇŉťƠơƯưɑɒɖɗɚɝɞɠɢɬɳɴɻʆʋʑʒʗʚʛʜʝʡʢΗΘΝΟΠάαπσНОПЦЪбийцъҐ—",
		6: "#MWmw¼½ÆæĦŒœŴŵɕɫɮɯɰɱɲɶɷɸʉʊʍʓʘʠʬʭʮΆΈΎΔΛΜΦΨΩφψωώДЖИЙМФШЫЮджмфшыю",
		7: "ʣʦʧʩʪʫʯΉΌЩщ",
		8: "¾ʤʥΏ",
		9: "ʨ",
	} {
		for _, r := range glyphs {
			m[r] = px
		}
	}
	return m
}()

// accentFontOverhang is how far a glyph's last lit column is from the usual
// one, which leaves one unlit column before the next glyph.
var accentFontOverhang = map[rune]int{'Í': -1, 'Į': -1, 'Ą': 1, 'ą': 1, 'Ę': 1}
