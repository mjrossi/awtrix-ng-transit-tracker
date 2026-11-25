package transitland

import (
	"cmp"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// departureLimit caps departures per stop and per child platform. It is
// the server's maximum, since filters only see what it returns.
const departureLimit = 1000

// timetable is one fetched answer for a stop.
type timetable struct {
	deps []transit.Departure
	// until is the last departure of a list cut off at departureLimit, or
	// zero when the answer is complete.
	until time.Time
}

type departuresResponse struct {
	Stops []wireStop `json:"stops"`
}

type wireStop struct {
	Departures []wireDeparture `json:"departures"`
	// Children are a station's platforms; a station's departures are
	// listed under them.
	Children []struct {
		Departures []wireDeparture `json:"departures"`
	} `json:"children"`
}

type wireDeparture struct {
	ServiceDate          string    `json:"service_date"`
	StopHeadsign         string    `json:"stop_headsign"`
	PickupType           int       `json:"pickup_type"`
	ScheduleRelationship string    `json:"schedule_relationship"`
	Departure            wireEvent `json:"departure"`
	Trip                 struct {
		TripID               string `json:"trip_id"`
		TripHeadsign         string `json:"trip_headsign"`
		ScheduleRelationship string `json:"schedule_relationship"`
		Route                struct {
			OnestopID      string `json:"onestop_id"`
			RouteID        string `json:"route_id"`
			RouteShortName string `json:"route_short_name"`
			RouteLongName  string `json:"route_long_name"`
			RouteColor     string `json:"route_color"`
			RouteType      *int   `json:"route_type"`
		} `json:"route"`
	} `json:"trip"`
}

type wireEvent struct {
	// Scheduled is the GTFS time of the service day, which passes 24:00.
	Scheduled string `json:"scheduled"`
	// ScheduledUTC is null for trips added in realtime.
	ScheduledUTC *time.Time `json:"scheduled_utc"`
	EstimatedUTC *time.Time `json:"estimated_utc"`
}

// lists returns every departure list in the answer: each stop's own and its
// platforms'.
func (r departuresResponse) lists() [][]wireDeparture {
	var out [][]wireDeparture
	for _, s := range r.Stops {
		out = append(out, s.Departures)
		for _, c := range s.Children {
			out = append(out, c.Departures)
		}
	}
	return out
}

// newTimetable converts an answer's departure lists, dropping trips listed
// twice.
func newTimetable(lists [][]wireDeparture) timetable {
	type seen struct {
		trip string
		at   int64
	}
	dup := map[seen]bool{}
	var out timetable
	for _, list := range lists {
		// The server lists added trips on top of the limit.
		scheduled := 0
		var last time.Time
		for _, w := range list {
			if at := w.Departure.ScheduledUTC; at != nil {
				scheduled++
				if at.After(last) {
					last = *at
				}
			}
			d, ok := convert(w)
			if !ok {
				continue
			}
			k := seen{w.Trip.TripID, d.Scheduled.UnixNano()}
			if w.Trip.TripID != "" && dup[k] {
				continue
			}
			dup[k] = true
			out.deps = append(out.deps, d)
		}
		if scheduled >= departureLimit && (out.until.IsZero() || last.Before(out.until)) {
			out.until = last
		}
	}
	return out
}

// convert maps one departure, reporting false for ones riders cannot board.
func convert(w wireDeparture) (transit.Departure, bool) {
	predicted := w.Departure.EstimatedUTC
	if predicted != nil && predicted.IsZero() {
		predicted = nil
	}
	scheduled := cmp.Or(w.Departure.ScheduledUTC, predicted)
	if scheduled == nil {
		return transit.Departure{}, false
	}
	// pickup_type 1: no pickup, as at a trip's last stop.
	if w.PickupType == 1 {
		return transit.Departure{}, false
	}
	status := transit.StatusScheduled
	for _, rel := range []string{w.ScheduleRelationship, w.Trip.ScheduleRelationship} {
		switch rel {
		case "SKIPPED", "DELETED":
			return transit.Departure{}, false
		case "CANCELED":
			status = transit.StatusCanceled
		case "ADDED", "NEW", "DUPLICATED":
			if status == transit.StatusScheduled {
				status = transit.StatusAdded
			}
		}
	}

	r := w.Trip.Route
	id := cmp.Or(r.OnestopID, r.RouteID)
	mode := transit.ModeOther
	if r.RouteType != nil {
		mode = transit.ModeForRouteType(*r.RouteType)
	}
	d := transit.Departure{
		Line: transit.Line{
			ID:        id,
			ShortName: cmp.Or(r.RouteShortName, r.RouteLongName, id),
			Color:     transit.NormalizeColor(r.RouteColor),
			Mode:      mode,
		},
		Headsign:  cmp.Or(w.StopHeadsign, w.Trip.TripHeadsign),
		Scheduled: *scheduled,
		Predicted: predicted,
		Status:    status,
	}
	return d, d.Line.ID != ""
}
