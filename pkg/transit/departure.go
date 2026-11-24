// Package transit is the provider-neutral core: the departure model every
// provider returns, the Provider interface, and the filtering and grouping
// that turn raw departures into rows for a display.
//
// Nothing outside a provider package should know which provider produced a
// departure. See docs/ARCHITECTURE.md for the design.
package transit

import (
	"fmt"
	"strings"
	"time"
)

// Mode is the kind of vehicle a line uses.
type Mode string

// Modes follow the GTFS route_type groups.
const (
	ModeMetro    Mode = "metro"
	ModeTram     Mode = "tram"
	ModeRail     Mode = "rail"
	ModeBus      Mode = "bus"
	ModeFerry    Mode = "ferry"
	ModeCableway Mode = "cableway"
	ModeOther    Mode = "other"
)

// ParseMode parses a mode name. An empty string is ModeOther.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeMetro, ModeTram, ModeRail, ModeBus, ModeFerry, ModeCableway, ModeOther:
		return m, nil
	case "":
		return ModeOther, nil
	default:
		return "", fmt.Errorf("unknown mode %q", s)
	}
}

// Status says whether a departure runs as planned.
type Status string

// Statuses mirror GTFS-RT's schedule relationship.
const (
	StatusScheduled Status = "scheduled"
	StatusCanceled  Status = "canceled"
	// StatusAdded is a trip that is not in the published schedule.
	StatusAdded Status = "added"
)

// Line is a route as riders know it.
type Line struct {
	// ID is the provider's stable route identifier. Required.
	ID string
	// ShortName is what riders call the line: "735", "Azul".
	ShortName string
	// Color is the line's brand color as "#RRGGBB", or empty if unknown.
	Color string
	Mode  Mode
}

// NormalizeColor returns a color as "#RRGGBB", accepting it with or
// without the "#" and in either case, or "" when s is not a six-digit hex
// color. AWTRIX NG validates payloads strictly, so one malformed color
// could get a whole push rejected.
func NormalizeColor(s string) string {
	hex := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if len(hex) != 6 {
		return ""
	}
	for _, c := range hex {
		if !strings.ContainsRune("0123456789ABCDEF", c) {
			return ""
		}
	}
	return "#" + hex
}

// Departure is one vehicle leaving one stop.
type Departure struct {
	Line Line
	// Headsign is the destination shown on the vehicle.
	Headsign string
	StopID   string
	// Scheduled is the timetabled departure time.
	Scheduled time.Time
	// Predicted is the realtime estimate, or nil when only the schedule is
	// known.
	Predicted *time.Time
	Status    Status
}

// Time is the best known departure time: the prediction if there is one,
// otherwise the schedule.
func (d Departure) Time() time.Time {
	if d.Predicted != nil {
		return *d.Predicted
	}
	return d.Scheduled
}

// Realtime reports whether the departure has a live prediction.
func (d Departure) Realtime() bool { return d.Predicted != nil }

// MinutesUntil returns whole minutes from now until the departure, rounded
// down and never negative, which is how platform displays count.
func (d Departure) MinutesUntil(now time.Time) int {
	m := int(d.Time().Sub(now) / time.Minute)
	return max(m, 0)
}
