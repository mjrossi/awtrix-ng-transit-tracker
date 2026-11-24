// Package demo is a provider that generates departures from fixed headways.
// It needs no network access, so it can drive a real clock end to end
// before any transit API is configured, and it is the reference
// implementation for the provider contract.
package demo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// Name is the provider's name in configuration.
const Name = "demo"

// LineConfig describes one generated service.
type LineConfig struct {
	ID        string `toml:"id"`
	ShortName string `toml:"short_name"`
	Color     string `toml:"color"`
	Mode      string `toml:"mode"`
	Headsign  string `toml:"headsign"`
	// Headway is the gap between departures, such as "6m".
	Headway string `toml:"headway"`
	// Offset shifts the timetable, such as "2m", so lines do not all leave
	// at the same moment.
	Offset string `toml:"offset"`
	// Stops lists the stop IDs the line serves. Empty means every stop.
	Stops []string `toml:"stops"`
	// Realtime marks every other departure as predicted, one minute late,
	// so both display styles can be seen.
	Realtime bool `toml:"realtime"`
}

// Config is the provider's configuration block.
type Config struct {
	Lines []LineConfig `toml:"lines"`
}

type line struct {
	transit.Line
	headsign string
	headway  time.Duration
	offset   time.Duration
	stops    map[string]bool
	realtime bool
}

// Provider generates departures. It is safe for concurrent use.
type Provider struct {
	lines []line
}

// Register adds the demo provider to r.
func Register(r *transit.Registry) {
	transit.RegisterConfig(r, Name, New)
}

// New validates cfg and returns a provider.
func New(cfg Config) (*Provider, error) {
	if len(cfg.Lines) == 0 {
		return nil, errors.New("at least one line is required")
	}
	p := &Provider{}
	for i, lc := range cfg.Lines {
		if lc.ID == "" {
			return nil, fmt.Errorf("lines[%d]: id is required", i)
		}
		mode, err := transit.ParseMode(lc.Mode)
		if err != nil {
			return nil, fmt.Errorf("lines[%d]: %w", i, err)
		}
		headway, err := time.ParseDuration(lc.Headway)
		if err != nil || headway <= 0 {
			return nil, fmt.Errorf("lines[%d]: headway %q must be a positive duration", i, lc.Headway)
		}
		var offset time.Duration
		if lc.Offset != "" {
			if offset, err = time.ParseDuration(lc.Offset); err != nil {
				return nil, fmt.Errorf("lines[%d]: offset: %w", i, err)
			}
		}
		stops := map[string]bool{}
		for _, s := range lc.Stops {
			stops[s] = true
		}
		short := lc.ShortName
		if short == "" {
			short = lc.ID
		}
		p.lines = append(p.lines, line{
			Line:     transit.Line{ID: lc.ID, ShortName: short, Color: transit.NormalizeColor(lc.Color), Mode: mode},
			headsign: lc.Headsign,
			headway:  headway,
			offset:   offset,
			stops:    stops,
			realtime: lc.Realtime,
		})
	}
	return p, nil
}

// Departures implements transit.Provider.
func (p *Provider) Departures(ctx context.Context, q transit.Query) ([]transit.Departure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	end := q.Now.Add(q.Window)
	var out []transit.Departure
	for _, l := range p.lines {
		if len(l.stops) > 0 && !l.stops[q.StopID] {
			continue
		}
		out = append(out, l.departures(q.StopID, q.Now, end)...)
	}
	return transit.SortAndLimit(out, q.Limit), nil
}

// departures lists scheduled times on the grid offset + k*headway (counted
// from the Unix epoch, so every instance agrees) that fall in [from, to].
func (l line) departures(stopID string, from, to time.Time) []transit.Departure {
	var out []transit.Departure
	epoch := time.Unix(0, 0)
	// Start one headway early so a late-running predicted departure whose
	// schedule is already past can still appear.
	first := int64((from.Sub(epoch) - l.offset - l.headway) / l.headway)
	for k := first; ; k++ {
		sched := epoch.Add(l.offset + l.headway*time.Duration(k)).In(from.Location())
		d := transit.Departure{
			Line:      l.Line,
			Headsign:  l.headsign,
			StopID:    stopID,
			Scheduled: sched,
			Status:    transit.StatusScheduled,
		}
		if l.realtime && k%2 == 0 {
			pred := sched.Add(time.Minute)
			d.Predicted = &pred
		}
		t := d.Time()
		if t.After(to) && sched.After(to) {
			break
		}
		if !t.Before(from) && !t.After(to) {
			out = append(out, d)
		}
	}
	return out
}
