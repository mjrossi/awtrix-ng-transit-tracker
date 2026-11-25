package config

import (
	"maps"
	"slices"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// daysPerMonth is the longest month.
const daysPerMonth = 31

// QuotaUse is the estimated requests a month against one quota, which
// provider blocks with the same account share.
type QuotaUse struct {
	// Providers are the blocks' names, sorted.
	Providers []string
	Requests  int
	// Quota is the smallest the blocks configure, or the smallest default if
	// none does.
	Quota int
}

// Over reports whether the estimate exceeds the quota.
func (u QuotaUse) Over() bool { return u.Requests > u.Quota }

// QuotaUses estimates monthly requests for every quota the metered providers
// in provs, keyed by block name, draw on. It is sorted by block name.
func (c *Config) QuotaUses(provs map[string]transit.Provider) []QuotaUse {
	refresh := map[string]time.Duration{}
	quotas := map[string]transit.Quota{}
	for name, p := range provs {
		if m, ok := p.(transit.Metered); ok {
			quotas[name] = m.Quota()
			refresh[name] = quotas[name].RefreshInterval
		}
	}
	est := c.EstimateRequests(refresh)
	var accounts []string
	blocks := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(quotas)) {
		a := quotas[name].Account
		if blocks[a] == nil {
			accounts = append(accounts, a)
		}
		blocks[a] = append(blocks[a], name)
	}
	var out []QuotaUse
	for _, a := range accounts {
		u := QuotaUse{Providers: blocks[a]}
		var limits, defaults []int
		for _, name := range blocks[a] {
			u.Requests += est[name]
			defaults = append(defaults, quotas[name].DefaultLimit)
			if l := quotas[name].Limit; l > 0 {
				limits = append(limits, l)
			}
		}
		if len(limits) == 0 {
			limits = defaults
		}
		u.Quota = slices.Min(limits)
		out = append(out, u)
	}
	return out
}

// EstimateRequests estimates the upstream requests a month made by each
// provider block named in refresh, which maps a block to the shortest time
// between its requests for one stop. Boards that poll the same stop share
// requests: a poll makes one once the refresh interval has passed since the
// last. Failed fetches that are retried, searches and previews are not
// counted, nor is the one request repeated when boards with different windows
// start.
func (c *Config) EstimateRequests(refresh map[string]time.Duration) map[string]int {
	type source struct{ provider, stop string }
	boards := map[source][]Board{}
	for _, b := range c.Boards {
		for _, s := range b.Stops {
			if _, ok := refresh[s.Provider]; !ok {
				continue
			}
			k := source{s.Provider, s.Stop}
			boards[k] = append(boards[k], b)
		}
	}

	out := map[string]int{}
	// Active and Quiet only read the time of day, and a whole UTC day covers
	// every time zone. Where zones differ in daylight saving, their overlap
	// shifts with the season; one date stands for the year.
	day := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	for k, bs := range boards {
		var polls []time.Duration
		for _, b := range bs {
			polls = append(polls, c.pollTimes(b, day)...)
		}
		slices.Sort(polls)
		perDay, last := 0, -refresh[k.provider]
		for _, t := range polls {
			if t-last >= refresh[k.provider] {
				perDay++
				last = t
			}
		}
		out[k.provider] += perDay * daysPerMonth
	}
	return out
}

// pollTimes lists when b polls on day, as time since its start. Each active
// span starts with a poll.
func (c *Config) pollTimes(b Board, day time.Time) []time.Duration {
	// A zero interval would never advance.
	poll := max(b.PollInterval.D(), MinPollInterval)
	var out []time.Duration
	var due time.Duration
	for start := time.Duration(0); start < 24*time.Hour; start += time.Minute {
		end := start + time.Minute
		at := day.Add(start)
		if c.Quiet(at) || !b.Active(at.In(b.Location())) {
			due = end
			continue
		}
		for ; due < end; due += poll {
			out = append(out, due)
		}
	}
	return out
}
