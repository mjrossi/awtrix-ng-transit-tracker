# Changelog

User-visible changes to Urbanist Timetable. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions
follow [Semantic Versioning](https://semver.org/); before 1.0, a minor
version may break configuration.

## [Unreleased]

### Changed

- **Breaking:** a board's clock settings (`layout`, `max_rows`,
  `overflow`, `lifetime`, `duration`, `font`, `empty_text`) move to a
  `[boards.clock]` table, and `[boards.labels]` becomes
  `[boards.clock.labels]`. Configs that still use the old keys fail to load,
  and the error names each key.
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

[Unreleased]: https://github.com/mjrossi/urbanist-timetable/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/mjrossi/urbanist-timetable/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/mjrossi/urbanist-timetable/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/mjrossi/urbanist-timetable/releases/tag/v0.1.0
