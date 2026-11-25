# Changelog

User-visible changes to Urbanist Timetable. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions
follow [Semantic Versioning](https://semver.org/); before 1.0, a minor
version may break configuration.

## [Unreleased]

### Added

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
  `transit.Quota` and `transit.ModeForRouteType` are new. `Query.Limit` and
  `Stop.Modes`, which the service never used, are removed, and
  `SortAndLimit` is now `SortByTime`. Providers return
  `transit.ErrUnknownStop` instead of an error of their own.

### Fixed

- `gtfsrt` no longer sends its `headers`, or the address it was redirected
  from, when a feed redirects to another host. Either could hold an API key.
- `gtfsrt` and `metrolisboa` boards with `poll_interval = "15s"` read fresh
  data on every poll, instead of often repeating the previous poll's.
- A trip without a destination no longer takes the place of a named one on
  the `departure` layout.
- Errors about a `base_url` or `token_url` that is not http(s) no longer
  print it, since it can hold credentials.
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

[Unreleased]: https://github.com/mjrossi/urbanist-timetable/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/mjrossi/urbanist-timetable/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/mjrossi/urbanist-timetable/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/mjrossi/urbanist-timetable/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/mjrossi/urbanist-timetable/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/mjrossi/urbanist-timetable/releases/tag/v0.1.0
