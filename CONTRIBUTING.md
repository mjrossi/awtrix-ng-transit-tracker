# Contributing

## Adding your city

Most agencies need **no code**. If your agency publishes static GTFS and a
GTFS-Realtime `TripUpdates` feed (look it up in the
[Mobility Database](https://mobilitydatabase.org/)), the generic `gtfsrt`
provider serves it from configuration alone:

```toml
[providers.myagency]
type = "gtfsrt"
static_url = "https://example.org/gtfs.zip"
realtime_urls = ["https://example.org/tripupdates.pb"]
```

If it works, please add it to the catalog so others can use
`system = "<id>"`: copy a file in `pkg/transit/catalog/systems/`, set
`status = "community"`, pick a busy `sample_stop`, and run
`just catalog-check` (it calls the live feeds) before opening a pull
request. Add the system to the README's table.

## Adding a provider

A new provider package is accepted only when the generic GTFS-RT provider
cannot serve the agency, because:

- the agency publishes no GTFS-RT `TripUpdates`, or
- its own API is clearly better, such as realtime data the GTFS-RT feed
  lacks.

This keeps the number of adapters small enough to maintain.

A provider must:

1. Live in `pkg/transit/providers/<name>/` and implement
   `transit.Provider` (and `transit.StopSearcher` if the API can search).
   It must be safe for concurrent use: boards that name the same provider
   block share one instance.
2. Register itself in `pkg/transit/providers/providers.go`.
3. Pass `transittest.Run` against recorded responses in its `testdata/`.
   Tests must not call the live API.
4. Decode its options strictly through `transit.Options`, and read secrets
   from configuration (where `${NAME}` references the environment), never
   from hard-coded values.
5. Keep secrets out of error messages: they reach the logs, so name URLs
   without their query strings. See [`SECURITY.md`](./SECURITY.md).
6. Return what it can with a `*transit.PartialError` when part of its data
   fails, such as one of several feeds, rather than failing the whole
   query.
7. Document its options in `config.example.toml`.

The `demo` provider is the reference implementation.

## Before opening a pull request

Run `just ci`.

## Releasing

1. Set `version` in `cmd/urbanist-timetable/main.go` to the new version, for
   builds outside the image, and merge it to `main`.
2. Tag and push: `git tag v1.2.3 && git push origin v1.2.3`. The `publish`
   workflow runs `just ci`, then pushes the multi-arch image.
3. Publish the release notes: `gh release create v1.2.3 --generate-notes`.
4. On an arm64 machine, check that
   `docker run --rm ghcr.io/mjrossi/urbanist-timetable:1.2.3 --version`
   prints `1.2.3`.
