Feeds recorded on 2026-11-23. Tests serve them from a local server: the
CSV files zipped as the static feed, `realtime.pb` as the TripUpdates
feed. `Query.Now` in tests is each feed's header timestamp.

`mta/`: New York City subway (MTA New York City Transit)
- `realtime.pb`: `https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs`
  (1, 2, 3, 4, 5, 6, 7 and S lines), header timestamp 13:41:09 UTC.
  Realtime trip IDs do not match the schedule's.
- `routes.txt`, `stops.txt`: from
  `http://web.mta.info/developers/data/nyct/subway/google_transit.zip`,
  unchanged. `trips.txt`: its first 39 trips only.

`bart/`: Bay Area Rapid Transit
- `realtime.pb`: `http://api.bart.gov/gtfsrt/tripupdate.aspx`, header
  timestamp 13:41:12 UTC. Updates carry no route_id, and nine trips are
  not in the schedule.
- `routes.txt`, `stops.txt`: from
  `https://www.bart.gov/dev/schedules/google_transit.zip`, unchanged.
  `trips.txt`: trimmed to the trips in `realtime.pb`.

Data belongs to the agencies and is used under their developer terms; see
NOTICE.
