`feeds_v2.csv`: rows of the Mobility Database's feed list,
`https://files.mobilitydatabase.org/feeds_v2.csv`, downloaded on
2026-11-26: the systems tests search for, with their realtime feeds and a
replaced feed. The catalog is CC0; `feed_contact_email` is blanked.

`transitland/`: Transitland REST API responses recorded on 2026-11-26,
with geometry, tags and IDs removed: an operator search for "lisboa",
`/operators?feed_onestop_id=` and `/feeds/` for a schedule alone
(`f-carris~pt`), a schedule with keyless realtime (`f-barta~pa`), and an
operator with two schedules (`f-9q9-bart`).

Hand-written, not recorded: the last row of `feeds_v2.csv`
(`mdb-990001`), whose URL holds a `${NAME}` reference, and
`transitland/operators-search-mta.json`, an operator search where
Transitland's looser matching returns SFMTA beside MTA.
