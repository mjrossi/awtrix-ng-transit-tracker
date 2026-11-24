# Architecture

How the service is built today. The roadmap is in [PLAN.md](./PLAN.md), the
TC002 design rules are in [rendering.md](./rendering.md), and the security
model is in [SECURITY.md](../SECURITY.md).

## Data flow

```
config.toml ──▶ internal/config ──▶ cmd (wire.go): one provider per block, one runner per board

each tick, per board (internal/scheduler.Runner):
  outside active_hours ──▶ Output.Withdraw (once)
  otherwise:
    for each stop:  Provider.Departures ──▶ last-good cache, backoff
    ──▶ Snapshot (cached departures + health per stop) ──▶ Output.Publish

clock output (internal/clockout):
  Snapshot.Rows (transit.Filter ──▶ transit.Group) ──▶ render.TC002 ──▶ awtrix PushApp ──▶ clock
```

The scheduler knows nothing about displays. It produces a `Snapshot` per
board and hands it to a `scheduler.Output`. A snapshot keeps each stop's
cached departures unfiltered, together with its health (`FetchedAt`,
`Failing`, `Partial`, `Usable`), so an output can decide how to show stale
data. `Snapshot.Rows(now, dimFailing)` filters and groups them; the clock
passes `dimFailing = true` so a failing source's predictions are shown
schedule-only.

The service makes outgoing requests only: to transit APIs and to the clock.
It listens on no ports.

## Packages and what they may import

| Package | Owns | Imports (project) |
|---|---|---|
| `pkg/transit` | `Departure`, `Line`, `Provider`, `Query`, `PartialError`, `Filter`, `Group`, `Registry`, `MatchesSearch` | none |
| `pkg/transit/transittest` | the provider contract suite | `transit` |
| `pkg/transit/providers/<name>` | one data source each | `transit` only |
| `pkg/transit/providers` | the built-in registry | every provider |
| `pkg/transit/catalog` | embedded system entries (`systems/*.toml`) | none |
| `pkg/awtrix` | typed AWTRIX NG HTTP client and payload types | none |
| `internal/render` | departure rows → TC002 `awtrix.Layout` | `transit`, `awtrix` |
| `internal/scheduler` | polling, backoff, last-good cache, active hours, `Snapshot`, the `Output` interface | `transit` |
| `internal/clockout` | the clock `Output`: snapshot → TC002 app → push or delete | `scheduler`, `render`, `awtrix` |
| `internal/config` | TOML decoding, `${NAME}` expansion, validation, provider resolution | `transit`, `catalog`, `render`, `awtrix` |
| `cmd/urbanist-timetable` | CLI commands and wiring (`wire.go`) | everything |

Rules, from [CLAUDE.md](../CLAUDE.md):

- Nothing past a provider imports a provider package. The scheduler,
  outputs, renderer and clock client see only `transit.Departure`.
- The scheduler imports no output: display policy, such as dimming stale
  predictions, belongs to the output.
- `pkg/` is importable by other projects; `internal/` is not.
- Filtering happens in the core, not in providers.

## Concurrency model

- `run` starts one `scheduler.Runner` per board (`scheduler.RunAll`). Each
  runner ticks on its own `time.Ticker` and owns its per-source state, so
  that state needs no lock. A snapshot shares the cached departure slices
  with the runner; a refresh replaces a slice rather than changing it, and
  `Snapshot.Rows` copies before it changes anything.
- All runners share one output. `clockout.Output` holds no mutable state.
- Within a tick, a board refreshes its due sources one after another.
- `buildProviders` (`cmd/urbanist-timetable/wire.go`) builds each
  `[providers.NAME]` block **once**. Boards that name the same block share
  the instance, and with it its caches. Providers must therefore be safe for
  concurrent use; `transittest.Run` calls them from several goroutines, and
  CI runs tests with `-race`.
