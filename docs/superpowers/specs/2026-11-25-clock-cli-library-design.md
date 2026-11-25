# Design: finish the clock, add a CLI, polish the library

Date: 2026-11-25. Status: approved 2026-11-25.

Items marked **(call)** were proposed by Claude and not discussed
individually; check those first.

## Intent

Urbanist Timetable is a fun side project that does not need to go anywhere
specific, and a good thing to show on a resume. A TC002 board alone is
niche. A command-line tool for querying transit APIs, and a `pkg/` others
can import, widen its reach by reusing code that already exists.

The earlier roadmap (HTTP JSON, MQTT and Home Assistant, a hosted API) is
dropped, because other projects already do those well: Transit Tracker and
Transiter, gtfs2 and OpenPublicTransport, and Transitous.

### Goals

- A finished, well-scoped clock service.
- `departures`: see a stop's next departures in a terminal in one command,
  with no config file for systems that need no key.
- A `pkg/` that someone outside this repository can use.

### Non-goals

- Trip planning, vehicle positions, alerts, or a full-screen interface.
- New providers, unless the maintainer needs one. Transitous is out: its
  terms make the project answerable for every user's polling.
- A server of any kind. The process still listens on no ports.
- Prebuilt binaries or Homebrew, until someone asks.
- A stability promise. The module stays before 1.0.

### Anti-goals

- A CLI that downloads a whole timetable on every command.
- Generalizing for users who do not exist.

## Approaches considered

| Approach | Tradeoff | Outcome |
|---|---|---|
| Use Transitous for most data | One keyless API, but terms the maintainer does not like, and 0 live departures sampled for Metro de Lisboa and the NYC subway | Rejected |
| Split `pkg/` into modules | The HTTP providers already use only the standard library; a split mostly adds multi-module tagging | Rejected |
| CLI 1: `departures` added to the clock's binary | Least change, but the config and commands stay clock-first | Not chosen |
| CLI 2: a second binary | Shorter name, two programs to release | Rejected |
| **CLI 3: the CLI is the product, the clock a command group** | Breaks command names, but the maintainer is the only user | **Chosen** |

## Release 1: v0.5.0, finish the clock

1. **Check PR #10 on the TC002** (maintainer): badge and dot spacing in the
   `grid` layout, the one item its description lists as unchecked that
   affects rendering. Fix what that finds, then merge.
2. **Trim the documents:**
   - `docs/PLAN.md`: replace "Direction" with the CLI and library releases
     below. From "Later", keep **(call)**: schedule-only `gtfsrt`, first
     departure in the empty state, section cards, pages for busy stops,
     destinations from the last stop. Move the 32 × 8 renderer to
     "contributions welcome". Drop: MQTT, the Home Assistant add-on,
     on-device mode. Note the third-party CP `TripUpdates` feed as
     unverified.
   - `SECURITY.md`: remove "When HTTP output lands".
   - `CLAUDE.md`, `README.md`, `ARCHITECTURE.md`: remove references to
     planned outputs, including the planned MQTT module.
   - `CONTRIBUTING.md`: catalog entries stay welcome; new adapters are
     frozen unless the maintainer needs one.
3. **Release** by the steps in `CONTRIBUTING.md`. No command or
   configuration changes.

## Release 2: v0.6.0, the CLI

### Commands

```
urbanist-timetable departures <source> <stop>...     new
urbanist-timetable stops search <source> <words>...
urbanist-timetable systems [show <id>]
urbanist-timetable config validate
urbanist-timetable clock run                         was: run
urbanist-timetable clock preview [--push]            was: preview
```

No aliases for the old names. The image's default command becomes
`["clock", "run"]`. A deployment changes only if it passes `run` itself.

### `departures`

```
$ urbanist-timetable departures nyc-subway 127N --lines 1,2,3
Times Sq-42 St                                              22:31
1   Van Cortlandt Park-242 St      2   9   16
2   Wakefield-241 St               4  14
3   Harlem-148 St                  6  18
```

