# Urbanist Timetable: joining the Urbanist family (v0.2.0)

Decided 2026-11-23.

## Context

The Urbanist family is a publishing brand: *The Urbanist Lexicon* (the blog
on mjrossi.com) and *Urbanist Atlas* (its "companion volume"), sharing the
Lexicon Family design system. Each member is "Urbanist" + a reference-book
noun. The maintainer chose to make this repo a full member rather than
link it from outside, accepting the rename cost while the project is v0.x
with no known users. The maintainer has since run it on their own TC002,
where it works and looks right. The docs still say it hasn't been tried
on a real clock, and this release fixes that.

## Design (short)

**Approaches considered**
- A. Stay independent: keep the name, at most a projects-page entry. No cost, no connection.
- B. Link without renaming: keep the findable `awtrix-ng-transit-tracker`, add a family line, cut a normal v0.2.0.
- **C. Full member, rename (chosen):** strongest brand tie. Costs: old image pulls stop updating, and the new name says less about what the tool does. The README tagline and opening line make up for that.

**Decided by the maintainer**
- Name: **Urbanist Timetable**. Repo `mjrossi/urbanist-timetable`, image `ghcr.io/mjrossi/urbanist-timetable` (the publish workflow builds `IMAGE` from `github.repository`, so it follows the repo rename on its own).
- Rename everything, including the binary, the `cmd/` directory and the env-var prefix.
- No web page this release. A landing page in the Lexicon Family design gets its own design later.

**Calls made during design (accepted with the plan)**
- Binary `urbanist-timetable`, `cmd/urbanist-timetable/`. This mirrors Atlas's `urbanist-atlas-server`.
- Env prefix `TIMETABLE_*` (`TIMETABLE_CONFIG`, `TIMETABLE_LOG_FORMAT`, `TIMETABLE_LOG_LEVEL`). Atlas uses `URBANIST_*`, but a bare `URBANIST_` prefix would make it unclear which service a variable belongs to.
- Version **v0.2.0**: a breaking change while still in 0.x.
- README: describe it as transit departures for your displays, but keep "AWTRIX NG" in the first line so clock owners can still find it, e.g. "Live transit departures for the displays around your home, starting with AWTRIX NG pixel clocks." Add a family line after Atlas's pattern: "A companion to *The Urbanist Lexicon* and *Urbanist Atlas*." Draft tagline: "Your next departure, wherever you look."
- No fallback that still reads the old `TRANSIT_*` names. With no known users, a migration note in the release is enough.

**Direction: departures for any display, with the clock as the first one (decided by the maintainer)**
- Urbanist Timetable is positioned as a transit departures service, and the AWTRIX NG clock is its first supported display. The two audiences in view are smart-home tinkerers who run it themselves on other displays, and developers using a hosted API the maintainer operates.
- This release is the rename only. The broadening goes on the roadmap: (1) a self-hosted HTTP endpoint that returns departures as JSON, which means giving up the "listens on no ports" rule; (2) MQTT and a Home Assistant add-on, both already listed under "Later"; (3) a hosted public API, reusing Atlas's Phase 2 plans for API keys and rate limits. Item 3 needs its own design first, covering feed redistribution licenses and running costs.
- The architecture already supports this: `pkg/transit` is library-first, and everything past a provider sees only `transit.Departure`.

**Non-goals:** a web page, Lexicon Family visual work, any behavior change, building any of the roadmap items.
