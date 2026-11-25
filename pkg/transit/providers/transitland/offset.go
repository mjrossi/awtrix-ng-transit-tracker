package transitland

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// lateLookback is how late a vehicle can always run and still be shown.
	lateLookback = 30 * time.Minute
	// clockChange widens requests at both ends. GTFS times count from noon
	// minus 12 hours, so around a daylight saving change neighboring service
	// days, and an offset learned from one, differ by an hour.
	clockChange = time.Hour
	// offsetMaxAge limits how long a wrong offset is used.
	offsetMaxAge = 12 * time.Hour
)

// serviceOffset is how far a stop's GTFS times are ahead of UTC, which is
// the agency's zone rather than the stop's.
type serviceOffset struct {
	ahead time.Duration
	at    time.Time
}

// departureParams asks for departures in the span from now, and from
// before it once the stop's offset is known. The server filters
// by scheduled time, so without the lookback a late vehicle that has not come
// yet is left out.
func (p *Provider) departureParams(key string, now time.Time, span time.Duration) url.Values {
	params := url.Values{
		"limit": {strconv.Itoa(departureLimit)},
		// The default falls back to a typical day's timetable when the feed
		// has expired, which would show trips that do not run.
		"use_service_window": {"false"},
	}
	p.mu.Lock()
	offset, ok := p.offsets[key]
	p.mu.Unlock()
	if !ok || now.Sub(offset.at).Abs() > offsetMaxAge {
		params.Set("next", strconv.Itoa(int(span/time.Second)))
		return params
	}
	from := now.Add(offset.ahead - lateLookback - clockChange).UTC()
	start := from.Sub(from.Truncate(24 * time.Hour))
	params.Set("date", from.Format(time.DateOnly))
	params.Set("start_time", gtfsTime(start))
	params.Set("end_time", gtfsTime(start+lateLookback+span+2*clockChange))
	return params
}

// learnOffset records the stop's service-day offset for later requests.
func (p *Provider) learnOffset(key string, lists [][]wireDeparture, now time.Time) {
	for _, list := range lists {
		for _, w := range list {
			if ahead, ok := offsetOf(w); ok {
				p.mu.Lock()
				p.offsets[key] = serviceOffset{ahead, now}
				p.mu.Unlock()
				return
			}
		}
	}
}

func gtfsTime(d time.Duration) string {
	s := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}

// offsetOf derives the service-day offset from a departure's GTFS time and
// the instant it stands for.
func offsetOf(w wireDeparture) (time.Duration, bool) {
	if w.Departure.ScheduledUTC == nil {
		return 0, false
	}
	day, err := time.Parse(time.DateOnly, w.ServiceDate)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(w.Departure.Scheduled, ":")
	if len(parts) != 3 {
		return 0, false
	}
	var secs int
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, false
		}
		secs = secs*60 + n
	}
	ahead := day.Add(time.Duration(secs) * time.Second).Sub(*w.Departure.ScheduledUTC)
	// Zones run from UTC-12 to UTC+14.
	return ahead, ahead >= -12*time.Hour && ahead <= 14*time.Hour
}