- **Source:** a provider block in the config, a catalog system, or a
  provider type that needs no settings, as `stops search` accepts today.
  With `--realtime URL` (repeatable) and optional `--static URL`, there is
  no source argument and every argument is a stop. Feeds that need headers
  need a config block, so keys stay out of process listings **(call)**.
- **Keys without a config (call):** a system's required options are read
  from `<SYSTEM>_<OPTION>`, such as `LISBON_METRO_CLIENT_ID`. `systems show`
  prints these names and `just catalog-check` reads them already.
- **Several stops** merge into one list, for complexes with a station per
  group of lines.
- **Flags:** `--lines`, `--headsigns`, `--min-lead`, `--canceled`
  (all map to `transit.Filter`); `--window` (default 90m); `--per-row`
  (default 3); `--limit` (rows, default no limit); `--json`;
  `--watch`, with `--interval` (default 30s, at least `MinPollInterval`).
- **Text:** rows from `transit.Group`, `*` for schedule-only times as in
  `preview`, `x` for canceled ones. Control characters in feed text are
  removed, because feeds are not trusted. Line colors only on a terminal with `NO_COLOR` unset. The
  header time is the user's local time, since an agency's time zone is not
  known.
- **Stop name (call):** a new optional interface, `transit.StopNamer`, is
  implemented where the name is already loaded (`gtfsrt`, `metrolisboa`).
  Otherwise the header shows the stop ID; a name is not worth a
  metered request or a large download.
- **`--watch`** redraws in place and needs a terminal. With `--json` it is
  an error. A failed refresh keeps the last result and says how old it is.
  It stops on Ctrl-C with exit code 0.
- **`--json`** has its own types, not tags on `transit.Departure`. The help
  text says the shape may change before 1.0.

  ```json
  {"source": "nyc-subway", "at": "2026-11-25T22:31:00-05:00", "partial": false,
   "departures": [{"stop": "127N", "line": {"id": "1", "name": "1",
     "color": "#D82233", "mode": "metro"}, "headsign": "Van Cortlandt Park-242 St",
     "scheduled": "...", "predicted": "...", "status": "scheduled", "minutes": 2}]}
  ```

  `scheduled` and `predicted` are left out when unknown.
- **Results and errors:** no departures prints "No departures in the next
  90 minutes" and exits 0. A `PartialError` prints the result and a warning
  on stderr, and exits 0. An unknown stop exits 1 and suggests
  `stops search`. Warnings are plain text, not slog. Secrets stay out of
  messages, as `SECURITY.md` requires.

Formatting lives in a new `internal/termout`, which sees only
`transit.Departure`. The command stays thin.

### Unknown stops in `gtfsrt`

`gtfsrt` never returns `ErrUnknownStop`, so a mistyped stop would look like
a quiet one. It will return it when the schedule is loaded, every realtime
feed loaded, the stop is not in the schedule, and no feed serves it.
On the clock, such a board then backs off as failing instead of showing
"No departures", which is a visible change for the changelog.

### Disk cache for the GTFS schedule

Today the parsed schedule lives only in memory, so each command would
download the zip again.

- `gtfsrt.New` and `gtfsrt.Register` take options; `WithCacheDir(dir)` turns
  the cache on. `providers.Registry` passes it through. It is not a config
  key, so strict decoding in other providers is unaffected.
- Each entry is one file: a JSON line with the ETag and Last-Modified, then
  the zip. One file, because two could be replaced by different processes.
  Its modification time is the fetch time. It is named by the SHA-256 of the
  URL, so a key in a URL is not written to disk. Directory mode 0700, files
  0600.
- Younger than 24 h: no request. Older: a conditional request; 304 renews
  it. If the download fails, the old zip is used with a `PartialError`.
- Written to a temporary file in the same directory, then renamed, so
  concurrent processes never read half a file. A zip that does not parse is
  deleted and downloaded once more.
- If the directory cannot be used, behavior falls back to today's.
- Ages are measured from the `now` passed to the cache load
  (`upstream.NewCacheAt`), not the wall clock.
- Global flag `--cache-dir` (`TIMETABLE_CACHE`), default
  `os.UserCacheDir()/urbanist-timetable`; `off` disables it. The image sets
  `/data/cache`, so restarts also stop downloading again. `SECURITY.md` and
  the README say what is stored there.

