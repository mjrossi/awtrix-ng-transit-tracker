package config

import "time"

// daysPerMonth is the longest month, so an estimate is never low.
const daysPerMonth = 31

// EstimateRequests estimates the upstream requests a month made by each
// provider block named in refresh, which maps a block to the shortest time
// between its requests for one stop. Boards that poll the same stop with
// the same window share requests; a stop is counted for every minute in
// which any of its boards is active and not in quiet hours.
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
	// Any date works: Active and Quiet only read the time of day. Sampling
	// a whole UTC day covers every board's time zone.
	day := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	for k, bs := range boards {
		active := 0
		for m := range 24 * 60 {
			t := day.Add(time.Duration(m) * time.Minute)
			if c.Quiet(t) {
				continue
			}
			for _, b := range bs {
				if b.Active(t.In(b.Location())) {
					active++
					break
				}
			}
		}
		every := refresh[k.provider]
		perDay := (time.Duration(active)*time.Minute + every - 1) / every
		out[k.provider] += int(perDay) * daysPerMonth
	}
	return out
}
