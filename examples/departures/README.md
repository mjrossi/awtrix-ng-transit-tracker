# departures

A small program that uses Urbanist Timetable as a library. It imports only
`pkg/`, and lives in a module of its own, so CI's build of it shows that
nothing it needs is in `internal/`.

```bash
go run . nyc-subway 127N
```

It opens a ready-made system with `providers.Open` and prints the next
hour's departures at one stop. `gtfsrt` systems show only departures with
live predictions. Systems that need keys, such as `lisbon-metro`, need them
passed in the settings map.
