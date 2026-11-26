# Providers

A provider reads departures from one kind of source. Each
`[providers.NAME]` block in `config.toml` configures one; boards and
commands refer to it by NAME. NAME is also the provider type unless the
block sets `type`, so one type can serve several agencies:

```toml
[providers.bart]            # a verified system: its settings come from the catalog
system = "bart"

[providers.myagency]        # the generic provider, by URL
type = "gtfsrt"
static_url = "https://example.org/gtfs.zip"

[providers.carrismetropolitana]   # NAME is the type
```

Most agencies need only `gtfsrt`. The others exist where it cannot serve
an agency, or where an API is clearly better. Transitland has a page of its
own: [transitland.md](./transitland.md).

| Provider | For |
|---|---|
| [`gtfsrt`](#gtfsrt) | Any agency with a GTFS schedule, GTFS-Realtime TripUpdates, or both |
| [`transitland`](./transitland.md) | Thousands of agencies through Transitland's API, with a monthly quota |
| [`metrolisboa`](#metrolisboa) | Metro de Lisboa, from its own API |
| [`carrismetropolitana`](#carrismetropolitana) | Carris Metropolitana buses, Lisbon metropolitan area |
| [`demo`](#demo) | Generated departures, for trying the display |

## gtfsrt

Almost every agency publishes the same two open formats: a GTFS schedule,
a zip of the timetable, stops, lines and their colors; and often
GTFS-Realtime TripUpdates, a feed of predictions. `gtfsrt` reads both, so
an agency needs only a configuration block.

### Getting a block

Let the tool write it:

```bash
urbanist-timetable agencies search <agency or city>
urbanist-timetable systems show <id>     # prints the block, with ${NAME} for any key
```

The IDs from the search, such as `mdb-2929`, also work directly as a
source, without a block. To try feeds you found yourself:

```bash
urbanist-timetable departures --static https://example.org/gtfs.zip \
  --realtime https://example.org/tripupdates.pb STOP_ID
```

### Settings

| Setting | Meaning |
|---|---|
| `static_url` | The GTFS schedule zip. Gives line names and colors, stop names, destinations and the timetable. Needed for `stops search`. |
| `realtime_urls` | GTFS-Realtime TripUpdates feeds. Agencies that split their network, like the New York subway, list several. |
| `timetable` | `false` shows only trips a feed predicts. Default `true`. |
| `headers` | Sent with every request, for feeds that need a key: `headers = { "X-Api-Key" = "${MY_KEY}" }`. |

Either URL may be left out:

- **Schedule and realtime:** timetabled trips are shown, with the
  prediction where a feed has one, so a board is not empty while no
  vehicle is near. Predicted times are bright on the clock; scheduled
  ones are dimmed, and marked `*` in the terminal.
- **Schedule only**, for an agency that publishes no TripUpdates: every
  time is from the timetable, so delays and cancellations do not show.
- **Realtime only:** lines show as bare route IDs and destinations as stop
  IDs, and `stops search` cannot work.

### Stops

Stop IDs are the schedule's `stop_id`s; `stops search` finds them by name.
A station ID covers all its platforms, and a platform ID, such as the New
York subway's `127N`, narrows to one direction. Where several stops share
a name, `stops search` lists the lines leaving each and where they go.

### When realtime and schedule disagree

Realtime trips are matched to the timetable by trip ID, or by route and
start time for feeds that name them that way, such as HSL's. A delay sent
without a time, or carried forward from an earlier stop, applies. Trips
without a destination are named after their last stop.

Some agencies' realtime trips do not match their schedule's at all. Then,
on lines with realtime trips, only those are shown, so a train is not
listed twice, and a warning is logged. If that warning keeps coming back,
set `timetable = false`, as the `mta-subway` system does.

### Downloads and memory

- The schedule is downloaded once and kept in the cache directory
  (`--cache-dir`). After a day it is checked with the server, which costs
  little where the server supports it, and a failed check keeps the old
  copy. `cache clear` deletes it.
- The first query for a stop reads its times from the schedule (about 5
  seconds for HSL), and the result is saved, so later runs are quick.
- Realtime feeds are fetched at most every 15 seconds; boards on the same
  block share each fetch.
- Schedules up to 512 MB are read. `systems show` and `config init` warn
  about ones over 150 MB, which a small machine such as a NAS may
  struggle with: routes, stops, trips and the calendar are held in memory.
- A key in `headers` goes only to the host it was first sent to; a
  redirect elsewhere drops it.

## metrolisboa

Metro de Lisboa publishes train wait times only through its own API, not
as GTFS-Realtime. The verified system `metro-lisboa` uses this provider.

1. Register at the [API Store](https://api.metrolisboa.pt/store/).
2. Subscribe an application to EstadoServicoML.
3. Generate production keys with the client-credentials grant.
4. Put them in the environment, never in the file:

```bash
export METRO_LISBOA_CLIENT_ID=...
export METRO_LISBOA_CLIENT_SECRET=...
```

With those set, `metro-lisboa` works without a configuration file. In one:

```toml
[providers.metro]
system = "metro-lisboa"
client_id = "${METRO_LISBOA_CLIENT_ID}"
client_secret = "${METRO_LISBOA_CLIENT_SECRET}"
```

Station IDs are two letters, such as `BC` for Baixa-Chiado;
`stops search metro-lisboa chiado` finds them. Every time is a live
prediction: the API has no timetable. Lines are Azul, Amarela, Verde and
Vermelha. Data CC BY 4.0, © Metropolitano de Lisboa.

## carrismetropolitana

Regional buses in 15 of the 18 municipalities of the Lisbon metropolitan
area, from the operator's open API. No key is needed:

```toml
[providers.carrismetropolitana]
```

or `system = "carris-metropolitana"`. Stop IDs are six digits, such as
`060001`, and each side of a road usually has its own; `stops search
carris-metropolitana <words>` finds them. Times are scheduled, with the
prediction where the bus reports one. Data CC BY 4.0, © Carris
Metropolitana.

Lisbon's city buses and trams (Carris) publish no predictions this tool
can read; their schedule works through `gtfsrt` or `transitland`.

## demo

Generated departures from fixed headways, for seeing the display work
before configuring a real source. `urbanist-timetable config example`
shows it configured:

```toml
[providers.demo]
[[providers.demo.lines]]
id = "azul"
short_name = "Azul"
color = "#0072CE"
mode = "metro"
headsign = "Santa Apolónia"
headway = "6m"      # one departure every 6 minutes
offset = "1m"
realtime = true     # every other departure shown as a live prediction, a minute late
```

Any stop ID works unless a line lists `stops`.
