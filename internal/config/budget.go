package config

import (
	"slices"
	"time"
)

// daysPerMonth is the longest month.
const daysPerMonth = 31

// EstimateRequests estimates the upstream requests a month made by each
// provider block named in refresh, which maps a block to the shortest time
// between its requests for one stop. Boards that poll the same stop with
// the same window share requests; a stop is counted for every minute in
// which any of its boards is active and not in quiet hours. Failed fetches
// that are retried, searches and previews are not counted.
func (c *Config) EstimateRequests(refresh map[string]time.Duration) map[string]int {
	type source struct {
		provider, stop string
		window         time.Duration
	}
	boards := map[source][]Board{}
	for _, b := range c.Boards {
		for _, s := range b.Stops {
			if _, ok := refresh[s.Provider]; !ok {
				continue
			}
			k := source{s.Provider, s.Stop, b.Window.D()}
			boards[k] = append(boards[k], b)
		}
	}

	out := make(map[string]int, len(refresh))
	for name := range refresh {
		out[name] = 0
	}
	// Active and Quiet only read the time of day, and a whole UTC day covers
	// every time zone. Where zones differ in daylight saving, their overlap
	// shifts with the season; one date stands for the year.
	day := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	for k, bs := range boards {
		every := refresh[k.provider]
		poll := bs[0].PollInterval.D()
		for _, b := range bs[1:] {
			poll = min(poll, b.PollInterval.D())
		}
		every = max(every, poll)

		active := func(t time.Time) bool {
			return !c.Quiet(t) && slices.ContainsFunc(bs, func(b Board) bool { return b.Active(t.In(b.Location())) })
		}
		// Each active span starts with a fetch, so spans round up one by one.
		perDay, span := 0, time.Duration(0)
		endSpan := func() {
			perDay += int((span + every - 1) / every)
			span = 0
		}
		for m := range 24 * 60 {
			if active(day.Add(time.Duration(m) * time.Minute)) {
				span += time.Minute
			} else {
				endSpan()
			}
		}
		endSpan()
		out[k.provider] += perDay * daysPerMonth
	}
	return out
}
