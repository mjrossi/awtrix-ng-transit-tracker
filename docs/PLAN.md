# Plan

A small Go service that polls public transit departure data and pushes it to
an AWTRIX NG pixel clock (Ulanzi TC002, 52 × 16). It runs in Docker, for
example on a NAS, and is meant to be usable by anyone, in any city.

## Goals

- Show the next departures for one or more stops on the clock, with the
  line's color, direction and minutes to departure.
- Work for any mode: metro first, then bus, tram, rail and ferry, without
  changing the core.
- Support many agencies without maintaining code for each one.
- Fail safe: stale data disappears from the clock rather than lying.

## Non-goals (v1)

- Trip planning or routing.
- Running on the clock itself. An on-device Berry script for simple APIs
  may come later; GTFS-RT decoding and large static GTFS zips do not fit on
  the clock.
- A web UI. Configuration is one TOML file.

## Data sources: agencies are data, adapters are code

There is no single free API for every agency. Almost every agency publishes
the same open format, though, so maintenance cost does not need to grow
with the number of agencies.

| Provider | Agencies | Code per agency | Notes |
|---|---|---|---|
| `gtfsrt` (generic) | Thousands: anything with static GTFS + GTFS-RT `TripUpdates` | None: a TOML entry | The default path. Feeds are listed in the [Mobility Database](https://mobilitydatabase.org/). |
| `transitland` | Same feeds, merged into JSON by [Transitland](https://www.transit.land/) | None | Skipped: the free plan is 10,000 queries a month, too few for an always-on clock, and the generic provider covers the same feeds. |
| Custom adapters | One each | A few hundred lines | Only for agencies without usable GTFS-RT `TripUpdates`. |

**Rule for custom adapters** (goes into `CONTRIBUTING.md`): an agency gets
its own adapter only if the generic GTFS-RT provider cannot serve it, or
its own API is clearly better (for example, realtime that the GTFS-RT feed
lacks). Expect roughly 10–20 over the life of the project.

### Lisbon (first city)

| Operator | Source | Status |
|---|---|---|
| Carris Metropolitana (regional bus) | Own JSON API, `https://api.carrismetropolitana.pt/v2/arrivals/by_stop/{id}`, no key. Returns the whole service day; `Cache-Control: max-age=20`; unknown stops get 404. Live line IDs carry a `[XXXXX]` feed prefix that `/lines` does not. Data CC BY 4.0 | **Done** (`carrismetropolitana` provider) |
| Metro de Lisboa | EstadoServicoML API behind a WSO2 gateway; free registration and a client-credentials OAuth token. Wait times have no line field (read per line), errors come as HTTP 200 with `codigo`, some platforms are frozen since 2024, and terminals list trains twice. Data CC BY 4.0 | **Done** (`metrolisboa` provider) |
| Carris (city bus and tram) | Reported GTFS-RT | Not yet confirmed |
| CP, Fertagus, ferries | — | Not researched |

## Architecture

```
providers ──▶ pkg/transit ──▶ internal/render ──▶ pkg/awtrix ──▶ clock
(adapters)    Departure model   TC002 layout       HTTP (MQTT later)
              filter + group
              ▲
              internal/scheduler: poll, active hours, backoff, last-good cache
```

### The abstraction

Every provider implements one small interface and returns one model.
Nothing past `pkg/transit` knows which provider produced a departure.

```go
type Provider interface {
    Departures(ctx context.Context, q Query) ([]Departure, error)
}
```

- `Query` carries the stop ID, the current time, a look-ahead window and a
  limit. Filtering by line, direction and walk time happens in the core,
  not in providers, so every provider gets it for free.
- `Departure` carries the line (ID, short name, color, mode), headsign,
  stop ID, scheduled time, an optional predicted time, and a status
  (scheduled, canceled, added).
- Stop search (`StopSearcher.SearchStops`) is a separate, optional interface so
  providers that cannot search still work.

**Enforcement.** `pkg/transit/transittest` is a contract suite every
provider must pass, modelled on urbanist-atlas's `storetest`: results
sorted soonest first, nothing in the past, nothing outside the window, the
limit respected, the right stop ID, a non-empty line ID, and context
cancellation honored. Each adapter tests against recorded responses in
its `testdata/`; CI never calls a live API.

**Catalog.** Supported systems are TOML entries in
`pkg/transit/catalog/systems/`, embedded in the binary. Each names its
provider and settings, a sample stop, a maintenance status (`maintained`
or `community`), the date it last passed a live check, and any settings
users must add, such as API keys. A provider block uses one with
`system = "<id>"`; its other keys override the entry's. CI checks every
entry offline; `just catalog-check` fetches each sample stop live. Line
colors come from the providers (GTFS `route_color`), not the catalog.

### Rendering on the TC002

Pushed apps without a layout are drawn at double size on a 26 × 8 grid.
The renderer uses AWTRIX NG's TC002-only `layout` key to address all
52 × 16 pixels. A board's `layout` is one of:

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
  by line and headsign so they do not swap places between pushes.

  ```
  ▌Sta Apolónia      4
  ▌Telheiras     1 · 6
  ```

Names that still do not fit:

- `[boards.labels]` maps a headsign to a shorter display name. No
  automatic abbreviation: rules like "Santa" → "Sta" are language-specific.
- Otherwise they scroll in their box, or with `overflow = "clip"` are cut.

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
  between times. A canceled trip kept with `keep_canceled` is struck
  through.
- The departure layout draws a hairline under the destination, and shows
  a departure under a minute away as `Due`. The rows layout keeps `0`,
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

### Robustness

- Every push sets `lifetimeMs`, so if the service dies the app leaves the
  clock instead of showing "2 min" forever.
- Pushed apps live in RAM and are lost when the clock reboots. Pushing on
  every poll restores the app within one interval. MQTT will add re-push
  on the retained `availability` topic.
- Provider errors back off exponentially; the last good data is reused
  until it is older than a configurable limit.
- Polling only within configured active hours keeps API use low.

## Tech stack

Mirrors urbanist-atlas's Go conventions:

- Go, standard library first. Approved modules: `urfave/cli/v3` (CLI),
  `pelletier/go-toml/v2` (config), `google/go-cmp` (tests). GTFS-RT is
  decoded with the standard library, so no protobuf module. Later: an
  MQTT client (to be chosen).
- `log/slog`, JSON in production and text in development.
- Library-first: `pkg/` is importable, `cmd/transit-tracker` is a thin CLI.
- mise for tooling, just for commands, golangci-lint v2 as the single lint
  and format gate.
- Multi-stage Dockerfile, static `CGO_ENABLED=0` binary, non-root Alpine.

### Layout

```
cmd/transit-tracker/      CLI: run, preview, config validate, stops search, systems
pkg/transit/              Departure model, Provider interface, filtering, grouping
pkg/transit/transittest/  provider contract suite
pkg/transit/providers/    demo, carrismetropolitana, metrolisboa, gtfsrt
pkg/transit/catalog/      ready-made systems
pkg/awtrix/               typed AWTRIX NG client and payload types
internal/config/          TOML loading and validation
internal/render/          TC002 layout renderer
internal/scheduler/       polling loop
docs/                     this plan and design notes
```

## Build order

1. **Core.** Model, `Provider` interface, contract suite, AWTRIX NG HTTP
   client, TC002 renderer, scheduler, config, CLI, Dockerfile. A `demo`
   provider generates departures from configured headways so the whole
   pipeline runs on a real clock without any transit API. Done.
2. **Carris Metropolitana adapter.** Verified and keyless: the first real
   data on the clock. Done, with `stops search` for finding stop IDs.
3. **Metro de Lisboa adapter.** Done.
4. **Generic GTFS-RT adapter.** Done, tested against the NYC subway and
   BART. Decodes GTFS-RT with the standard library (no protobuf module).
   The schedule zip is staged in a temporary file and repeated strings
   are stored once, so HSL (a 91 MB zip, 444,000 trips) peaks at about
   95 MB instead of 213 MB on the first load. The daily refresh peaks
   higher, because the previous schedule stays in use until the new one
   has loaded. Boards sharing a provider share one download; a failed
   download is retried after 15 minutes, and one failing realtime feed
   leaves the others showing.
   Limits: only departures with realtime predictions, and stop updates
   with a delay but no absolute time are skipped; both need
   `stop_times.txt` and calendar handling, a later step.
5. **Catalog.** Done: six systems, `systems` command, offline and live
   checks. The Transitland adapter is skipped: its free plan (10,000
   queries a month) cannot support an always-on board, and the generic
   provider plus the catalog cover what it was for.
6. **Test on a real TC002.** Everything so far has been checked against a
   fake clock. Push the demo and Lisbon boards to a real clock and check:
   - the firmware accepts every payload (no `422`), for both layouts and
     the empty state;
   - text widths match `render.TextWidth`: about 13 characters across,
     "Cais do Sodré" fits and "Santa Apolónia" scrolls;
   - badge bar, filled bus-number badge (the unlit number over its fill
     relies on later regions drawing on top), mode glyphs, minute spacing
     and dimmed schedule-only times look right; `overflow = "clip"` cuts
     cleanly;
   - the small font's capitals sit on rows 1 to 5 of an 8 px box, as the
     drawn marks assume: the live dot on row 0, the separator and strike
     on row 3, the hairline on row 7. Check that the hairline and the live
     dot just below it do not crowd each other, and that the dim colors
     (separator dots, hairline) are visible at night brightness;
   - lifetime: stopping the service removes the app after three polls;
   - recovery: after a clock reboot the app returns within one poll;
   - active hours remove the app and stop requests; the app is removed
     with `DELETE /api/v1/apps/{name}` although it was pushed to
     `/api/v1/apps/pushed/{name}`, and deleting an app that is already
     gone answers `404`, which the client treats as success;
   - a malformed color is rejected (colors are normalized before
     pushing, so this only confirms the safeguard is needed);
   - in Docker on the NAS, the clock is reachable (`.local` names often
     do not resolve inside containers; use the IP address if not).

   Fix what differs in the renderer and golden files, and record the
   results here.
7. **Publish images.** Done: `.github/workflows/publish.yml`, plus a
   `main` tag published from every push to `main`. A GitHub Actions
   workflow on version tags (`v1.2.3`) that:
   - builds multi-arch images (`linux/amd64`, `linux/arm64`) with
     buildx and pushes them to `ghcr.io/mjrossi/awtrix-ng-transit-tracker`
     tagged `1.2.3`, `1.2`, `1` and `latest`;
   - sets OCI labels (source, license, version) and injects the version
     so `transit-tracker --version` reports it;
   - runs `just ci` first.

   Then switch the README's Docker and Compose examples to the published
   image, and add a short release checklist (tag, notes, verify the
   image runs on arm64).

## Later

Not needed for the maintainer's setup (TC002, Docker on a NAS, Lisbon
providers). Each widens who the project works for.

- **Schedule-only departures and delay-only updates in `gtfsrt`.**
  Realtime feeds often list only trips that have started or are about
  to, so a board shows "No departures" while a timetabled bus is 20
  minutes away, and a realtime outage blanks the board although the
  timetable is known. Some agencies, many in Europe, send delays without
  clock times, and those updates are skipped today. Needs `stop_times.txt`
  and calendar handling (`calendar.txt`, `calendar_dates.txt`, times past
  24:00, `frequencies.txt`). The file is large (37 MB uncompressed for the
  NYC subway, 1.1 GB for HSL), so index only the stops in the
  configuration while streaming it.
- **MQTT output.** Pushed apps live in the clock's RAM, so after a reboot
  a board is missing until the next poll (up to a minute). AWTRIX NG's
  retained `availability` topic would allow an immediate re-push. MQTT
  also avoids the clock's address having to resolve inside Docker. Needs
  an MQTT client module approved.
- **First departure in the empty state.** Instead of "No departures",
  show when service starts ("First 5:42"). Needs a departure beyond the
  board's window, so the scheduler would have to look further ahead, and
  for `gtfsrt` the schedule-only support above.
- **Section cards.** With boards from several agencies, "which board is
  this?" becomes a question the clock should answer. A short card (2–3 s)
  ahead of each agency's boards in the rotation would: the agency's icon
  at up to 16 × 16, where a mark can be read, beside a short title such
  as "Metro". A board's optional `section` names its card; a section has
  a title and an optional `icon`, an AWTRIX icon ID or file the user
  supplies, since the repository ships no agency logos. The card is
  pushed while any of its boards is active, with the same `lifetimeMs`,
  and removed with them. Open problem: rotation order. The firmware
  shows pushed apps in the order they first arrived and remembers it,
  and runners push concurrently, so a card can land anywhere. Either
  document arranging the rotation once in the clock's web UI, or set the
  order through the firmware's rotation API at startup, moving only this
  service's apps relative to each other. Off by default: with one agency
  or two boards, a card is a few seconds per round with no departures.
- **Per-board time zone.** `active_hours` reads the one top-level
  `timezone`, so a clock showing boards from several cities needs their
  hours converted by hand, and they drift by an hour for the weeks a year
  when only one city has changed clocks for daylight saving time. An
  optional `timezone` on `[[boards]]`, falling back to the top-level one,
  fixes both. It belongs on the board rather than the provider because
  `active_hours` is a board setting and one board can combine stops from
  several providers.
- **32 × 8 renderer.** Both layouts use the TC002-only `layout` key, so
  32 × 8 AWTRIX NG clocks such as the Ulanzi TC001, probably the more
  common device, cannot show boards. Needs its own design: a scrolling
  destination with the next time, or the badge and times only.
- **Home Assistant add-on.** Many AWTRIX owners run Home Assistant OS,
  where add-ons are the only way to run a container. Packaging only: an
  add-on repository, options mapped to `config.toml`, and the published
  multi-arch images.
- **On-device mode.** A Berry script on the clock that polls a simple JSON
  provider (Carris Metropolitana) with no server. Only for providers
  that need no protobuf decoding or large schedule files.

## Open questions

- Whether any Lisbon operator publishes GTFS-RT `TripUpdates`.
- Which MQTT client to approve when MQTT lands.
- The Ulanzi TC001's behavior with the `layout` key: confirm it is
  rejected before designing the 32 × 8 renderer.
