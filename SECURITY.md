# Security

## Reporting a vulnerability

Please report it privately through GitHub's
[security advisories](https://github.com/mjrossi/urbanist-timetable/security/advisories/new)
rather than in a public issue.

## Model

Urbanist Timetable runs on a home network, usually in Docker on a NAS. It
reads one configuration file, calls transit APIs and the clock, and listens
on no ports.

### What it trusts

- **The configuration file and its environment.** Whoever can edit
  `config.toml` or set the container's environment chooses which URLs the
  service fetches and where it pushes. The file is mounted read-only.
- **The configured feed URLs**, including the catalog's embedded ones, to
  be the agencies they claim to be. Some agencies serve feeds over plain
  HTTP (the New York subway's schedule zip, for example), which offers no
  integrity.

### What it does not trust

Feed and API contents. They are decoded defensively:

| Input | Bound |
|---|---|
| GTFS schedule zip | 512 MB download; staged in a temporary file, only routes, stops and trips kept |
| GTFS-Realtime feed | 64 MB; hand-written protobuf decoder that skips unknown fields and rejects malformed lengths |
| Carris Metropolitana JSON | 32 MB |
| Metro de Lisboa JSON | 4 MB; token response 64 KB |
| Transitland JSON | 16 MB |
| Clock API replies (app list, device state) | 1 MB decoded |
| Clock error bodies | 64 KB |

Every request has a timeout. Line colors are normalized to `#RRGGBB`
before they reach the clock, and text is sent as JSON strings, never
interpreted.

## Secrets

- `${NAME}` in `[clock]` and `[providers.*]` settings reads an environment
  variable, so keys and passwords stay out of the file. Expansion happens
  after TOML decoding, so comments are left alone and values need no
  escaping. A missing variable is an error that names the variable.
- Secrets must not reach logs or error messages:
  - provider option errors name the unknown key, never the line
    (`config.ProviderOptions.Decode`), because values hold expanded
    secrets by then;
  - `gtfsrt` names URLs without credentials or query strings (`redact`);
  - `transitland` sends its key in the `apikey` header, never the URL, so
    request errors cannot carry it;
  - the clock URL may embed basic-auth credentials; logs show only its host
    (`Client.Host`), the state file its host and path (`Client.ID`), and
    URL parse errors drop the raw URL.

  New providers follow the same rules (CONTRIBUTING.md, "Adding a provider").
- The clock's basic auth is usually sent over plain HTTP, because AWTRIX NG
  serves HTTP on the LAN. Treat that password as LAN-visible.

## Container

- A static binary on Alpine, running as a non-root user.
- No published ports. The only writes are temporary schedule files in
  `/tmp`, where with a read-only root filesystem downloads fall back to
  memory, and the state file in `/data`, which holds the clock's host and
  board names, never credentials.
- Images are built by GitHub Actions, from version tags and from `main`,
  only after `just ci` passes.

## When HTTP output lands

The planned `serve` command (PLAN.md, "Direction") is the first listener.
Its design must meet these rules before it merges:

- **Opt-in.** Off unless configured; the clock-only setup stays portless.
- **An explicit bind address**, documented for Docker, where `0.0.0.0`
  inside the container plus a published port is the usual setup.
- **Read-only.** `GET` only, nothing that changes state or configuration.
- **Served from snapshots.** Requests read the last poll's result and never
  call a provider, so HTTP traffic cannot multiply upstream API traffic.
- **Bounded server.** `http.Server` read, write and idle timeouts, and a
  header size limit.
- **No debug surface.** No `pprof`, `expvar` or config dump.
- **Minimal exposure.** Board names, stop IDs and departures only, never
  provider settings, URLs or the clock's address.
- **Decided, not defaulted:** whether to allow CORS (browser dashboards
  need it) and whether to offer an optional bearer token for setups that
  expose the port beyond the LAN.
