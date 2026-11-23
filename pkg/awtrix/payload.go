// Package awtrix is a typed client for the AWTRIX NG HTTP API.
//
// It covers what this project needs: pushing and deleting apps, including
// the TC002-only layout key. Field names follow the AWTRIX NG payload
// reference (docs/reference/payload.md and docs/guides/layouts.md in the
// firmware repository). AWTRIX NG rejects unknown keys, so every field here
// is omitted when empty rather than sent as a zero value.
package awtrix

import "encoding/json"

// Display sizes, in pixels.
const (
	TC002Width  = 52
	TC002Height = 16
)

// App is the body of a pushed app.
type App struct {
	// DurationMs is how long the app is shown per rotation; 0 uses the
	// clock's global setting.
	DurationMs int `json:"durationMs,omitempty"`
	// LifetimeMs removes (or marks) the app this long after the clock
	// received it; 0 means forever.
	LifetimeMs     int    `json:"lifetimeMs,omitempty"`
	LifetimeExpiry string `json:"lifetimeExpiry,omitempty"`
	Repeat         int    `json:"repeat,omitempty"`

	// Layout splits a TC002 display into regions. It cannot be combined
	// with the plain visual keys below.
	Layout *Layout `json:"layout,omitempty"`

	Text      *Text  `json:"text,omitempty"`
	TextColor string `json:"textColor,omitempty"`
	Icon      string `json:"icon,omitempty"`
	Font      string `json:"font,omitempty"`
}

// Lifetime expiry behaviors.
const (
	ExpiryRemove = "remove"
	ExpiryMark   = "mark"
)

// Layout is the TC002 layout block.
type Layout struct {
	Version         int      `json:"version"`
	BackgroundColor string   `json:"backgroundColor,omitempty"`
	Regions         []Region `json:"regions"`
}

// Box is a region's [x, y, width, height] on the full-resolution display.
type Box [4]int

// Region is one box of a layout. Exactly one of Text, Icon or Draw must be
// set.
type Region struct {
	ID       string    `json:"id"`
	Box      Box       `json:"box"`
	Text     *Text     `json:"text,omitempty"`
	Icon     string    `json:"icon,omitempty"`
	Draw     []Command `json:"draw,omitempty"`
	Font     string    `json:"font,omitempty"`
	Color    string    `json:"color,omitempty"`
	Align    string    `json:"align,omitempty"`
	VAlign   string    `json:"valign,omitempty"`
	Scroll   *Scroll   `json:"scroll,omitempty"`
	TextCase string    `json:"textCase,omitempty"`
}

// Alignments for Region.Align and Region.VAlign.
const (
	AlignStart  = "start"
	AlignCenter = "center"
	AlignEnd    = "end"
)

// Text case modes.
const (
	TextCaseInherit = "inherit"
	TextCaseUpper   = "upper"
	TextCaseAsTyped = "asTyped"
)

// Scroll controls how text that does not fit moves.
type Scroll struct {
	Mode     string `json:"mode,omitempty"`
	WhenFits string `json:"whenFits,omitempty"`
	Speed    int    `json:"speed,omitempty"`
	Gap      int    `json:"gap,omitempty"`
	HoldMs   int    `json:"holdMs,omitempty"`
}

// Scroll modes.
const (
	ScrollStatic = "static"
	ScrollWrap   = "wrap"
	ScrollLoop   = "loop"
	ScrollBounce = "bounce"
)

// Fragment is a run of text in one color.
type Fragment struct {
	Text  string `json:"text"`
	Color string `json:"color,omitempty"`
}

// Text is either a plain string or a list of colored fragments.
type Text struct {
	Plain     string
	Fragments []Fragment
}

// PlainText returns a Text holding s.
func PlainText(s string) *Text { return &Text{Plain: s} }

// Fragments returns a Text made of colored runs.
func Fragments(f ...Fragment) *Text { return &Text{Fragments: f} }

// MarshalJSON encodes fragments as an array and plain text as a string.
func (t Text) MarshalJSON() ([]byte, error) {
	if t.Fragments != nil {
		return json.Marshal(t.Fragments)
	}
	return json.Marshal(t.Plain)
}

// UnmarshalJSON accepts either form.
func (t *Text) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '[' {
		t.Plain = ""
		return json.Unmarshal(b, &t.Fragments)
	}
	t.Fragments = nil
	return json.Unmarshal(b, &t.Plain)
}

// Command is one draw command: a name followed by its arguments, such as
// ["rectFill", 0, 0, 2, 8, "#0072CE"].
type Command []any

// RectFill draws a filled rectangle.
func RectFill(x, y, w, h int, color string) Command {
	return Command{"rectFill", x, y, w, h, color}
}

// Pixel draws one pixel.
func Pixel(x, y int, color string) Command {
	return Command{"pixel", x, y, color}
}

// Pixels draws many pixels in one color; xy alternates x and y.
func Pixels(color string, xy ...int) Command {
	c := Command{"pixels", color}
	for _, v := range xy {
		c = append(c, v)
	}
	return c
}
