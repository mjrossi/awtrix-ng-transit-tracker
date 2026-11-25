# Rendering on the TC002

Design rules for `internal/render`. See [ARCHITECTURE.md](./ARCHITECTURE.md)
for where the renderer sits.

Pushed apps without a layout are drawn at double size on a 26 × 8 grid.
The renderer uses AWTRIX NG's TC002-only `layout` key to address all
52 × 16 pixels. Board settings named here go in its `[boards.clock]`
table. A board's `layout` is one of:

- **`departure`** (default): one line and direction, like a platform sign.
  The destination gets the full width (about 13 characters in the small
  font, which advances 4 px per character and 2 px per space), so most
  names fit without scrolling. Other directions are other boards, and the
  clock's app rotation cycles through them.

  ```
  Sta Apolónia
  ────────────
  ▌ 3 · 8 · 14
  ```

- **`rows`**: two rows, one per line and direction, for stops where seeing
  several routes at once matters more. The soonest rows are shown, ordered
  by line and headsign so they do not swap places between pushes. Rows
  without a headsign come last.

  ```
  ▌Sta Apolónia      4
  ▌Telheiras     1 · 6
  ```

- **`grid`**: up to four lines in two columns, for busy stops. Each cell is
  one line's badge and its next departure, with no destination, so all of a
  line's destinations share a cell. Use it where every vehicle goes the same
  way, or pin the direction with the stop's `headsigns`. The soonest lines
  are shown, ordered by line. A canceled trip gives way to the next one a
  rider can catch. Badges have a fixed share of the cell, so a four-character
  name is drawn in the line color without a fill.

  ```
  732   5   736   7
  744  12   783   8
  ```

Names that still do not fit:

- `[boards.clock.labels]` maps a headsign to a shorter display name. No
  automatic abbreviation: rules like "Santa" → "Sta" are language-specific.
- Otherwise they scroll in their box, or with `overflow = "clip"` are cut.

Names with a mark above a Latin-1 letter ("São Sebastião") use the
`matrix-light6` font: `small` fits those accents by shortening the letter,
with no blank row between them, so they read as blobs. Other names, the
times and the badges stay in `small`. `name_font` overrides the choice for
destination names and `empty_text`, and a board with its own `font` keeps
it. `matrix-light6` is proportional, so its names are measured glyph by
glyph (`FontWidth`), for European letters and punctuation; other characters
count as 4 px.

Other rules:

- Colors are the Night theme of the maintainer's Lexicon palette, since
  unlit LEDs are the background: warm cream for names and live minutes, a
  warm grey for schedule-only minutes, and amber for one job only, the
  live marker. Agency line colors appear only in the badge.
- Badges: a color bar for metro lines. Other lines get their short name
  cut out of a block of the line color (unlit or cream, whichever
  contrasts more; agencies' `route_text_color` is ignored because some
  pair white with bright yellow). A line without a color shows its plain
  name. A name too wide for a badge gets a 5 × 5 glyph of the line's mode
  (bus, tram, rail, ferry), or the bar for other modes.
- Only as many times as fit are shown; times never scroll or get cut.
- Live minutes get a one-pixel amber dot at their top right, so live and
  schedule-only differ by a mark as well as brightness. A dim dot sits
  between times. A canceled trip kept with a stop's `keep_canceled` is
  struck through, and never pushes the next trip a rider can catch out of
  its row. The marks are placed for the `small` font; with another font
  they are left out, and so are canceled trips.
- The departure layout draws a hairline under the destination, broken
  above each live dot so the two do not merge, and shows a departure
  under a minute away as `Due`. The rows layout keeps `0`,
  because `Due` would take two or three characters from the destination.
- In the rows layout a second time is shown only if the destination still
  fits beside it, so names scroll less.
- Text is sent with `textCase: asTyped`, so the clock's uppercase setting
  does not turn "Cais do Sodré" into capitals.
- Minutes are computed from absolute times at render time, so the last
  good data stays accurate while a provider is briefly down. While a
  source's last refresh has failed, its departures are shown as
  schedule-only: same times, no live marker.

A 32 × 8 renderer for other AWTRIX NG clocks can follow.
