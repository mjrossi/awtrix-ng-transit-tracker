# Changelog

User-visible changes to Urbanist Timetable. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions
follow [Semantic Versioning](https://semver.org/); before 1.0, a minor
version may break configuration.

## [Unreleased]

### Added

- `agencies search <words>` finds systems by agency or place: the
  ready-made ones, every feed in the Mobility Database's public list, and,
  with `TRANSITLAND_API_KEY` set, Transitland's operators. A result's ID,
  such as `mdb-2929` or `f-eyc-cp`, works in `departures`, `stops search`
  and `systems show` without a configuration file. `systems show` prints
  its `[providers]` block, with `${NAME}` for any key the feeds need and
  where to get one, and warns about schedules too large for a small
  machine. Where Transitland lists the feed URLs and they need no key, the
  block uses `gtfsrt`, which spends no Transitland quota on departures;
  working that out takes two or more Transitland requests, kept a week in
  the cache directory. `cache clear` also removes the saved feed list and
  these lookups.
- `gtfsrt` shows timetabled trips from the schedule, with the realtime
  prediction where a feed has one, so a stop no longer reads "No
  departures" while a scheduled bus is 20 minutes away, and a realtime
  outage no longer blanks a board. Delays sent without a time, and delays
  carried forward from an earlier stop, now apply. Feeds that name trips
  by route and start time instead of trip ID, such as HSL's, are matched
  too. `realtime_urls` is optional, so an agency that publishes only a
  schedule works, and `departures --static URL STOP` needs no
  `--realtime`. Trips without a headsign are named after their last
  stop. Where realtime trips do not match the schedule's, lines with
  realtime trips show only those and a warning is logged;
  `timetable = false` turns timetabled trips off, as `mta-subway` does.
  Without `realtime_urls`, a schedule that gives no timetable is an error
  rather than an empty board.
  The first query for a stop reads the schedule's `stop_times.txt` (about
  5 seconds for HSL), and the result is saved in the cache directory.
- Release binaries for Linux, macOS and Windows (amd64 and arm64), with
  checksums and build provenance, on each GitHub release. Install with
  `mise use -g github:mjrossi/urbanist-timetable` or from the release page.
- `cache clear` deletes the cached GTFS schedules, which otherwise stay in
  the cache directory even after no board uses them. Library users can call
  `providers.ClearScheduleCache` or `gtfsrt.ClearCache`.
- GTFS schedule downloads are logged to stderr, with their size and time, so
  a first run that downloads several schedules no longer sits silent.
  `--log-level debug` also logs each stop `departures` fetches and how long it
  took. A bad `--log-level` or `--log-format` is rejected before any command
  runs. Library users can pass `providers.WithLogger` or `gtfsrt.WithLogger`.
- Errors from the command line come with a hint for what to do next. A
  mistyped command, flag, system or source gets a "did you mean". A stop
  name given where an ID belongs gets the `stops search` command that
  finds its ID. A provider that needs settings gets a pointer to where they
  go.
- `config init` writes a starting configuration. At a terminal it asks for
  an agency or city and searches for systems as `agencies search` does,
  then searches for your stop, offers to keep some of its lines, and saves
  a board; `--system` and `--stop` do the same without questions. A
  system found in the Mobility Database or Transitland is written out in
  full, with its agency's name for the block (or its ID, for a name
  without Latin letters) and `${NAME}` for any key, so the file does not
  depend on the search. A schedule too large for a small machine is
  warned about before it is downloaded.
- `departures` with no arguments shows every board in the configuration,
  and `departures NAME` one board, with the board's filters, `window` and
  `per_row`; flags given on the command line replace them. Boards no longer
  need a `[clock]`, so the configuration can serve the terminal alone.
  Boards and stops are fetched at the same time.
- Shell completion for bash, zsh and fish (`completion`), covering
  commands, flags, systems, provider blocks and board names.
- `config example` prints the annotated example configuration.
- `stops search --json`.
- `--help` gives examples for each command, and the root help a
  getting-started sequence.
- `systems` names the environment variables a system needs, and marks
  those already set.
- `departures` says how many departures `--lines`, `--headsigns` or
  `--min-lead` hid when they hid all of them, and which lines the stop has.

- The README explains how to use the packages from Go, and
  `examples/departures` is a complete program that does. Runnable examples
  document the main packages.
- For programs that import `pkg/`: `transit.Filter.Narrows` reports
  whether a filter asks for less than every departure.

### Changed

- Catalog systems are renamed after their agency: `mta-subway` (was
  `nyc-subway`), `mbta` (`boston-mbta`), `hsl` (`helsinki-hsl`), `bart`
  (`sf-bart`), `metro-lisboa` (`lisbon-metro`) and `carris-metropolitana`
  (`lisbon-carris-metropolitana`). Configurations that name a system with
  `system =` need the new ID; an old ID's error names the new one. The
  environment variables for Metro de Lisboa's keys are now
  `METRO_LISBOA_CLIENT_ID` and `METRO_LISBOA_CLIENT_SECRET`. `systems`
  shows each system's agency.
