# Urbanist Timetable

> Your next departure, wherever you look.

Live public transit departures in your terminal, or on an
[AWTRIX NG](https://github.com/Blueforcer/awtrix-ng) pixel clock (Ulanzi
TC002).

A companion to [*The Urbanist Lexicon*](https://mjrossi.com/blog) and
[*Urbanist Atlas*](https://urbanistatlas.com).

```
$ urbanist-timetable departures sf-bart EMBR --limit 3
Embarcadero                                       22:44
Green-S  OAK Airport / SF / Daly City           2 22 41
Blue-N   SF / OAK Airport / Dublin/Pleasanton   3 22 42
Red-S    SF / SFO Airport / Millbrae            5 24 44
```

On the clock, each board shows one line and direction, like a platform sign: the
destination on top, then the line color (or bus number) and minutes until
the next departures. Live predictions are bright with an amber dot;
schedule-only times are dimmed. Add a board per direction and the clock
rotates through them. A `rows` layout fits two lines or directions on one
screen instead, and a `grid` layout the next departure of up to four lines.

> **Status:** early, and running on a real TC002. Supported today:
> any agency publishing GTFS-Realtime, with six
> [ready-made systems](#systems) checked against their live feeds, plus
> dedicated providers for Metro de Lisboa and Carris Metropolitana, and
> [Transitland](https://www.transit.land/) for thousands more. Only
> the 52 × 16 TC002 is supported on the clock. What comes next is in
> [`docs/PLAN.md`](./docs/PLAN.md).
>
> **Renamed:** this project was `awtrix-ng-transit-tracker` until v0.2.0.
> The image is now `ghcr.io/mjrossi/urbanist-timetable`, the binary
> `urbanist-timetable`, and the environment variables `TIMETABLE_*`
> (formerly `TRANSIT_*`). Images published before the rename are at
> `ghcr.io/mjrossi/awtrix-ng-transit-tracker`.

## In your terminal

With Go installed:

```bash
go install github.com/mjrossi/urbanist-timetable/cmd/urbanist-timetable@latest

urbanist-timetable systems                          # ready-made systems
urbanist-timetable stops search nyc-subway times sq # find a stop ID
urbanist-timetable departures nyc-subway 127N       # its next departures
urbanist-timetable departures nyc-subway 127N --lines 1,2,3 --watch
```

Systems that need no key work without a configuration file. For one that
does, set its variables, which `systems show ID` names (such as
`LISBON_METRO_CLIENT_ID`), or add a block to
`~/.config/urbanist-timetable/config.toml` as described under
[Providers](#providers). Any agency with GTFS-Realtime works from its feed
URLs:

```bash
urbanist-timetable departures --static https://example.org/gtfs.zip \
  --realtime https://example.org/tripupdates.pb STOP_ID
```

Schedules are downloaded once a day into your cache directory (`--cache-dir`;
`off` turns it off), so later commands are quick. `--json` prints JSON for
scripts; its shape may change before 1.0. Times marked `*` are from the
schedule, without a live prediction.

## On a pixel clock

You need a TC002 with AWTRIX NG 1.2.2 or later on your network, and Docker
on a machine that stays on, such as a NAS. Images for `linux/amd64` and
`linux/arm64` are published to `ghcr.io/mjrossi/urbanist-timetable`:

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
urbanist-timetable clock preview         # fetches once and prints what each board would show
urbanist-timetable clock preview --push  # ...and pushes it to the clock once
```

`clock preview` marks names too wide for their space on the display; give them shorter names
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

`/data` holds the cached schedules and a small file naming the configured
boards, so that a board you remove from the configuration also leaves the clock's app
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
urbanist-timetable departures SOURCE STOP...    # next departures; see --help for filters
urbanist-timetable stops search SOURCE WORDS    # find stop IDs
urbanist-timetable systems [show ID]            # ready-made systems
urbanist-timetable config validate              # check the configuration
urbanist-timetable clock run                    # poll and push until stopped (the image's default)
urbanist-timetable clock preview [--board NAME] # fetch once, print rows and payload
urbanist-timetable clock preview --push         # ...and push it to the clock once
urbanist-timetable clock preview --json         # print only the payload
```

SOURCE is a `[providers.NAME]` block in the configuration, a system ID, or
a provider that needs no settings.

Global flags: `--config` (`TIMETABLE_CONFIG`, default
`~/.config/urbanist-timetable/config.toml`, or under `$XDG_CONFIG_HOME`; in the
image, `/config/config.toml`), `--cache-dir` (`TIMETABLE_CACHE`, default
your user cache directory; in the image, `/data/cache`), `--log-format json|text`
(`TIMETABLE_LOG_FORMAT`, default `json`), `--log-level
debug|info|warn|error` (`TIMETABLE_LOG_LEVEL`), and `--version`, which prints
the release version.

## Configuration

See [`config.example.toml`](./config.example.toml). In short: an
optional `timezone` and `quiet_hours`, one `[clock]`, one
`[providers.NAME]` block per data source, and one `[[boards]]` entry per
app on the clock, each with one or more stops. A board's
`[boards.clock]` table holds how it looks on the clock: layout, labels,
lifetime and so on. Without `timezone`, the
`TZ` environment variable applies, which in the image is UTC unless you
set it. A board's own `timezone` overrides the top-level one for that
board's `active_hours`, for boards from another city; `"Local"` there
means the `TZ` zone.

### When boards are shown

Two settings take a daily window such as `"06:30-23:30"`, which may run past
midnight:

- A board's `active_hours` are the hours you want to see it, such as your
  commute. They are not the agency's service hours.
- The top-level `quiet_hours` are a night mode for the whole service. They
  hide every board, whatever its `active_hours`, and always follow the
  top-level `timezone`.

A hidden board is not polled, and the clock skips it. In quiet hours the
service uses no network at all, except to hide the boards at the start: each
within one `poll_interval`, retried if the clock does not answer, until the
app's `lifetime` has passed. `clock preview` and `stops search` still work. The
display stays on and shows the clock's other apps.

A hidden board returns to the same place if you have arranged the rotation in
the clock's web UI, which lists it as "no data" meanwhile. Boards you have
not arranged return at the end of the rotation.

Stopping the service works the same way: its boards leave the display when
their `lifetime` ends, and the web UI lists them as "no data" until it runs
again.

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
| `transitland` | Thousands of agencies through [Transitland](https://www.transit.land/), schedule and realtime merged | Free API key; requests count against a monthly quota | The agency's; Transitland's terms apply |
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

### Transitland

Prefer an agency's own feeds where you can: they have no quota. Where that
is awkward (no realtime feed, a feed behind a key, or a schedule zip too
large for your NAS), `transitland` serves the agency from one API, with
timetabled trips shown even when realtime has none. Data via Transitland.

#### What you need

An API key from [Transitland](https://www.transit.land/), and the monthly
request quota of your plan in `monthly_quota`:

| Plan | Requests a month | `monthly_quota` |
|---|---|---|
| Free | 10,000 | leave it out; this is the default |
| Hobbyist/Academic (free for non-commercial use) | 50,000 | `50000` |
| Other | see your plan | that number |

Plans change, so go by the quota [your plan](https://www.transit.land/plans-pricing/)
states. The setting only drives the warnings described under
[Staying within the quota](#staying-within-the-quota); Transitland enforces
the quota itself.

#### Is my agency covered?

Agencies do not sign up: Transitland collects the feeds they publish. These
have been run through this provider:

| Agency | Feed | Times | Notes |
|---|---|---|---|
| Carris, Lisbon | `f-carris~pt` | Schedule only | No destinations, so use the `grid` layout |
| Comboios de Portugal | `f-eyc-cp` | Schedule only | |
| MBTA, Boston | `f-drt-mbta` | Live and schedule | Trips added in realtime lack a destination |

For another agency, find its feed's Onestop ID on its Transitland page, then
look up the stop:

```bash
urbanist-timetable stops search carris rossio
```

Here `carris` is the name of your provider block. Some agency feeds answer
nothing because Transitland serves the agency from a regional feed instead:
BART's `f-9q9-bart` does, in favor of `f-sf~bay~area~rg`. Search without
`feed` to see which feed has a stop.

#### Example

Two Carris stops at Rossio, in central Lisbon, one board each:

```toml
[providers.carris]
type = "transitland"
api_key = "${TRANSITLAND_API_KEY}"
feed = "f-carris~pt"       # the feed's Onestop ID; stops are then GTFS stop IDs
refresh_interval = "5m"    # 1m to 10m; default 5m
monthly_quota = 50000      # your plan's quota; default 10000 (free plan)

[[boards]]
name = "rossio-901"

[boards.clock]
layout = "grid"

[[boards.stops]]
provider = "carris"
stop = "901"

[[boards]]
name = "rossio-919"

[boards.clock]
layout = "grid"

[[boards.stops]]
provider = "carris"
stop = "919"
```

Without `feed`, stops are full keys such as `f-carris~pt:901`, and one block
serves several agencies. `lines` matches short names or Transitland route
Onestop IDs, not GTFS route IDs.

#### Live and scheduled times

Times are live only where Transitland has the agency's realtime feed. Others
are schedule-only, shown dimmed without the live dot: they do not know about
delays or cancellations.

- Between fetches predictions do not change: a vehicle leaves the board at its
  last predicted time and, if it was delayed further, returns at the next
  fetch, so a shorter `refresh_interval` follows delays more closely.
- Vehicles up to 30 minutes late are shown, except that until a stop's first
  answer with a timetabled departure, at startup or after 12 hours without
  one, those already past their scheduled time are missed.
- Destinations are blank where the agency publishes none, as with Carris, and
  on some trips added in realtime, as with the MBTA's. Those rows are listed
  after the others, so the `departure` layout shows one only when no named
  destination is left, and a `headsigns` filter drops them. The `grid` layout
  needs no destinations.

#### Staying within the quota

Each stop is fetched once per `refresh_interval`, and boards still count
minutes down between fetches. Boards on one stop share its requests.
Requests a month are about

    31 × stops × active hours a day × 60 ÷ refresh minutes

so one stop shown from 06:30 to 23:30 takes about:

| `refresh_interval` | Requests a month per stop |
|---|---|
| `"2m"` | 15,800 |
| `"5m"` (default) | 6,300 |
| `"8m"` | 4,000 |

For schedule-only agencies a long interval costs little.
`config validate` prints the estimate for your boards, taking `active_hours`
and `quiet_hours` into account, and warns when it exceeds `monthly_quota`;
`clock run` logs it at startup. Blocks with the same key share its quota, so they
are added up against the smallest `monthly_quota` any of them sets. Stop
searches, previews and retries count too, but are not in the estimate.

## How it behaves

- Each push sets a lifetime (three poll intervals by default), so the app
  disappears from the clock if the service stops instead of showing stale
  times.
- If a provider fails, the last good data is reused for up to `max_stale`
  (10 minutes by default) while the provider is retried with backoff.
  Minutes are recomputed on every push.
- Outside `active_hours` and in `quiet_hours` a board is hidden and no
  requests are made; see [When boards are shown](#when-boards-are-shown).
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

## Use it from Go

The packages under `pkg/` are importable and need Go 1.26. They use only
the standard library, except `catalog`, which reads TOML, and `providers`,
which uses `catalog`.

| Package | For |
|---|---|
| `pkg/transit` | The departure model, the `Provider` interface, `Filter` and `Group` |
| `pkg/transit/providers` | `Open`: a provider from a system or type name |
| `pkg/transit/providers/gtfsrt` | Any agency with GTFS and GTFS-Realtime, from its URLs |
| `pkg/transit/catalog` | The ready-made systems |
| `pkg/transit/transittest` | The contract suite for a provider of your own |
| `pkg/awtrix` | A typed client for AWTRIX NG clocks |

```go
p, err := providers.Open("bart", map[string]any{"system": "sf-bart"})
if err != nil {
	return err
}
now := time.Now()
deps, err := p.Departures(ctx, transit.Query{StopID: "EMBR", Now: now, Window: time.Hour})
if err != nil {
	return err
}
for _, row := range transit.Group(deps, 0, 3) {
	fmt.Println(row.Line.ShortName, row.Headsign, row.Departures[0].MinutesUntil(now))
}
```

[`examples/departures`](./examples/departures) is a complete program. Before
1.0, a minor version may break the API.

## Development

```bash
mise install     # Go, just, golangci-lint
just             # list commands
just ci          # lint, test, validate the example config
```

| Command | Does |
|---|---|
| `just run [config]` / `just preview [config]` | `clock run` or `clock preview` against `config.toml` with text logs |
| `just build` | Builds `bin/urbanist-timetable` |
| `just test` / `just lint` / `just fmt` | Tests with the race detector; lint; format |
| `just golden` | Rewrites the renderer's golden files after an intended layout change |
| `just catalog-check` | Checks every catalog system against its live feeds |
| `just example` | Builds the example importer, a module of its own |
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
