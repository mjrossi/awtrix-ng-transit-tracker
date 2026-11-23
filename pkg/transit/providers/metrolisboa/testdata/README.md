Responses recorded from
`https://api.metrolisboa.pt:8243/estadoServicoML/1.0.1` on 2026-11-23
between 12:02:24 and 12:03:06 UTC, unchanged. Tests use 12:03:10 UTC as
`Query.Now`.

- `infoEstacao_todos.json`: `GET /infoEstacao/todos`
- `infoDestinos_todos.json`: `GET /infoDestinos/todos`
- `tempoEspera_Linha_<line>.json`: `GET /tempoEspera/Linha/<line>`
- `bad_station.json`: `GET /tempoEspera/Estacao/ZZ` (HTTP 200 with
  `"codigo":"404"`)

Some entries are deliberately kept as recorded: platforms at Reboleira,
Santa Apolónia and Cais do Sodré carry `hora` values from 2024 and
`"--"` instead of seconds, which the provider must drop.

Data © Metropolitano de Lisboa, E.P.E., licensed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
