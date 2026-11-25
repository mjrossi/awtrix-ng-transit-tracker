# Plan

Urbanist Timetable is a small Go tool that shows public transit
departures in a terminal and on an AWTRIX NG pixel clock (Ulanzi TC002,
52 × 16). It runs in Docker, for example on a NAS, and is meant to be usable by anyone, in any
city. It belongs to the Urbanist family, alongside *The Urbanist Lexicon*
and *Urbanist Atlas*.

## Goals

- Show the next departures for one or more stops on the clock, with the
  line's color, direction and minutes to departure.
- Work for any mode: metro first, then bus, tram, rail and ferry, without
  changing the core.
- Support many agencies without maintaining code for each one.
- Fail safe: stale data disappears from the clock rather than lying.
- Keep departures independent of the display.

## Non-goals (v1)

- Trip planning or routing.
- Running on the clock itself: GTFS-RT decoding and large static GTFS zips
  do not fit there.
- A server. The process listens on no ports.
- A web UI. Configuration is one TOML file.

## Direction

One release follows, described in the
[design](./superpowers/specs/2026-11-25-clock-cli-library-design.md):
**v0.7.0, a library.** `pkg/` gets a core free of configuration code,
examples, and a lower Go requirement, so other Go programs can use the
providers.

## Data sources: agencies are data, adapters are code

There is no single free API for every agency. Almost every agency publishes
the same open format, though, so maintenance cost does not need to grow
with the number of agencies.

| Provider | Agencies | Code per agency | Notes |
|---|---|---|---|
| `gtfsrt` (generic) | Thousands: anything with static GTFS + GTFS-RT `TripUpdates` | None: a TOML entry | The default path. Feeds are listed in the [Mobility Database](https://mobilitydatabase.org/). |
| `transitland` | Thousands, merged into JSON by [Transitland](https://www.transit.land/) | None: a TOML entry with a feed Onestop ID | Done. The fallback where an agency's own feeds are awkward, and the way to show timetabled trips today. Every request counts against a monthly quota (10,000 free, 50,000 Hobbyist/Academic), so each stop is fetched once per `refresh_interval` and boards count down from that. The quota belongs to the API key, so blocks sharing a key are added up. |
| Custom adapters | One each | A few hundred lines | Only for agencies without usable GTFS-RT `TripUpdates`. |

**Rule for custom adapters** (also in `CONTRIBUTING.md`): an agency gets
its own adapter only if the generic GTFS-RT provider cannot serve it, or
its own API is clearly better (for example, realtime that the GTFS-RT feed
lacks). New adapters are frozen unless the maintainer needs one.

### Lisbon (first city)

| Operator | Source | Status |
|---|---|---|
| Carris Metropolitana (regional bus) | Own JSON API, `https://api.carrismetropolitana.pt/v2/arrivals/by_stop/{id}`, no key. Returns the whole service day; `Cache-Control: max-age=20`; unknown stops get 404. Live line IDs carry a `[XXXXX]` feed prefix that `/lines` does not. Data CC BY 4.0 | **Done** (`carrismetropolitana` provider) |
| Metro de Lisboa | EstadoServicoML API behind a WSO2 gateway; free registration and a client-credentials OAuth token. Wait times have no line field (read per line), errors come as HTTP 200 with `codigo`, some platforms are frozen since 2024, and terminals list trains twice. Data CC BY 4.0 | **Done** (`metrolisboa` provider) |
| Carris (city bus and tram) | GTFS and GTFS-RT vehicle positions on `https://gateway.carris.pt/gateway/gtfs/api/v2.11/GTFS`; its trip updates and alerts paths answer 404. The GTFS has no headsigns. Transitland has the schedule (`f-carris~pt`) but no realtime | **Schedule only**, through `transitland` |
| CP (rail) | Transitland has the schedule (`f-eyc-cp`); a third-party `TripUpdates` feed exists (see Open questions) | **Schedule only**, through `transitland` |
| Fertagus, ferries | — | Not researched |

## Where the rest lives

- How the code is built: [ARCHITECTURE.md](./ARCHITECTURE.md).
- TC002 layouts and design rules: [rendering.md](./rendering.md).
- Security model: [SECURITY.md](../SECURITY.md).
- Conventions and approved modules: [CLAUDE.md](../CLAUDE.md).
- Adding a city or provider, and releasing:
  [CONTRIBUTING.md](../CONTRIBUTING.md).

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
  configuration while streaming it. Until then, `transitland` shows
  timetabled trips for the agencies it covers.
- **Pages for busy stops.** The `grid` layout shows four lines; a stop with
  more needs one board per group of `lines`. AWTRIX NG accepts an array of
  apps and numbers them, but an array that shrinks, withdrawing a board, the
  state file and the rotation order all need a design.
- **Destinations from the last stop.** Carris publishes no headsigns, so its
  departures have none. `transitland` could look up a trip's last stop once
  per route pattern. Each is a request against the quota, and may
  have to be repeated for each new feed version, which Carris has daily.
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

## Contributions welcome

- **32 × 8 renderer.** All layouts use the TC002-only `layout` key, so
  32 × 8 AWTRIX NG clocks such as the Ulanzi TC001, probably the more
  common device, cannot show boards. Needs its own design: a scrolling
  destination with the next time, or the badge and times only.

## Open questions

- Whether any Lisbon operator publishes GTFS-RT `TripUpdates`.
- Whether CP's third-party `TripUpdates` feed
  (`rt.gtfs.baguette.pirnet.si`) works with `gtfsrt`, and on what terms.
- The Ulanzi TC001's behavior with the `layout` key: confirm it is
  rejected before designing the 32 × 8 renderer.
- Still to confirm on the maintainer's TC002:
  - a cleanup of removed boards leaves scripts in the rotation;
  - names in `matrix-light6` next to boards in `small`, and that a name
    with an accented capital ("Águas Livres") keeps the baseline of the
    others;
  - a malformed color is rejected (colors are normalized before pushing,
    so this only confirms the safeguard is needed).
