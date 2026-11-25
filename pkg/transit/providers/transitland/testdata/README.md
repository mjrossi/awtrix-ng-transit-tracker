Hand-written responses for an invented agency, in the shape of the Transitland
API's answers. Transitland's terms restrict redistributing its responses, so
do not replace these with recordings. Tests use 16:04:34 UTC on 2026-11-25 as
`Query.Now`; the agency is six hours behind UTC.

- `departures_HC.json`: a station whose departures are listed under its
  platforms, as in an answer to a request with the lookback. It covers live,
  schedule-only and canceled trips, a late one (scheduled 15:58, predicted
  16:21), added trips (no scheduled time or headsign), stop headsigns and
  departures that have left.
- `departures_QUIET.json`: a known stop with no departures.
- `stops_search.json`: an answer to a stop search.

An unknown stop gets 200 with an empty `stops`.
