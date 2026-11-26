# Transitland

The `transitland` provider serves thousands of agencies through
[Transitland](https://www.transit.land/)'s API, schedule and realtime
merged. Data via Transitland.

Prefer an agency's own feeds where you can: they have no quota. Where that
is awkward (a feed behind a key, realtime that Transitland merges from a
regional feed, or a schedule zip too large for your NAS), `transitland`
serves the agency from one API. `systems show` picks for you: it writes a
`gtfsrt` block when Transitland lists keyless feed URLs, so departures
spend no quota, and a `transitland` block otherwise. Looking up a
Transitland ID takes two or more requests, saved for a week in the cache
directory.

In `agencies search` (with `TRANSITLAND_API_KEY` set), `realtime?` marks a
Transitland result whose agency publishes predictions, which Transitland
does not always pass on; `systems show` says which way a system is read,
and `departures` shows which times are live.

## What you need

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

## Is my agency covered?

Agencies do not sign up: Transitland collects the feeds they publish. These
have been run through this provider:

| Agency | Feed | Times | Notes |
|---|---|---|---|
| Carris, Lisbon | `f-carris~pt` | Schedule only | No destinations, so use the `grid` layout |
| Comboios de Portugal | `f-eyc-cp` | Schedule only | |
| MBTA, Boston | `f-drt-mbta` | Live and schedule | Trips added in realtime lack a destination |

For another agency, find its feed's Onestop ID with `agencies search` (with
`TRANSITLAND_API_KEY` set) or on its Transitland page, then look up the
stop:

```bash
urbanist-timetable stops search carris rossio
```

Here `carris` is the name of your provider block. Some agency feeds answer
nothing because Transitland serves the agency from a regional feed instead:
BART's `f-9q9-bart` does, in favor of `f-sf~bay~area~rg`. Search without
`feed` to see which feed has a stop.

## Example

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

## Live and scheduled times

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

## Staying within the quota

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
