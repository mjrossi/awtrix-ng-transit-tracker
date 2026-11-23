# awtrix-ng-transit-tracker

Shows upcoming public transit departures on an
[AWTRIX NG](https://github.com/Blueforcer/awtrix-ng) pixel clock (Ulanzi
TC002).

```
Sta Apolónia
▌ 3  8  14
```

Each board shows one line and direction, like a platform sign: the
destination on top, then the line color (or bus number) and minutes until
the next departures. Live predictions are bright with an amber dot;
schedule-only times are dimmed. Add a board per direction and the clock rotates through them. A
`rows` layout fits two lines or directions on one screen instead.

> **Status:** early, and not yet tried on a real clock. Supported today:
> any agency publishing GTFS-Realtime, with six
> [ready-made systems](#systems) checked against their live feeds, plus
> dedicated providers for Metro de Lisboa and Carris Metropolitana. Only
> the 52 × 16 TC002 is supported. See [`docs/PLAN.md`](./docs/PLAN.md).

## Getting started

You need an AWTRIX NG clock on your network and Docker on a machine that
stays on, such as a NAS. Images for `linux/amd64` and `linux/arm64` are
published to `ghcr.io/mjrossi/awtrix-ng-transit-tracker`:

| Tag | Contents |
|---|---|
| `latest`, `1`, `1.2`, `1.2.3` | Releases |
| `main` | The latest commit on `main`, for fixes not yet released |

```bash
docker pull ghcr.io/mjrossi/awtrix-ng-transit-tracker:latest
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
alias transit-tracker='docker run --rm -v "$PWD/config.toml:/config/config.toml:ro" ghcr.io/mjrossi/awtrix-ng-transit-tracker:latest'
```

Create `config.toml` before using the alias. If the file is missing,
Docker creates an empty directory with that name in its place.

### 2. Pick a data source

```bash
transit-tracker systems                    # ready-made systems
transit-tracker systems show nyc-subway    # prints a block to paste into config.toml
```

If your agency is not listed, see [Providers](#providers): any agency
with GTFS-Realtime works through the `gtfsrt` provider.

### 3. Find your stop

```bash
transit-tracker stops search nyc-subway times sq
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

[boards.labels]       # names that fit the display
"Van Cortlandt Park-242 St" = "Van Cortlandt"
```

[`config.example.toml`](./config.example.toml) documents every setting.
Delete the demo boards and providers you do not use.

### 5. Check it

```bash
transit-tracker config validate     # checks the file and builds its providers and clock client
transit-tracker preview             # fetches once and prints what each board would show
transit-tracker preview --push      # ...and pushes it to the clock once
```

`preview` marks names too wide for the display; give them shorter names
under `[boards.labels]`.

### 6. Run it

```bash
docker run -d --name transit-tracker --restart unless-stopped \
  -v "$PWD/config.toml:/config/config.toml:ro" \
  ghcr.io/mjrossi/awtrix-ng-transit-tracker:latest
docker logs -f transit-tracker
```

Or with Docker Compose:

```yaml
services:
  transit-tracker:
    image: ghcr.io/mjrossi/awtrix-ng-transit-tracker:latest
    restart: unless-stopped
    volumes:
      - ./config.toml:/config/config.toml:ro
    # environment:
    #   AWTRIX_PASSWORD: ...   # referenced as ${AWTRIX_PASSWORD} in config.toml
```

The configuration is read at startup, so restart the container after
editing it. To update, run `docker compose pull && docker compose up -d`.
To build the image yourself instead, clone the repository and run
`docker build -t awtrix-ng-transit-tracker .`. The container only makes outgoing requests, to transit APIs
and to the clock, so it needs no published ports.

### Secrets

`${NAME}` in the `[clock]` and `[providers.*]` settings is read from the
environment, so keys and passwords can stay out of `config.toml`. Pass
them with `-e NAME=...` or Compose's `environment:`. A variable that is
referenced but not set is a configuration error.

## Commands

```bash
transit-tracker run                          # poll and push until stopped (the image's default)
transit-tracker preview [--board NAME]       # fetch once, print rows and payload
transit-tracker preview --push               # ...and push it to the clock once
transit-tracker preview --json               # print only the payload
transit-tracker config validate              # check config.toml
transit-tracker stops search PROVIDER WORDS  # find stop IDs
transit-tracker systems [show ID]            # ready-made systems
```

Global flags: `--config` (`TRANSIT_CONFIG`, default `config.toml`; in the
image, `/config/config.toml`), `--log-format json|text`
(`TRANSIT_LOG_FORMAT`, default `json`), `--log-level
debug|info|warn|error` (`TRANSIT_LOG_LEVEL`), and `--version`, which prints
the release version.

## Configuration

See [`config.example.toml`](./config.example.toml). In short: an
optional `timezone` for `active_hours`, one `[clock]`, one
`[providers.NAME]` block per data source, and one `[[boards]]` entry per
app on the clock, each with one or more stops. Without `timezone`, the
`TZ` environment variable applies, which in the image is UTC unless you
set it.

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
| `just build` | Builds `bin/transit-tracker` |
| `just test` / `just lint` / `just fmt` | Tests with the race detector; lint; format |
| `just golden` | Rewrites the renderer's golden files after an intended layout change |
| `just catalog-check` | Checks every catalog system against its live feeds |
| `just docker-build` / `just docker-run [config]` | Builds the image; runs it against `config.toml` |

Secrets for local runs go in `mise.local.toml` (gitignored); copy
[`mise.local.toml.example`](./mise.local.toml.example). `just
catalog-check` reads system keys as `<SYSTEM>_<OPTION>`, such as
`LISBON_METRO_CLIENT_ID`, and skips systems without them.

See [`CLAUDE.md`](./CLAUDE.md) for conventions and
[`CONTRIBUTING.md`](./CONTRIBUTING.md) for adding a city or provider.

## License

Code is licensed under the [Apache License 2.0](./LICENSE). Transit data
belongs to its publishers and keeps their licenses; see
[`NOTICE`](./NOTICE) for data included in this repository.
