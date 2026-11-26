// Package transit is the provider-neutral core: the departure model every
// provider returns, the Provider interface, and the filtering and grouping
// that turn raw departures into rows for a display.
//
// Code that uses departures never needs to know which provider produced
// them. To get a provider, open one with the providers package, or build one
// directly from its own package, such as gtfsrt.
package transit

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
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

// ModeForRouteType maps a GTFS route_type, basic or extended, to a mode.
func ModeForRouteType(t int) Mode {
	switch {
	case t == 0, t == 5, t >= 900 && t < 1000:
		return ModeTram
	case t == 1, t == 12, t >= 400 && t < 500:
		return ModeMetro
	case t == 2, t >= 100 && t < 200:
		return ModeRail
	case t == 3, t == 11, t >= 200 && t < 300, t >= 700 && t < 800:
		return ModeBus
	case t == 4, t >= 1000 && t < 1100, t >= 1200 && t < 1300:
		return ModeFerry
	case t == 6, t == 7, t >= 1300 && t < 1500:
		return ModeCableway
	default:
		return ModeOther
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

// CompareLines orders lines for listing: numbered ones first, in numeric
// order so 2 comes before 10, then named ones, then by ID.
func CompareLines(a, b Line) int {
	return cmp.Or(compareLineNames(a.ShortName, b.ShortName), strings.Compare(a.ID, b.ID))
}

func compareLineNames(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return cmp.Compare(na, nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// StopLine is a line at a stop and where its trips from there go.
type StopLine struct {
	Line Line
	// Headsigns are distinct and sorted, and may be empty.
	Headsigns []string
}

// AddStopLine records that l leaves a stop toward headsign. Lines riders
// call by the same name are one, since a stop in two feeds lists the same
// line under two route IDs. An empty headsign adds the line alone.
func AddStopLine(lines []StopLine, l Line, headsign string) []StopLine {
	name := cmp.Or(l.ShortName, l.ID)
	i := slices.IndexFunc(lines, func(s StopLine) bool { return cmp.Or(s.Line.ShortName, s.Line.ID) == name })
	if i < 0 {
		lines = append(lines, StopLine{Line: l})
		i = len(lines) - 1
	}
	if headsign == "" {
		return lines
	}
	if j, found := slices.BinarySearch(lines[i].Headsigns, headsign); !found {
		lines[i].Headsigns = slices.Insert(lines[i].Headsigns, j, headsign)
	}
	return lines
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
