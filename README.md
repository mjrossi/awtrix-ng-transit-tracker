# Urbanist Timetable

> Your next departure, wherever you look.

Live public transit departures for the displays around your home,
starting with [AWTRIX NG](https://github.com/Blueforcer/awtrix-ng) pixel
clocks (Ulanzi TC002).

A companion to [*The Urbanist Lexicon*](https://mjrossi.com/blog) and
[*Urbanist Atlas*](https://urbanistatlas.com).

```
Sta Apolónia
▌ 3  8  14
```

Each board shows one line and direction, like a platform sign: the
destination on top, then the line color (or bus number) and minutes until
the next departures. Live predictions are bright with an amber dot;
schedule-only times are dimmed. Add a board per direction and the clock
rotates through them. A `rows` layout fits two lines or directions on one
screen instead.

> **Status:** early, and running on a real TC002. Supported today:
> any agency publishing GTFS-Realtime, with six
> [ready-made systems](#systems) checked against their live feeds, plus
> dedicated providers for Metro de Lisboa and Carris Metropolitana. Only
> the 52 × 16 TC002 is supported; other displays are on the roadmap in
> [`docs/PLAN.md`](./docs/PLAN.md).
>
> **Renamed:** this project was `awtrix-ng-transit-tracker` until v0.2.0.
> The image is now `ghcr.io/mjrossi/urbanist-timetable`, the binary
> `urbanist-timetable`, and the environment variables `TIMETABLE_*`
> (formerly `TRANSIT_*`). Images published before the rename are at
> `ghcr.io/mjrossi/awtrix-ng-transit-tracker`.

## Getting started

You need an AWTRIX NG clock on your network and Docker on a machine that
stays on, such as a NAS. Images for `linux/amd64` and `linux/arm64` are
published to `ghcr.io/mjrossi/urbanist-timetable`:

| Tag | Contents |
|---|---|
| `latest`, `1`, `1.2`, `1.2.3` | Releases |
| `main` | The latest commit on `main`, for fixes not yet released |

```bash
docker pull ghcr.io/mjrossi/urbanist-timetable:latest
```

### 1. Create the config

```bash
cp config.example.toml config.toml
```

Set `clock.url` to your clock's address. The IP address is the safest
choice, because `awtrixng-xxxxxx.local` names often do not resolve inside
containers. As it ships, the example drives two boards from the `demo`
provider, so you can see the display working before choosing a real data
source.

The remaining commands run the CLI in the container. A shell alias keeps
them short:

```bash
alias urbanist-timetable='docker run --rm -v "$PWD/config.toml:/config/config.toml:ro" ghcr.io/mjrossi/urbanist-timetable:latest'
```

Create `config.toml` before using the alias. If the file is missing,
Docker creates an empty directory with that name in its place.

### 2. Pick a data source

```bash
urbanist-timetable systems                    # ready-made systems
urbanist-timetable systems show nyc-subway    # prints a block to paste into config.toml
```

If your agency is not listed, see [Providers](#providers): any agency
with GTFS-Realtime works through the `gtfsrt` provider.

### 3. Find your stop

```bash
urbanist-timetable stops search nyc-subway times sq
```

The first argument is a `[providers.NAME]` block in `config.toml`, a
system ID, or a provider that needs no settings.

### 4. Add a board

```toml
[providers.nyc]
system = "nyc-subway"

[[boards]]
name = "uptown"

[[boards.stops]]
provider = "nyc"
stop = "127N"        # Times Sq-42 St, northbound
lines = ["1"]

[boards.clock.labels] # names that fit the display
"Van Cortlandt Park-242 St" = "Van Cortlandt"
```

[`config.example.toml`](./config.example.toml) documents every setting.
Delete the demo boards and providers you do not use.

### 5. Check it

```bash
urbanist-timetable config validate     # checks the file and builds its providers and clock client
urbanist-timetable preview             # fetches once and prints what each board would show
urbanist-timetable preview --push      # ...and pushes it to the clock once
```

`preview` marks names too wide for their space on the display; give them shorter names
under `[boards.clock.labels]`.

### 6. Run it

```bash
docker run -d --name urbanist-timetable --restart unless-stopped \
  -v "$PWD/config.toml:/config/config.toml:ro" \
  -v timetable-data:/data \
  ghcr.io/mjrossi/urbanist-timetable:latest
docker logs -f urbanist-timetable
```

Or with Docker Compose:

```yaml
services:
  urbanist-timetable:
    image: ghcr.io/mjrossi/urbanist-timetable:latest
    restart: unless-stopped
    volumes:
      - ./config.toml:/config/config.toml:ro
      - timetable-data:/data
    # environment:
    #   AWTRIX_PASSWORD: ...   # referenced as ${AWTRIX_PASSWORD} in config.toml

volumes:
  timetable-data:
```

`/data` holds a small file naming the configured boards, so
that a board you remove from the configuration also leaves the clock's app
list. Mount a volume there, as above: without one, each new container starts
with an empty state file, and boards removed from the configuration are not
cleaned up after a restart. To use a folder instead of a named volume, make
it writable for the container's non-root user. Outside Docker, set `--state`
(or `TIMETABLE_STATE`) to a file path; without it, removed boards stay in
the clock's list as "no data".

The configuration is read at startup, so restart the container after
editing it. To update, run `docker compose pull && docker compose up -d`.
To build the image yourself instead, clone the repository and run
`docker build -t urbanist-timetable .`. The container only makes
outgoing requests, to transit APIs and to the clock, so it needs no published ports.

### Secrets

`${NAME}` in the `[clock]` and `[providers.*]` settings is read from the
environment, so keys and passwords can stay out of `config.toml`. Pass
them with `-e NAME=...` or Compose's `environment:`. A variable that is
referenced but not set is a configuration error.

## Commands

```bash
urbanist-timetable run                          # poll and push until stopped (the image's default)
urbanist-timetable preview [--board NAME]       # fetch once, print rows and payload
urbanist-timetable preview --push               # ...and push it to the clock once
urbanist-timetable preview --json               # print only the payload
urbanist-timetable config validate              # check config.toml
urbanist-timetable stops search PROVIDER WORDS  # find stop IDs
urbanist-timetable systems [show ID]            # ready-made systems
```

Global flags: `--config` (`TIMETABLE_CONFIG`, default `config.toml`; in the
image, `/config/config.toml`), `--log-format json|text`
(`TIMETABLE_LOG_FORMAT`, default `json`), `--log-level
debug|info|warn|error` (`TIMETABLE_LOG_LEVEL`), and `--version`, which prints
the release version.

## Configuration

See [`config.example.toml`](./config.example.toml). In short: an
optional `timezone` for `active_hours`, one `[clock]`, one
`[providers.NAME]` block per data source, and one `[[boards]]` entry per
app on the clock, each with one or more stops. A board's
`[boards.clock]` table holds how it looks on the clock: layout, labels,
lifetime and so on. Without `timezone`, the
`TZ` environment variable applies, which in the image is UTC unless you
set it. A board's own `timezone` overrides the top-level one for that
board's `active_hours`, for boards from another city; `"Local"` there
means the `TZ` zone.

## Systems

Ready-made settings for systems known to work:

| System | City | Modes |
|---|---|---|
| `lisbon-metro` | Lisbon | metro (needs a free API key) |
| `lisbon-carris-metropolitana` | Lisbon | bus |
| `nyc-subway` | New York | metro |
| `sf-bart` | San Francisco Bay Area | metro |
| `boston-mbta` | Boston | metro, tram, bus, rail, ferry |
| `helsinki-hsl` | Helsinki | metro, tram, bus, rail, ferry |

In `config.toml`:

```toml
[providers.nyc]
system = "nyc-subway"
```

Other settings in the block override the system's.

## Providers

| Provider | Covers | Key | Data license |
|---|---|---|---|
| `carrismetropolitana` | Carris Metropolitana buses, Lisbon metropolitan area | None | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/), © Carris Metropolitana |
| `metrolisboa` | Metro de Lisboa, all four lines | Free OAuth client from the [API Store](https://api.metrolisboa.pt/store/) | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/), © Metropolitano de Lisboa |
| `gtfsrt` | Any agency with GTFS and GTFS-Realtime TripUpdates, configured by URL (several per config with `type = "gtfsrt"`) | Depends on the agency | The agency's |
| `demo` | Generated departures for testing the display | None | — |

Find an agency's feeds in the [Mobility Database](https://mobilitydatabase.org/),
then:

```toml
[providers.myagency]
type = "gtfsrt"
static_url = "https://example.org/gtfs.zip"
realtime_urls = ["https://example.org/tripupdates.pb"]
```

The `gtfsrt` provider shows only departures with realtime predictions;
trips the feed does not list yet are missing until it does.

## How it behaves

- Each push sets a lifetime (three poll intervals by default), so the app
  disappears from the clock if the service stops instead of showing stale
  times.
- If a provider fails, the last good data is reused for up to `max_stale`
  (10 minutes by default) while the provider is retried with backoff.
  Minutes are recomputed on every push.
- Outside `active_hours` the app is removed and no requests are made.
- A board removed from the configuration is removed from the clock at the
  next start, including its place in the rotation. If the clock is off, this
  is retried with backoff, from one minute up to one hour. Apps of other
  senders, scripts and built-in apps are left alone.
- Logs show each board's configuration and changes of state. Set
  `TIMETABLE_LOG_LEVEL=debug` to log every fetch and push.
- The clock forgets pushed apps when it restarts; the next poll restores
  them.
- GTFS timetables are downloaded once a day to a temporary file in `/tmp`
  and only the needed parts are kept in memory. With a read-only root
  filesystem, mount a tmpfs at `/tmp`; otherwise downloads fall back to
  memory, which needs about as much RAM as the zip is large.

## Development

```bash
mise install     # Go, just, golangci-lint
just             # list commands
just ci          # lint, test, validate the example config
```

| Command | Does |
|---|---|
| `just run [config]` / `just preview [config]` | `run` or `preview` against `config.toml` with text logs |
| `just build` | Builds `bin/urbanist-timetable` |
| `just test` / `just lint` / `just fmt` | Tests with the race detector; lint; format |
| `just golden` | Rewrites the renderer's golden files after an intended layout change |
| `just catalog-check` | Checks every catalog system against its live feeds |
| `just docker-build` / `just docker-run [config]` | Builds the image; runs it against `config.toml` |

Secrets for local runs go in `mise.local.toml` (gitignored); copy
[`mise.local.toml.example`](./mise.local.toml.example). `just
catalog-check` reads system keys as `<SYSTEM>_<OPTION>`, such as
`LISBON_METRO_CLIENT_ID`, and skips systems without them.

See [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) for how the code fits
together, [`CLAUDE.md`](./CLAUDE.md) for conventions,
[`CONTRIBUTING.md`](./CONTRIBUTING.md) for adding a city or provider, and
[`SECURITY.md`](./SECURITY.md) for the security model and reporting.

## License

Code is licensed under the [Apache License 2.0](./LICENSE). Transit data
belongs to its publishers and keeps their licenses; see
[`NOTICE`](./NOTICE) for data included in this repository.
