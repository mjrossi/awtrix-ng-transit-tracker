# Urbanist Timetable

> Your next departure, wherever you look.

Live public transit departures in your terminal, or on an
[AWTRIX NG](https://github.com/Blueforcer/awtrix-ng) pixel clock (Ulanzi
TC002).

A companion to [*The Urbanist Lexicon*](https://mjrossi.com/blog) and
[*Urbanist Atlas*](https://urbanistatlas.com).

```
$ urbanist-timetable departures bart EMBR --limit 3
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

> **Status:** early, and running on a real TC002. Any agency that
> publishes a GTFS schedule works, with live predictions where it also
> publishes GTFS-Realtime; `agencies search` finds it by name or city.
> Only the 52 × 16 TC002 is supported on the clock. What comes next is in
> [`docs/PLAN.md`](./docs/PLAN.md).

## In your terminal

Install a release binary for Linux, macOS or Windows, one of:

```bash
mise use -g github:mjrossi/urbanist-timetable    # with mise
go install github.com/mjrossi/urbanist-timetable/cmd/urbanist-timetable@latest
```

or download an archive from
[Releases](https://github.com/mjrossi/urbanist-timetable/releases). Each
archive has build provenance:
`gh attestation verify ARCHIVE -R mjrossi/urbanist-timetable`. Then:

```bash
urbanist-timetable agencies search mta              # find your agency's system
urbanist-timetable stops search mta-subway times sq # find a stop ID in it
urbanist-timetable departures mta-subway 127N       # its next departures
urbanist-timetable departures mta-subway 127N --lines 1,2,3 --watch
```

Systems that need no key work without a configuration file. For one that
does, set its variables, which `systems show ID` names (such as
`METRO_LISBOA_CLIENT_ID`), or add a block to
`~/.config/urbanist-timetable/config.toml` as described under
[Providers](#providers). Every result's ID works this way, verified or
not (`departures mdb-2929 3801`). Feed URLs work directly too:

```bash
urbanist-timetable departures --static https://example.org/gtfs.zip \
  --realtime https://example.org/tripupdates.pb STOP_ID
```

Leave out `--realtime` for an agency that publishes only a schedule.

To see the same stops without typing them each time, save them as boards.
`urbanist-timetable config init` asks for an agency or city, lets you pick
a system from the same search as `agencies search`, searches for your stop
and writes `~/.config/urbanist-timetable/config.toml`; after that,
`departures` with no arguments shows every board, and `departures NAME` one
of them. `config init --system mta-subway --stop 127N` does the same
without questions, and `--system` takes a search result's ID too. A board
in the file looks like this:

```toml
[providers.nyc]
system = "mta-subway"

[[boards]]
name = "uptown"

[[boards.stops]]
provider = "nyc"
stop = "127N"
lines = ["1", "2", "3"]
```

The clock shows the same boards, but a terminal needs no `[clock]`.

Schedules are downloaded once a day into your cache directory (`--cache-dir`;
`off` turns it off), so later commands are quick. They stay there until
`urbanist-timetable cache clear` deletes them. `--json` prints JSON for
scripts; its shape may change before 1.0. Times marked `*` are from the
schedule, without a live prediction.

Tab completion of commands, systems and sources is available for bash, zsh
and fish; `urbanist-timetable completion --help` shows how to turn it on.
`urbanist-timetable config example` prints a configuration that explains
every setting.

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
docker run --rm ghcr.io/mjrossi/urbanist-timetable:latest config example > config.toml
```

(or `cp config.example.toml config.toml` in a clone of the repository).

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

Search for your system by agency or city:

```bash
urbanist-timetable agencies search lisbon
urbanist-timetable systems show mdb-2929      # prints a block to paste into config.toml
```

The search covers the [verified systems](#systems), the
[Mobility Database](https://mobilitydatabase.org/)'s list of feeds, and,
with `TRANSITLAND_API_KEY` set, [Transitland](https://www.transit.land/)'s
operators. The agency shown is whoever published the data, so if a name
finds nothing, try the city; countrywide feeds, such as national
railways, are found by the country's name. `systems show` says when a
feed needs a key, and how it is read; see
[`docs/transitland.md`](./docs/transitland.md) for Transitland's quota.

### 3. Find your stop

```bash
urbanist-timetable stops search mta-subway times sq
```

The first argument is a system ID or a `[providers.NAME]` block in
`config.toml`. Where several stops share a name, such as one per
direction or platform, each is listed with the lines that leave from it
and where they go, such as `7X to 34 St-Hudson Yards or Flushing-Main St`.

### 4. Add a board

```toml
[providers.nyc]
system = "mta-subway"

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
Delete the demo boards and providers you do not use. With the binary
installed, `urbanist-timetable config init --stdout` writes the provider
and board for you, with a `[clock]` block to fill in.

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

Mount a volume at `/data`, as above: it keeps the downloaded schedules and
the list of boards, so a board you remove also leaves the clock after a
restart. The configuration is read at startup, so restart the container
after editing it. To update, run `docker compose pull && docker compose up -d`.
The container only makes outgoing requests, to transit APIs and to the
clock, so it needs no published ports.

How the running service behaves (active and quiet hours, failures, removed
boards, the data volume) is in [`docs/clock.md`](./docs/clock.md).

### Secrets

`${NAME}` in the `[clock]` and `[providers.*]` settings is read from the
environment, so keys and passwords can stay out of `config.toml`. Pass
them with `-e NAME=...` or Compose's `environment:`. A variable that is
referenced but not set is a configuration error.

## Commands

```bash
urbanist-timetable departures SOURCE STOP...    # next departures; see --help for filters
urbanist-timetable stops search SOURCE WORDS    # find stop IDs
urbanist-timetable agencies search WORDS        # find an agency's system by name or city
urbanist-timetable systems show ID              # print a system's block for config.toml
urbanist-timetable systems                      # list the verified systems
urbanist-timetable config init                  # write a configuration with one board
urbanist-timetable config validate              # check the configuration
urbanist-timetable config example               # print an example that explains every setting
urbanist-timetable cache clear                  # delete cached GTFS schedules and the feed list
urbanist-timetable clock run                    # poll and push until stopped (the image's default)
urbanist-timetable clock preview [--board NAME] # fetch once, print rows and payload
urbanist-timetable clock preview --push         # ...and push it to the clock once
urbanist-timetable clock preview --json         # print only the payload
urbanist-timetable completion SHELL             # tab completion for bash, zsh or fish
```

SOURCE is a system ID, from `agencies search`, or a `[providers.NAME]`
block in the configuration.

Global flags: `--config` (`TIMETABLE_CONFIG`, default
`~/.config/urbanist-timetable/config.toml`, or under `$XDG_CONFIG_HOME`; in the
image, `/config/config.toml`), `--cache-dir` (`TIMETABLE_CACHE`, default
your user cache directory; in the image, `/data/cache`), `--log-format json|text`
(`TIMETABLE_LOG_FORMAT`, default `text` at a terminal and `json` otherwise),
`--log-level debug|info|warn|error` (`TIMETABLE_LOG_LEVEL`), and `--version`,
which prints the release version. Logs go to stderr. GTFS schedule downloads,
which can take a while on a first run, are logged at `info`; at `debug`,
`departures` also logs how long each stop took.

## Configuration

See [`config.example.toml`](./config.example.toml). In short: an
optional `timezone` and `quiet_hours`, one `[clock]`, one
`[providers.NAME]` block per data source, and one `[[boards]]` entry per
app on the clock, each with one or more stops and optional
`active_hours`. A board's `[boards.clock]` table holds how it looks on
the clock: layout, labels, lifetime and so on. When boards are shown is
explained in [`docs/clock.md`](./docs/clock.md#when-boards-are-shown).

## Systems

An agency publishes the data; a system is one schedule with its realtime
feeds, and an agency may run several (MTA runs the subway, LIRR and more);
a stop belongs to one system. `agencies search` finds an agency's systems
by its name or city. These are verified: checked against their live feeds, with short
IDs and any settings their feeds need:

| System | Agency | City | Modes |
|---|---|---|---|
| `metro-lisboa` | Metro de Lisboa | Lisbon | metro (needs a free API key) |
| `carris-metropolitana` | Carris Metropolitana | Lisbon | bus |
| `mta-subway` | MTA | New York | metro |
| `bart` | BART | San Francisco Bay Area | metro |
| `mbta` | MBTA | Boston | metro, tram, bus, rail, ferry |
| `hsl` | HSL | Helsinki | metro, tram, bus, rail, ferry |

In `config.toml`:

```toml
[providers.nyc]
system = "mta-subway"
```

Other settings in the block override the system's.

Verified IDs name the agency, and the system when the agency runs more
than one: `hsl`, or `mta-subway`. Other systems have the IDs of the
directory they come from: Mobility Database IDs such as `mdb-2929` or
`tld-716`, and Transitland feed IDs such as `f-eyc-cp`. Where a directory
lists a verified system, search shows the verified one.

## Providers

| Provider | Covers | Key | Data license |
|---|---|---|---|
| `carrismetropolitana` | Carris Metropolitana buses, Lisbon metropolitan area | None | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/), © Carris Metropolitana |
| `metrolisboa` | Metro de Lisboa, all four lines | Free OAuth client from the [API Store](https://api.metrolisboa.pt/store/) | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/), © Metropolitano de Lisboa |
| `gtfsrt` | Any agency with a GTFS schedule, GTFS-Realtime TripUpdates, or both, configured by URL (several per config with `type = "gtfsrt"`) | Depends on the agency | The agency's |
| `transitland` | Thousands of agencies through [Transitland](https://www.transit.land/), schedule and realtime merged | Free API key; requests count against a monthly quota | The agency's; Transitland's terms apply |
| `demo` | Generated departures for testing the display | None | — |

`agencies search` and `systems show ID` write this block for you from the
[Mobility Database](https://mobilitydatabase.org/) or Transitland. By
hand:

```toml
[providers.myagency]
type = "gtfsrt"
static_url = "https://example.org/gtfs.zip"
realtime_urls = ["https://example.org/tripupdates.pb"]
```

Either URL may be left out, for an agency that publishes only a schedule
or only realtime. [`docs/providers.md`](./docs/providers.md) explains each
provider's settings: what `gtfsrt` does with each feed, setting up Metro
de Lisboa's keys, and the demo. `transitland` needs a free API key, and
every request counts against a monthly quota: see
[`docs/transitland.md`](./docs/transitland.md).

## Use it from Go

The packages under `pkg/` are importable and need Go 1.26. They use only
the standard library, except `catalog`, which reads TOML, and `providers`,
which uses `catalog`.

| Package | For |
|---|---|
| `pkg/transit` | The departure model, the `Provider` interface, `Filter` and `Group` |
| `pkg/transit/providers` | `Open`: a provider from a system or type name |
| `pkg/transit/providers/gtfsrt` | Any agency with a GTFS schedule, GTFS-Realtime, or both, from its URLs |
| `pkg/transit/catalog` | The verified systems |
| `pkg/transit/transittest` | The contract suite for a provider of your own |
| `pkg/awtrix` | A typed client for AWTRIX NG clocks |

```go
p, err := providers.Open("bart", map[string]any{"system": "bart"})
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
mise install     # Go, just, golangci-lint, GoReleaser
just             # list commands
just ci          # lint, test, validate the example config
```

| Command | Does |
|---|---|
| `just run [config]` / `just preview [config]` | `clock run` or `clock preview` against `config.toml` with text logs |
| `just build` | Builds `bin/urbanist-timetable` |
| `just test` / `just lint` / `just fmt` | Tests with the race detector; lint; format |
| `just golden` | Rewrites the renderer's golden files after an intended layout change |
| `just catalog-check` | Checks every verified system against its live feeds |
| `just example` | Builds the example importer, a module of its own |
| `just release-check` | Validates the GoReleaser config and builds a snapshot |
| `just docker-build` / `just docker-run [config]` | Builds the image; runs it against `config.toml` |

Secrets for local runs go in `mise.local.toml` (gitignored); copy
[`mise.local.toml.example`](./mise.local.toml.example). `just
catalog-check` reads system keys as `<SYSTEM>_<OPTION>`, such as
`METRO_LISBOA_CLIENT_ID`, and skips systems without them.

See [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) for how the code fits
together, [`CLAUDE.md`](./CLAUDE.md) for conventions,
[`CONTRIBUTING.md`](./CONTRIBUTING.md) for adding a city or provider, and
[`SECURITY.md`](./SECURITY.md) for the security model and reporting.

## License

Code is licensed under the [Apache License 2.0](./LICENSE). Transit data
belongs to its publishers and keeps their licenses; see
[`NOTICE`](./NOTICE) for data included in this repository.
