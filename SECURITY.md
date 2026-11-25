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
| GTFS schedule zip | 512 MB download; staged in a temporary file, or kept in the cache directory when one is set, with only routes, stops and trips held in memory |
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
    (`catalog.Options.Decode`), because values hold expanded
    secrets by then;
  - `gtfsrt` names URLs without credentials or query strings (`redact`);
  - cached `gtfsrt` schedules are named by a SHA-256 of their URL and hold
    only the zip and its ETag and Last-Modified, never the URL or headers;
  - `transitland` sends its key in the `apikey` header, never the URL, so
    request errors cannot carry it;
  - the `transitland` key, `gtfsrt` `headers` and `metrolisboa` credentials
    go only to the host first asked: on a redirect to another host, or from
    https to http, they and `Referer` are dropped
    (`upstream.KeepHeadersOnHost`);
  - the clock URL may embed basic-auth credentials; logs show only its host
    (`Client.Host`), the state file its host and path (`Client.ID`), and
    URL parse errors drop the raw URL.

  New providers follow the same rules (CONTRIBUTING.md, "Adding a provider").
- The clock's basic auth is usually sent over plain HTTP, because AWTRIX NG
  serves HTTP on the LAN. Treat that password as LAN-visible.

## Container

- A static binary on Alpine, running as a non-root user.
- No published ports. The only writes are the state file in `/data`, which
  holds the clock's host and board names, never credentials, and GTFS
  schedules in `/data/cache` (`--cache-dir`), named by a hash of their URL.
  Without a cache directory, schedules are staged as temporary files in
  `/tmp`, and with a read-only root filesystem downloads fall back to
  memory.
- Images are built by GitHub Actions, from version tags and from `main`,
  only after `just ci` passes.
