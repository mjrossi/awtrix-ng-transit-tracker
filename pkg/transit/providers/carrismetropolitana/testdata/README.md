Responses recorded from `https://api.carrismetropolitana.pt/v2` on
2026-11-23 at 11:30:06 UTC. Tests use that instant as `Query.Now`.

- `arrivals_060001.json`: `GET /arrivals/by_stop/060001`, unchanged.
- `arrivals_060451.json`: `GET /arrivals/by_stop/060451`, unchanged.
- `lines.json`: `GET /lines`, trimmed to the lines in the arrivals files
  and to the fields the provider reads.
- `stops.json`: `GET /stops`, trimmed to a few stops and fields.

Data © Carris Metropolitana, licensed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