- A board's stops are fetched at the same time on each poll, rather than
  one after another.
- Several errors at once, such as from `config validate` or from several
  failing stops, are printed one per line instead of run together.
- A bad flag or argument prints a short error and points to `--help`,
  instead of printing the whole help.
- A file named with `--config` must exist. `TIMETABLE_CONFIG` may still
  name one that does not, as the Docker image does by default, for
  commands that work without a configuration.
- `stops search` aligns its IDs to the longest one, and at a terminal
  prints the `departures` command to use next.
- Stop search also ignores the accents in å, ø, ý and ÿ.
- Configuration: `[[boards]]` without `[clock]`, or `[clock]` without
  boards, is valid; `clock run` and `clock preview` still require both.
- `departures --json` for configured boards prints an array of boards,
  each with a `board` name.
- `--log-format` is `text` by default when stderr is a terminal, and
  `json` otherwise.

- For programs that import `pkg/`: the module needs Go 1.26, down from
  1.27.3.
- For programs that import `pkg/`: `Registry`, `Options`, `Factory`,
  `RegisterConfig` and `ErrUnknownProvider` move from `transit` to the new
  `registry` package; `NewRegistry` is `registry.New`, and `Registry.New`
  is `Registry.Build`. The core `transit` package has no configuration code
  and no dependencies outside the standard library.

## [0.6.0] - 2026-11-25

### Added

- `departures`: the next departures at one or more stops, in the terminal.
  The source is a provider block, a catalog system or a provider type, or
  feed URLs with `--realtime` and `--static`. Filters match the board
  settings (`--lines`, `--headsigns`, `--min-lead`, `--canceled`); `--json`
  prints JSON whose shape may change before 1.0, and `--watch` redraws.
- `--cache-dir` (`TIMETABLE_CACHE`) keeps GTFS schedules between runs,
  revalidating them after a day. It defaults to the user's cache directory;
  the Docker image uses `/data/cache`, so a restart no longer downloads
  the schedule again. `off` turns it off.
- An unknown stop or system suggests the command that finds the right one.

### Changed

- `run` and `preview` are now `clock run` and `clock preview`, beside the
  terminal commands. The Docker image runs `clock run` by default; a
  deployment that passes `run` itself must pass `clock run` instead.
- A `gtfsrt` board on a stop that is in neither the schedule nor any feed
  now fails, and backs off, instead of showing "No departures". A typo in a
  stop ID used to look like a quiet stop.
- For programs that import `pkg/`: `transit.StopNamer` gives a stop's name
  where a provider already holds it (`gtfsrt`, `metrolisboa`).
- The configuration file defaults to
  `~/.config/urbanist-timetable/config.toml` (or under `$XDG_CONFIG_HOME`)
  instead of `config.toml` in the working directory. Pass
  `--config config.toml` for the old behavior. The Docker image is
  unaffected: it sets `TIMETABLE_CONFIG`.
- `[clock]` and `[[boards]]` are optional together, so a file can hold only
  provider blocks for `stops search`. `run` and `preview` still need both.
- `stops search` reads only the named provider block, so an unset variable
  or an invalid setting elsewhere in the file no longer stops it. A
  misspelled key anywhere is still an error. A catalog system
  that needs keys reads them from `<SYSTEM>_<OPTION>` environment variables,
  such as `LISBON_METRO_CLIENT_ID`, when the file has no block for it.
- `config validate` reports providers as well as boards.
- For programs that import `pkg/`: `catalog.Resolve` and `catalog.Options`
  replace the internal provider resolution, and `providers.Open` builds a
  provider from a system or type name. Unknown systems and missing keys are
  typed errors (`catalog.ErrUnknownSystem`, `catalog.MissingOptionsError`).
- `gtfsrt.New` and `gtfsrt.Register` take options; `gtfsrt.WithCacheDir`
  keeps schedules on disk between processes.

## [0.5.0] - 2026-11-25

### Added

- `grid` layout: up to four lines of a busy stop on one board, each with its
  next departure and no destination.
- The README covers Transitland in more detail: plans and quotas, agencies
  known to work, live and scheduled times, and an example.
- `transitland` provider: thousands of agencies through the Transitland
  API, with timetabled trips shown alongside realtime ones. Needs a free
  API key. Each stop is fetched once per `refresh_interval` (5 minutes by
  default) to stay within the plan's monthly quota.
- `config validate` prints the estimated requests a month for each
  Transitland key, adding up blocks that share one, and warns when they
  exceed the smallest `monthly_quota` those blocks set; `run` logs the same
  at startup.

### Changed

- For programs that import `pkg/transit`: `transit.Metered`,
  `transit.Quota`, `transit.ModeForRouteType` and `Query.Contains` are new.
  `Query.Limit` and `Stop.Modes`, which the service never used, are removed,
  and `SortAndLimit` is now `SortByTime`. Providers return
  `transit.ErrUnknownStop` instead of an error of their own.

### Fixed

- `gtfsrt` no longer sends its `headers`, or the address it was redirected
  from, when a feed redirects to another host. Either could hold an API key.