- Shared caches inside providers let boards on the same feed share one
  download:

  | Provider | Cached | TTL | After a failure |
  |---|---|---|---|
  | `gtfsrt` | schedule zip | 24 h | keep last, retry in 15 min |
  | `gtfsrt` | each realtime feed | 15 s | failure cached for 15 s too |
  | `metrolisboa` | stations, destinations | 24 h | keep last, retry in 15 min |
  | `metrolisboa` | each line's wait times | 15 s | not cached |
  | `metrolisboa` | OAuth token | until 1 min before expiry | refetched on 401 once |
  | `carrismetropolitana` | lines | 24 h | keep last, retry in 15 min |
  | `carrismetropolitana` | stop list (search only) | process lifetime | not cached |

  `gtfsrt` is single-flight: one download per feed at a time, and other
  callers wait for it. Once a schedule exists, its daily refresh runs in a
  background goroutine detached from the poll's context, and callers keep
  using the previous schedule. The Lisbon providers do not deduplicate
  concurrent misses, so boards that start together may each fetch once.
- The process stops on `SIGINT` or `SIGTERM`: the root context is canceled,
  runners return, and a canceled fetch counts as neither a success nor a
  failure.

## Time

- Providers compute windows from `Query.Now`, never the wall clock, so tests
  are reproducible. A provider's own `clock` field is used only where no
  query exists: stop search and OAuth token expiry.
- A runner takes `now` in the board's location (`timezone`, else the
  top-level `timezone`, else `TZ`). The location only affects
  `active_hours`; departures are absolute times.
- Minutes are computed from absolute times at render time, so cached data
  stays accurate between fetches.

## Failure semantics

| Situation | What happens |
|---|---|
| A provider returns an error | That source backs off: `poll_interval × 2^failures`, at most ×64 and at most 10 min. The warning is logged on every failure. |
| A source's last refresh failed, but its last good data is younger than `max_stale` (default 10 min) | The snapshot keeps the cached departures and marks the source `Failing`. The clock shows them schedule-only (no live marker), because their predictions are as old as the last good fetch. |
| Every source is failing with nothing younger than `max_stale` | Nothing is published. The app's `lifetimeMs` (default 3 × `poll_interval`) removes it from the clock instead of showing "No departures". |
| A provider returns departures with a `*transit.PartialError` (such as one of several feeds failing) | The departures are used. The problem is logged when it starts, changes and clears, not on every poll. |
| The service stops | Every push carries `lifetimeMs` with `lifetimeExpiry: remove`, so the app leaves the clock. |
| Outside `active_hours` | No provider calls. The board is withdrawn once; on the clock the app is deleted, and a 404 counts as already gone. |
| The clock reboots | Pushed apps live in its RAM; the next tick pushes again. |
| Pushing to the clock fails | Logged; the next tick retries. |

## Configuration shape

A `[[boards]]` entry holds what the board is: its name, stops and filters,
polling, `window`, `active_hours`, `timezone`, `max_stale` and `per_row`.
Settings that only mean something on the clock (layout, labels, font,
overflow, lifetime, duration) go in its `[boards.clock]` table
(`config.BoardClock`), so a future output can add its own table beside
it. `BoardClock.Render` turns them into `render.Options`, and
`render.Options.Validate` owns their allowed values.

## Provider resolution

A `[providers.NAME]` block becomes a provider in `config.ResolveProvider`:

1. `system = "ID"`: the catalog entry's provider and options, overlaid with
   the block's other keys. The entry's `required_options` must be present.
2. Otherwise `type = "..."` names the provider.
3. Otherwise NAME is the provider type.

The options are decoded strictly (`config.ProviderOptions.Decode`), so a
mistyped key is an error that names the key but not its value.

## Adding things

- A city or agency: usually configuration only; see
  [CONTRIBUTING.md](../CONTRIBUTING.md).
- A provider: a package under `pkg/transit/providers/` that passes
  `transittest.Run`; see CONTRIBUTING.md for the rules.
- A display or output: planned in PLAN.md, "Direction".
