# The clock

Urbanist Timetable shows departures on an
[AWTRIX NG](https://github.com/Blueforcer/awtrix-ng) clock: a Ulanzi TC002,
52 × 16 pixels. Each board in the configuration becomes one app on the
clock, and the clock rotates through its apps.

- [Setup](#setup)
- [Layouts](#layouts)
- [When boards are shown](#when-boards-are-shown)
- [How it behaves](#how-it-behaves)
- [The data volume](#the-data-volume)

## Setup

You need a TC002 with AWTRIX NG 1.2.2 or later on your network, and Docker
on a machine that stays on, such as a NAS. Images for `linux/amd64` and
`linux/arm64` are published to `ghcr.io/mjrossi/urbanist-timetable`:

| Tag | Contents |
|---|---|
| `latest`, `1`, `1.2`, `1.2.3` | Releases |
| `main` | The latest commit on `main`, for fixes not yet released |

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

The search covers the verified systems (`urbanist-timetable systems`),
the [Mobility Database](https://mobilitydatabase.org/)'s list of feeds,
and, with `TRANSITLAND_API_KEY` set,
[Transitland](https://www.transit.land/)'s operators. The agency shown is
whoever published the data, so if a name finds nothing, try the city;
countrywide feeds, such as national railways, are found by the country's
name. `systems show` says when a feed needs a key; see
[providers.md](./providers.md) for each provider's settings.

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

Pick a [layout](#layouts) for the board, and delete the demo boards and
providers you do not use. [`config.example.toml`](../config.example.toml)
documents every setting. With the binary installed,
`urbanist-timetable config init --stdout` writes the provider and board
for you, with a `[clock]` block to fill in.

### 5. Check it

```bash
urbanist-timetable config validate       # checks the file and builds its providers and clock client
urbanist-timetable clock preview         # fetches once and prints what each board would show
urbanist-timetable clock preview --push  # ...and pushes it to the clock once
```

`clock preview` marks names too wide for their space on the display; give
them shorter names under `[boards.clock.labels]`.

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

Mount a volume at `/data`, as above; [the data volume](#the-data-volume)
says why. The configuration is read at startup, so restart the container
after editing it. To update, run `docker compose pull && docker compose up -d`.
The container only makes outgoing requests, to transit APIs and to the
clock, so it needs no published ports. To build the image yourself, clone
the repository and run `docker build -t urbanist-timetable .`.

`${NAME}` in the `[clock]` and `[providers.*]` settings is read from the
environment, so keys and passwords can stay out of `config.toml`. Pass
them with `-e NAME=...` or Compose's `environment:`. A variable that is
referenced but not set is a configuration error.

## Layouts

A board's `layout`, in its `[boards.clock]` table, decides what fits on
the 52 × 16 display:

| Layout | Shows | Suits |
|---|---|---|
| `departure` (default) | One line and direction, its destination across the full width, and the next few times | A metro or rail platform; one board per direction |
| `rows` | Two lines or directions, one per row, each with its destination and times | A stop where seeing two routes at once matters |
| `grid` | Up to four lines, each with its next time, no destination | A busy bus stop where every bus goes the same way |

### departure

Like a platform sign: the destination on top, then the line's color (or
bus number) and minutes until the next departures.

```
Sta Apolónia
────────────
▌ 3 · 8 · 14
```

About 13 characters of destination fit, so most names need no label. The
board shows the first line and direction, in name order, that has
departures, so pin it to one with the stop's `lines` and `headsigns` (or
`directions`), and add a board per direction you want:

```toml
[[boards]]
name = "baixa-azul"

[[boards.stops]]
provider = "metro"
stop = "BC"
lines = ["Azul"]
headsigns = ["Santa Apolónia"]
```

A departure under a minute away shows as `Due`.

### rows

```
▌Sta Apolónia      4
▌Telheiras     1 · 6
```

The soonest two lines and directions are shown, in a fixed order so rows
do not swap places between pushes. A second time is shown only if the
destination still fits beside it. `max_rows = 1` shows a single row.

```toml
[boards.clock]
layout = "rows"
```

### grid

```
732   5   736   7
744  12   783   8
```

Each cell is one line and its next departure, with no destination, so all
of a line's destinations share a cell. Use it where every vehicle goes the
same way, or pin the direction with the stop's `headsigns`. It also suits
agencies that publish no destinations, such as Carris in Lisbon.

```toml
[boards.clock]
layout = "grid"
```

### Busy stops

A stop with many destinations, such as a branching Tube line, fills a
`rows` board with the two soonest destinations, often both in one
direction. Two settings help. At Russell Square, on the Piccadilly line,
this shows one row each way, named after the next train:

```toml
[[boards.stops]]
provider = "tfl"
stop = "940GZZLURSQ"

[boards.clock]
layout = "rows"
group = "direction"
```

Or keep one platform with the stop's `directions` and list its next
trains, like the sign on the platform:

```toml
[[boards.stops]]
provider = "tfl"
stop = "940GZZLURSQ"
directions = ["Westbound"]

[boards.clock]
layout = "rows"
group = "train"
```

What a provider calls each direction is in
[providers.md](./providers.md).

### Reading the display

- Live predictions are bright, with a one-pixel amber dot at their top
  right. Times from the schedule are dimmed, without the dot. While a
  source is failing, its last good times are shown dimmed too.
- Only as many times as fit are shown, set by the board's `per_row`
  (default 3); times never scroll.
- With the stop's `keep_canceled = true`, a canceled trip is struck
  through, and never pushes the next trip you can catch out of its row.
- With no departures in the board's `window`, the board shows its
  `empty_text` ("No departures").

### Names that do not fit

`clock preview` marks them. Give a destination a shorter name under
`[boards.clock.labels]`:

```toml
[boards.clock.labels]
"Santa Apolónia" = "Sta Apolónia"
```

Otherwise a name scrolls in its space, or with `overflow = "clip"` is cut
off. Names with accents above letters ("São Sebastião") are drawn in the
`matrix-light6` font, which keeps the accents legible; `name_font`
chooses another.

### Other settings

All optional, in `[boards.clock]`:

| Setting | Default | Meaning |
|---|---|---|
| `layout` | `departure` | `departure`, `rows` or `grid` |
| `max_rows` | `2` | Rows in the `rows` layout: 1 or 2 |
| `group` | `destination` | What a row stands for: `destination`, `direction`, or `train` (`rows` only); see [Busy stops](#busy-stops) |
| `overflow` | `scroll` | `scroll` or `clip` for names too wide for their space |
| `lifetime` | 3 × `poll_interval` | How long the clock keeps the app without a fresh push |
| `duration` | the clock's setting | Time on screen per rotation, such as `"10s"` |
| `font` | `small` | Font for the whole board |
| `name_font` | chosen per name | Font for destinations and `empty_text` |
| `empty_text` | `No departures` | Shown when nothing departs in the window |
| `labels` | none | Shorter display names for destinations |

The design rules behind the layouts are in [rendering.md](./rendering.md).

## When boards are shown

Two settings take a daily window such as `"06:30-23:30"`, which may run past
midnight:

- A board's `active_hours` are the hours you want to see it, such as your
  commute. They are not the agency's service hours.
- The top-level `quiet_hours` are a night mode for the whole service. They
  hide every board, whatever its `active_hours`, and always follow the
  top-level `timezone`.

Without `timezone`, the `TZ` environment variable applies, which in the
image is UTC unless you set it. A board's own `timezone` overrides the
top-level one for that board's `active_hours`, for boards from another
city; `"Local"` there means the `TZ` zone.

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

## How it behaves

- Each push sets a lifetime (three poll intervals by default), so the app
  disappears from the clock if the service stops instead of showing stale
  times.
- If a provider fails, the last good data is reused for up to `max_stale`
  (10 minutes by default) while the provider is retried with backoff.
  Minutes are recomputed on every push.
- A board removed from the configuration is removed from the clock at the
  next start, including its place in the rotation. If the clock is off, this
  is retried with backoff, from one minute up to one hour. Apps of other
  senders, scripts and built-in apps are left alone.
- Logs show each board's configuration and changes of state. Set
  `TIMETABLE_LOG_LEVEL=debug` to log every fetch and push.
- The clock forgets pushed apps when it restarts; the next poll restores
  them.

## The data volume

`/data` holds the cached schedules and a small file naming the configured
boards, so that a board you remove from the configuration also leaves the
clock's app list. Mount a volume there: without one, each new container
starts with an empty state file, and boards removed from the configuration
are not cleaned up after a restart. To use a folder instead of a named
volume, make it writable for the container's non-root user. Outside
Docker, set `--state` (or `TIMETABLE_STATE`) to a file path; without it,
removed boards stay in the clock's list as "no data".

GTFS timetables are downloaded once a day to a temporary file in `/tmp`
(or the cache directory) and only the needed parts are kept in memory;
the stop times of the configured stops are read from the file. With a
read-only root filesystem, mount a tmpfs at `/tmp`; otherwise downloads
fall back to memory, which needs about as much RAM as the zip is large,
and boards show no timetabled trips: only realtime ones, or an error for
a board without `realtime_urls`.
