# Running the clock

How `clock run` behaves once it is set up. Setting it up is in the
[README](../README.md#on-a-pixel-clock); every setting is in
[`config.example.toml`](../config.example.toml).

## When boards are shown

Two settings take a daily window such as `"06:30-23:30"`, which may run past
midnight:

- A board's `active_hours` are the hours you want to see it, such as your
  commute. They are not the agency's service hours.
- The top-level `quiet_hours` are a night mode for the whole service. They
  hide every board, whatever its `active_hours`, and always follow the
  top-level `timezone`.

Without `timezone`, the `TZ` environment variable applies, which in the
image is UTC unless you set it. A board's own `timezone` overrides the
top-level one for that board's `active_hours`, for boards from another
city; `"Local"` there means the `TZ` zone.

A hidden board is not polled, and the clock skips it. In quiet hours the
service uses no network at all, except to hide the boards at the start: each
within one `poll_interval`, retried if the clock does not answer, until the
app's `lifetime` has passed. `clock preview` and `stops search` still work. The
display stays on and shows the clock's other apps.

A hidden board returns to the same place if you have arranged the rotation in
the clock's web UI, which lists it as "no data" meanwhile. Boards you have
not arranged return at the end of the rotation.

Stopping the service works the same way: its boards leave the display when
their `lifetime` ends, and the web UI lists them as "no data" until it runs
again.

## How it behaves

- Each push sets a lifetime (three poll intervals by default), so the app
  disappears from the clock if the service stops instead of showing stale
  times.
- If a provider fails, the last good data is reused for up to `max_stale`
  (10 minutes by default) while the provider is retried with backoff.
  Minutes are recomputed on every push.
- A board removed from the configuration is removed from the clock at the
  next start, including its place in the rotation. If the clock is off, this
  is retried with backoff, from one minute up to one hour. Apps of other
  senders, scripts and built-in apps are left alone.
- Logs show each board's configuration and changes of state. Set
  `TIMETABLE_LOG_LEVEL=debug` to log every fetch and push.
- The clock forgets pushed apps when it restarts; the next poll restores
  them.
- The configuration is read at startup, so restart the container after
  editing it.

## The data volume

`/data` holds the cached schedules and a small file naming the configured
boards, so that a board you remove from the configuration also leaves the
clock's app list. Mount a volume there: without one, each new container
starts with an empty state file, and boards removed from the configuration
are not cleaned up after a restart. To use a folder instead of a named
volume, make it writable for the container's non-root user. Outside
Docker, set `--state` (or `TIMETABLE_STATE`) to a file path; without it,
removed boards stay in the clock's list as "no data".

GTFS timetables are downloaded once a day to a temporary file in `/tmp`
(or the cache directory) and only the needed parts are kept in memory;
the stop times of the configured stops are read from the file. With a
read-only root filesystem, mount a tmpfs at `/tmp`; otherwise downloads
fall back to memory, which needs about as much RAM as the zip is large,
and boards show no timetabled trips: only realtime ones, or an error for
a board without `realtime_urls`.
