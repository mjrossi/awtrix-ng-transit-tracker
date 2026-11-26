# Urbanist Timetable — project conventions

This file is the contract for how code in this repo is written. Read it
before making non-trivial changes. How the code fits together is in
[`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md), the TC002 design rules in
[`docs/rendering.md`](./docs/rendering.md), the security model in
[`SECURITY.md`](./SECURITY.md), and the roadmap in
[`docs/PLAN.md`](./docs/PLAN.md).

## What this is

A Go command-line tool and service for public transit departures: it
prints them in a terminal (`departures`), and polls them for an AWTRIX NG
clock (Ulanzi TC002, 52 × 16) as one Docker container with a mounted
`config.toml` (`clock run`). It listens on no ports. What comes next is in
`docs/PLAN.md`, "Direction".

Part of the Urbanist family with *The Urbanist Lexicon* (mjrossi.com) and
*Urbanist Atlas*.

## Architecture rules

- **Agencies are data, adapters are code.** Most agencies are served by
  the generic GTFS-RT provider through configuration alone. A custom
  provider is only added when that path cannot serve an agency; see
  [`CONTRIBUTING.md`](./CONTRIBUTING.md).
- **Everything past a provider sees only `transit.Departure`.** The
  scheduler, renderer and clock client never import a provider package.
- **Providers must pass `transittest.Run`** against recorded responses in
  their `testdata/`, or hand-written ones where the source's terms restrict
  redistribution. Tests never call live APIs.
- **Providers are safe for concurrent use.** Boards naming the same
  provider block share one instance across goroutines.
- **Filtering happens in the core** (`transit.Filter`), not in providers.
- **Providers take `Query.Now`**, never the wall clock, so results are
  reproducible.
- **Every push sets `lifetimeMs`**, so stale data leaves the clock if the
  service stops.
- **AWTRIX NG rejects unknown keys.** Payload structs in `pkg/awtrix` use
  `omitempty` everywhere and only fields documented in the firmware's
  payload reference.

## Layout

- `cmd/urbanist-timetable/` — thin urfave/cli wrapper: `departures`,
  `stops search`, `systems`, `config init|example|validate`, `cache clear`,
  `clock run`, `clock preview`, `completion`. No business logic.
- `pkg/transit/` — the importable core: model, `Provider`, `Filter`,
  `Group`.
- `pkg/transit/registry/` — building providers by name from configuration.
- `pkg/transit/transittest/` — the provider contract suite.
- `pkg/transit/catalog/` — ready-made systems, one TOML file each in
  `systems/`. `just catalog-check` verifies them live; update `verified`
  when it passes.
- `pkg/transit/providers/<name>/` — one package per provider, registered in
  `pkg/transit/providers/providers.go`.
- `pkg/awtrix/` — typed AWTRIX NG HTTP client.
- `internal/config/` — TOML loading and validation.
- `internal/render/` — departure rows to TC002 layouts.
- `internal/termout/` — departures and stop lists to terminal text and
  JSON.
- `internal/fold/` — lowercase, unaccented text, shared by stop search
  and board names.
- `internal/scheduler/` — polling, active and quiet hours, backoff,
  last-good cache.
- `docs/` — `ARCHITECTURE.md`, `rendering.md`, `PLAN.md` (roadmap only:
  no build log or history).
- `CHANGELOG.md` — user-visible changes, Keep a Changelog format. Add an
  entry under `Unreleased` in the same change.

## Tooling

mise pins Go, just and golangci-lint: `mise install`, then `just` lists
the commands. The `go` line in `go.mod` is the oldest Go that importers may
use, and CI's `oldest-go` job builds with it; mise pins the newer version
used for development. `just ci` runs what CI runs. After an intended layout
change, `just golden` rewrites the renderer's golden files; review the
diff.

## Go conventions

Mirrors urbanist-atlas.

- **Standard library first.** Approved modules:
  - `github.com/urfave/cli/v3` — CLI and flags
  - `github.com/pelletier/go-toml/v2` — configuration
  - `github.com/google/go-cmp` — test diffs

  GTFS-RT is decoded from the protobuf wire format with `encoding/binary`
  (`pkg/transit/providers/gtfsrt/feed.go`), so no protobuf module is
  needed. Ask the maintainer before adding any other module.
- **Logging:** `log/slog` with an explicit `*slog.Logger`; JSON in
  production, text in development.
- **Errors:** stdlib `errors` and `fmt.Errorf("...: %w", err)`.
- **Config:** a TOML file for boards and providers; flags with `TIMETABLE_*`
  env-var fallbacks for process settings. `${NAME}` in clock and provider
  settings reads secrets from the environment. No viper.
- **Style:** golangci-lint v2 (`.golangci.yml`) is the single lint and
  format gate. US spelling (misspell enforces it).
- **Tests:** table-driven, `go-cmp` for diffs, fakes over mocks.
- **Module path:** `github.com/mjrossi/urbanist-timetable`.

## Comments

- A comment explains a non-obvious *why*: an invariant, a trade-off, or an
  external constraint (firmware rule, upstream API quirk). Code that reads
  clearly gets no comment.
- Delete comments that restate the code, narrate steps, or record history.
- Never assert another component's behavior unless a test pins it.
- Doc comments on exported identifiers stay, at one or two sentences.
- When you change code, fix or remove the comments it invalidates in the
  same commit.
