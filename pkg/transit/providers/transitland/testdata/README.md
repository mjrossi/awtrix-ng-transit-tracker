Hand-built responses, not recordings: the sandbox that wrote this provider
could not reach transit.land. Their shape follows the queries the
Transitland server runs for these endpoints
(`interline-io/transitland-server`, `server/rest/stop_departure_request.gql`
and `stop_request.gql`), trimmed to the fields the provider reads. Times are
relative to 2026-11-25 18:00:00 UTC, which tests use as `Query.Now`.

- `departures_LAKE.json`: `GET /stops/f-9q9-bart:LAKE/departures`, a
  station whose departures are listed under its two platforms. It covers a
  live prediction, schedule-only (`STATIC`), canceled, added and skipped
  trips, a trip ending here (`pickup_type` 1), a departure that has
  left, and one trip listed under both platforms.
- `departures_EMPTY.json`: a known stop with no departures.
- `stops_search.json`: `GET /stops?search=...`.

Replace them with trimmed recordings when a key and network are at hand.
Route names and colors are BART's.