**Measure first.** The first task is a throwaway timing of NYC and HSL,
run with `go` and not mise, with a cold and a warm zip. Targets, excluding
realtime requests: 1 s for NYC and 3 s for HSL when warm **(call)**. If HSL
misses, store only routes, stops and trips in the cached zip; if it still
misses, stop and bring the numbers back before adding a parsed index.

### Configuration

- **`[clock]` and `[[boards]]` become optional** for `departures`, `stops`
  and `config validate`. If either is present both are required, so a
  mistake in a clock config is still caught. `clock run` and
  `clock preview` require both.
- **Only the queried provider block is expanded and resolved** by
  `departures` and `stops search`. Today, an unset `${AWTRIX_PASSWORD}` or a
  mistake in another block makes a search fail.
- **Default path (call):** `$XDG_CONFIG_HOME/urbanist-timetable/config.toml`,
  else `~/.config/urbanist-timetable/config.toml`. The current default,
  `config.toml` in the working directory, is a common file name, and an
  installed tool would fail on an unrelated one. The image sets
  `TIMETABLE_CONFIG` already and the `just` recipes pass `--config`.
  `mise.toml` sets `TIMETABLE_CONFIG=config.toml` for work in this
  repository.

### Opening a system from outside

`ResolveProvider` and `ProviderOptions` move from `internal/config` to
`pkg/transit/catalog`, so `config` still imports no provider. A new
`providers.Open(source string, settings map[string]any, opts ...)`
resolves and builds. Library errors are typed (`ErrUnknownSystem`,
`MissingOptionsError`); the command adds its "run `systems`" hints.

### Tests and documents

- `termout`: golden tests for text, color and JSON.
- Commands: `demo` and `httptest` feeds; no live calls.
- Disk cache: fresh, 304, failed refresh, corrupt zip, unusable directory,
  two processes' worth of concurrent writers, and no URL in any file name
  or file.
- Config: each combination of clock and boards; an unset variable in an
  unqueried block.
- `gtfsrt`: the four conditions for an unknown stop; `transittest` passes.
- README leads with "transit departures in your terminal, or on a pixel
  clock", with `go install` first and the clock prominent. Update
  ARCHITECTURE, SECURITY, CLAUDE, CONTRIBUTING, the example config, the
  Dockerfile, `justfile` and CHANGELOG.

### Order

1. Timing measurement. 2. Disk cache. 3. `catalog` and `providers.Open`.
4. Config changes. 5. Unknown stops and `StopNamer`. 6. `termout` and
`departures`. 7. Command regrouping, image and documents. Each is its own
reviewable change, and 1 to 5 change no command names.

## Release 3: v0.7.0, the library

1. **Core without config code.** `Registry`, `Factory`, `Options`,
   `RegisterConfig` and `ErrUnknownProvider` move to
   `pkg/transit/registry`. `Metered` and `Quota` stay in `transit`
   **(call)**: after reading their uses, they describe a provider rather
   than its configuration.
2. **Go version.** `go.mod` asks for `go 1.27.3`, which forces importers to
   that patch. Lower it to the oldest supported release that builds and
   passes tests (target 1.26), with a `toolchain` line, and build with that
   version in CI. If the code needs 1.27, use `1.27.0`.
3. **Examples.** Runnable `Example` tests, in external test packages, for
   `transit`, `gtfsrt`, `catalog`, `transittest` and `awtrix`.
4. **An outside importer.** `examples/departures`, a module of its own with
   a `replace`, under 50 lines, built in CI. It proves that nothing needed
   is in `internal/`.
5. **Documents.** Package comments reviewed, and a README section, "Use it
   from Go".

## Constraints

- `CLAUDE.md` conventions: no new modules, US spelling, table-driven tests,
  `Query.Now`, secrets out of messages.
- `gtfsrt` still shows only departures with realtime predictions, and the
  CLI help says so.
- Existing golden files do not change in releases 2 and 3.

## Left to the builder

Internal names, file layout within a package, exact column widths, and
wording of help and messages.
