Responses recorded from `https://api.tfl.gov.uk`, without an app key, on
2026-11-27 between 08:49:05 and 08:49:07 UTC, unchanged. Tests use
08:49:10 UTC as `Query.Now`.

- `StopPoint_<id>_Arrivals.json`: `GET /StopPoint/<id>/Arrivals` for
  Oxford Circus (`940GZZLUOXC`), Bank DLR (`940GZZDLBNK`) and bus stop E at
  Charing Cross (`490013766E`)
- `StopPoint_Search_trafalgar_square.json`:
  `GET /StopPoint/Search/trafalgar square?modes=tube,dlr,overground,elizabeth-line,tram,bus&includeHubs=false`
- `StopPoint_490013766E,490013766F,490G000803,490G000804,490G000832.json`:
  `GET /StopPoint/<ids>`, the bus results of that search
- `StopPoint_940GZZLUOXC.json`, `StopPoint_490013766E.json`:
  `GET /StopPoint/<id>`
- `StopPoint_490G00003044.json`: `GET /StopPoint/490G00003044`, a pair of
  bus stops at Homerton High Street, recorded at 09:09:38 UTC
- `not_found.json`: `GET /StopPoint/ZZZNOPE/Arrivals` (HTTP 404)

The provider also asks for `river-bus`; the fake server ignores the
query. At Bank DLR, every prediction carries the same `id`, and two are
trains ending their trip there.

Powered by TfL Open Data. Contains OS data © Crown copyright and database
rights 2016 and Geomni UK Map data © and database rights [2019].