- `metrolisboa` no longer sends its credentials when redirected to a
  subdomain or from https to http.
- `gtfsrt` and `metrolisboa` boards with `poll_interval = "15s"` read fresh
  data on every poll, instead of often repeating the previous poll's.
- A trip without a destination no longer takes the place of a named one on
  the `departure` layout.
- Errors about a `base_url` or `token_url` that is not http(s) no longer
  print it, since it can hold credentials.
- `stops search` lists `metrolisboa` stations of the same name in a stable
  order.
- The notes for `sf-bart` name the labels table correctly,
  `[boards.clock.labels]`.

## [0.4.0] - 2026-11-25

### Changed

- Errors in a config value name its key, and errors in `active_hours` also
  name the board.

### Fixed

- Hiding a board outside its active hours can no longer delete a script of
  the same name from the clock.
- Boards removed from the config now also leave the clock's app list,
  instead of staying there as "no data". This needs a state file that
  survives restarts (`--state` or `TIMETABLE_STATE`); the Docker image keeps
  it in `/data`, so mount a volume there. Boards removed before this version
  have to be deleted once in the clock's web UI.
- Names with accents, such as "São Sebastião", use the `matrix-light6` font,
  because the `small` font runs accents into their letters. Set
  `name_font = "small"` to keep the old look.
- A board that cannot be hidden because the clock is unreachable is no longer
  retried on every poll after its app has expired.
- Stopping the service no longer logs a publish warning.
- A board is no longer shown after its `active_hours` end because a fetch was
  still running, and is hidden even if its last push reported an error.

### Added

- `quiet_hours`, a night mode: every board is hidden during a daily window,
  and the service makes no requests except to hide them.
- `run --state` (`TIMETABLE_STATE`), the file that remembers the configured
  boards.
- The README explains what happens outside `active_hours`.
- `name_font` in `[boards.clock]` sets the font of destination names.
- Logs say which boards and stops are configured at startup, and when a
  board is first shown, loses all usable data, recovers, or is hidden and why.
  `--log-level debug` also logs every fetch and push.
- The README states the minimum firmware, AWTRIX NG 1.2.2: earlier releases
  have no layouts.

### Removed

- `awtrix.Client.DeleteApp`, which also erased a script of the same name. Use
  `ExpireApp`.

## [0.3.0] - 2026-11-24

### Changed

- **Breaking:** a board's clock settings (`layout`, `max_rows`,
  `overflow`, `lifetime`, `duration`, `font`, `empty_text`) move to a
  `[boards.clock]` table, and `[boards.labels]` becomes
  `[boards.clock.labels]`. Configs that still use the old keys fail to load,
  and the error names each key. To migrate, move the keys under a new
  table:

  ```toml
  [[boards]]
  name = "metro"
  poll_interval = "45s"

  [boards.clock]
  layout = "rows"

  [boards.clock.labels]
  "Santa Apolónia" = "Sta Apolónia"
  ```
- Boards that use the same provider share one download for each resource,
  so polling several boards together no longer sends duplicate API
  requests.
- Metro de Lisboa no longer retries a failed station download on every
  poll.

### Added

- An optional `timezone` for each board, used for its `active_hours`.
  Boards with no `timezone` use the top-level `timezone`, then `TZ`.
- `preview` marks names that will scroll in the `rows` layout, not only in
  `departure`.
- `docs/ARCHITECTURE.md`, `docs/rendering.md` and `SECURITY.md`.

### Removed

- Unused render color options.

## [0.2.0] - 2026-11-23

### Changed

- **Breaking:** renamed from `awtrix-ng-transit-tracker` to Urbanist
  Timetable. The image is now `ghcr.io/mjrossi/urbanist-timetable`, the
  binary `urbanist-timetable`, and the environment variables
  `TIMETABLE_CONFIG`, `TIMETABLE_LOG_FORMAT` and `TIMETABLE_LOG_LEVEL`
  (formerly `TRANSIT_*`). `config.toml` is unchanged.
- Design pass on the TC002 layouts.

## [0.1.1] - 2026-11-23

### Fixed

- The `rows` layout no longer cuts off the first departure time. The
  times box was 1–2 px too narrow.

## [0.1.0] - 2026-11-23

First release: live departures on an AWTRIX NG clock (Ulanzi TC002).

### Added

- Generic GTFS-Realtime provider, plus providers for Metro de Lisboa and
  Carris Metropolitana.
- Ready-made systems: Boston MBTA, Helsinki HSL, Lisbon Carris
  Metropolitana, Lisbon Metro, NYC Subway and SF BART.
- Commands: `run`, `preview`, `config validate`, `systems`, `stops search`.

[Unreleased]: https://github.com/mjrossi/urbanist-timetable/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/mjrossi/urbanist-timetable/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/mjrossi/urbanist-timetable/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/mjrossi/urbanist-timetable/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/mjrossi/urbanist-timetable/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/mjrossi/urbanist-timetable/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/mjrossi/urbanist-timetable/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/mjrossi/urbanist-timetable/releases/tag/v0.1.0
