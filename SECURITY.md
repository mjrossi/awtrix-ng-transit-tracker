# Security

## Reporting a vulnerability

Please report it privately through GitHub's
[security advisories](https://github.com/mjrossi/urbanist-timetable/security/advisories/new)
rather than in a public issue.

## Model

Urbanist Timetable runs in two ways: as a service for the clock on a home
network, usually in Docker on a NAS, and as a command in a user's terminal.
Either way it reads at most one configuration file, calls transit APIs,
the Mobility Database and Transitland (for `agencies search` and
directory system IDs) and, for the clock, the clock itself, caches GTFS
schedules and the Mobility Database's public feed list on disk, and
listens on no ports.

### What it trusts

- **The configuration file and its environment.** Whoever can edit
  `config.toml` or set the container's environment chooses which URLs the
  service fetches and where it pushes. The file is mounted read-only.
- **Command-line arguments**, such as `--realtime` and `--static` URLs,
  which the user typing them chooses.
- **The configured feed URLs**, including the catalog's embedded ones, to
  be the agencies they claim to be. Some agencies serve feeds over plain
  HTTP (the New York subway's schedule zip, for example), which offers no
  integrity.
- **The Mobility Database and Transitland, to name agencies' feeds.** A
  directory ID such as `mdb-2929` fetches the URLs the directory gives for
  it, and `systems show` sends a HEAD request to its schedule. The
  Mobility Database is edited by its community, so nothing else in its
  data is trusted: see `${NAME}` below.

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

Text from feeds and servers, including error messages, is printed to the
terminal without control or format characters, so it cannot drive the
terminal. Every request has a timeout. Line colors are normalized to `#RRGGBB`
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
  - `transitland`, and `agencies search`, send the Transitland key in the
    `apikey` header, never the URL, so request errors cannot carry it;
  - blocks written for directory systems hold `${NAME}` references, never
    key values. Directory data that holds `${` itself is dropped, and a
    directory block reads only the variables it introduced
    (`config.ExpandSettings`), so an entry cannot have another secret,
    such as `AWTRIX_PASSWORD`, sent to a host of its choosing. Key
    variables named after an agency never take a name this tool or its
    catalog reads;
  - the `transitland` key, `gtfsrt` `headers` and `metrolisboa` credentials
    go only to the host first asked: on a redirect to another host, or from
    https to http, they and `Referer` are dropped
    (`upstream.KeepHeadersOnHost`, and `directory`'s own client for
    `agencies search`). A `gtfsrt` block sends its `headers` to each of its
    feeds, so a directory block whose key header would reach several hosts
    says so in its comment;
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
  schedules in `/data/cache` (`--cache-dir`), named by a hash of their URL,
  with the stop times read from them, the Mobility Database's feed list and
  Transitland lookups of directory IDs.
  Without a cache directory, schedules are staged as temporary files in
  `/tmp`, and with a read-only root filesystem downloads fall back to
  memory, without timetabled trips.
- Images are built by GitHub Actions, from version tags and from `main`,
  only after `just ci` passes.
