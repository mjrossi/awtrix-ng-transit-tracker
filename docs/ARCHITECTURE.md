# Architecture

How the service is built today. The roadmap is in [PLAN.md](./PLAN.md), the
TC002 design rules are in [rendering.md](./rendering.md), and the security
model is in [SECURITY.md](../SECURITY.md).

## Data flow

```
config.toml ──▶ internal/config ──▶ cmd (wire.go): one provider per block, one runner per board

each tick, per board (internal/scheduler.Runner):
  in quiet_hours or outside active_hours ──▶ Output.Withdraw (once)
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
data. `Snapshot.Rows(now)` filters and groups them, and drops the
predictions of a failing source, which are as old as its last good fetch.

The service makes outgoing requests only: to transit APIs and to the clock.
It listens on no ports.

## Packages and what they may import

| Package | Owns | Imports (project) |
|---|---|---|
| `pkg/transit` | `Departure`, `Line`, `Provider`, `Query`, `PartialError`, `Metered`, `Filter`, `Group`, `Registry`, `MatchesSearch`, `ModeForRouteType` | none |
| `pkg/transit/transittest` | the provider contract suite | `transit` |
| `pkg/transit/providers/<name>` | one data source each | `transit` only |
| `pkg/transit/providers` | the built-in registry | every provider |
| `pkg/transit/catalog` | embedded system entries (`systems/*.toml`) | none |
| `pkg/awtrix` | typed AWTRIX NG HTTP client and payload types | none |
| `internal/render` | departure rows → TC002 `awtrix.Layout` | `transit`, `awtrix` |
| `internal/scheduler` | polling, backoff, last-good cache, active and quiet hours, `Snapshot`, the `Output` interface | `transit` |
| `internal/clockout` | the clock `Output`: snapshot → TC002 app → push or expire; cleanup of removed boards | `scheduler`, `render`, `awtrix` |
| `internal/config` | TOML decoding, `${NAME}` expansion, validation, provider resolution, monthly request estimates | `transit`, `catalog`, `render`, `awtrix` |
| `cmd/urbanist-timetable` | CLI commands and wiring (`wire.go`) | everything |

Rules, from [CLAUDE.md](../CLAUDE.md):

- Nothing past a provider imports a provider package. The scheduler,
  outputs, renderer and clock client see only `transit.Departure`.
- The scheduler imports no output: display policy, such as how stale data
  looks, belongs to the output.
- `pkg/` is importable by other projects; `internal/` is not.
- Filtering happens in the core, not in providers.

## Concurrency model

- `run` starts one `scheduler.Runner` per board (`scheduler.RunAll`). Each
  runner ticks on its own `time.Ticker` and owns its per-source state, so
  that state needs no lock. A snapshot shares the cached departure slices
  with the runner; a refresh replaces a slice rather than changing it, and
  `Snapshot.Rows` copies before it changes anything.
- All runners share one output. `clockout.Output` keeps each board's last
  push time, under a mutex.
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
  | `metrolisboa` | stations | 24 h | keep last, retry in 15 min; with none yet, retry next poll |
  | `metrolisboa` | destinations | 24 h | keep last, retry in 15 min |
  | `metrolisboa` | each line's wait times | 15 s | not cached |
  | `metrolisboa` | OAuth token | until 1 min before expiry | refetched on 401 once |
  | `carrismetropolitana` | lines | 24 h | keep last, retry in 15 min |
  | `carrismetropolitana` | stop list (search only) | process lifetime | not cached |
  | `transitland` | each stop's departures, per window | `refresh_interval` (5 min) | keep last without predictions for one more `refresh_interval`, retry in `refresh_interval` or 5 min, whichever is shorter; with no data yet, retry next poll; an unknown stop counts as an answer |

  Every row except the token and stop list uses `upstream.Cache`
  (`pkg/transit/internal/upstream`), which is single-flight: one download
  per resource at a time, and other callers wait for it. Once a `gtfsrt`
  schedule exists, its daily refresh runs in a background goroutine
  detached from the poll's context, and callers keep using the previous
  schedule. Concurrent token fetches are harmless, so they are not
  deduplicated.
- `transitland` caches departures, not just reference data, because every
  request counts against a monthly quota. It fetches one `refresh_interval`
  beyond the window and windows the cached result at each poll's
  `Query.Now`, so boards keep their own poll interval. Providers with a
  quota implement `transit.Metered`; `config validate` and `run` pass their
  refresh intervals to `Config.EstimateRequests`, add up the blocks with the
  same `QuotaAccount`, and warn above the quota. Because an answer from the
  cache looks fresh to the scheduler, the provider itself stops marking
  predictions live after 10 minutes, and reports a failed fetch as a
  `*transit.PartialError` while the last timetable is less than two
  intervals old. Transitland filters by scheduled time, so to keep late
  vehicles the provider asks from 30 minutes back, in the agency's zone. It
  learns that zone's offset from each answer, so a first request uses
  `next`, without the lookback.
- The process stops on `SIGINT` or `SIGTERM`: the root context is canceled,
  runners return, and a canceled fetch counts as neither a success nor a
  failure.

## Time

- Providers compute windows from `Query.Now`, never the wall clock, so tests
  are reproducible. A provider's own `clock` field is used only where no
  query exists: stop search and OAuth token expiry.
- A runner takes `now` in the board's location (`timezone`, else the
  top-level `timezone`, else `TZ`). The location only affects
  `active_hours`; departures are absolute times. `quiet_hours` always follow
  the top-level `timezone` (`config.Config.Quiet`).
- Minutes are computed from absolute times at render time, so cached data
  stays accurate between fetches.

## Failure semantics

| Situation | What happens |
|---|---|
| A provider returns an error | That source backs off: `poll_interval × 2^failures`, at most ×64 and at most 10 min. The warning is logged on every failure. |
| A source's last refresh failed, but its last good data is younger than `max_stale` (default 10 min) | The snapshot keeps the cached departures and marks the source `Failing`. The clock shows them schedule-only (no live marker), because their predictions are as old as the last good fetch. |
| Every source is failing with nothing younger than `max_stale` | Nothing is published, which is logged when it starts. The app's `lifetimeMs` (default 3 × `poll_interval`) removes it from the clock instead of showing "No departures". |
| A provider returns departures with a `*transit.PartialError` (such as one of several feeds failing) | The departures are used. The problem is logged when it starts, changes and clears, not on every poll. |
| The service stops | Every push carries `lifetimeMs` with `lifetimeExpiry: remove`, so the app leaves the clock. |
| Outside `active_hours`, or in `quiet_hours` | No provider calls, and in `quiet_hours` no cleanup attempts. The board is withdrawn once; on the clock the app expires at once (see "Removed boards"). A failed withdrawal is retried until the app's lifetime has passed. |
| A board is removed from the configuration | At the next start, `clockout.Cleanup` compares the configured boards with the names in the state file and calls `awtrix.Client.ForgetApps`, retrying until the clock answers: after 1 min, doubling up to 1 h. See below. |
| The clock reboots | Pushed apps live in its RAM; the next tick pushes again. |
| Pushing to the clock fails | Logged; the next tick retries. |

## Removed boards

The clock keeps an app's name in its saved rotation after the app expires or
is deleted, and lists it as "no data". Only rewriting the whole rotation
(`PUT /api/v1/apps/order`) removes it, and the clock does not record who
pushed an app. So the service keeps its own record, the state file
(`--state`): the clock's address and the names of the boards it may have
pushed there. A removed board stays in the file until the clock has
forgotten it, so a restart resumes the cleanup. Names recorded for another
address are dropped, which is logged.

Neither this cleanup nor hiding a board uses
`DELETE /api/v1/apps/{name}`, which also erases the source of a script with
that name. A pushed app is replaced by one that expires after 1 ms instead
(`ExpireApp`), which cannot touch a script.

`ForgetApps` leaves the place of a name that belongs to a script or built-in
app. It asks `GET /api/v1/apps/script/{name}` as well as reading the app
list, because a pushed app hides a script of the same name there. Built-in
apps are hidden the same way and have no such lookup, so their names are
listed in the code. Before
rewriting the rotation it checks that the list is plausible (slots without
gaps, a built-in app); if any answer is doubtful it writes nothing and the
cleanup is retried. It writes even an unchanged rotation, because after a
507 the clock uses an order it has not saved. A change made in the web UI
between reading and writing would be lost.

## Configuration shape

`timezone` and `quiet_hours` apply to the whole service.
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
