package gtfsrt

import (
	"strconv"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// serves reports whether any trip in f stops at stop.
func (f feed) serves(st *static, stop string) bool {
	for _, tu := range f.trips {
		for _, stu := range tu.stops {
			if st.atStop(stu.stopID, stop) {
				return true
			}
		}
	}
	return false
}

// atStop reports whether stopID is want or one of its platforms.
func (s *static) atStop(stopID, want string) bool {
	if stopID == want {
		return true
	}
	return s != nil && s.stops[stopID].parent == want
}

// line resolves a trip's route from the update, or from the schedule when
// the feed leaves route_id out.
func (s *static) line(tu tripUpdate) transit.Line {
	routeID := tu.routeID
	if s != nil && routeID == "" {
		routeID = s.trips[tu.tripID].routeID
	}
	return s.routeLine(routeID)
}

// routeLine is the schedule's line for a route ID, or a bare one.
func (s *static) routeLine(routeID string) transit.Line {
	if s != nil {
		if l, ok := s.routes[routeID]; ok {
			return l
		}
	}
	if routeID == "" {
		// Unscheduled trips may name no route; group them as one line.
		return transit.Line{ID: "unknown", Mode: transit.ModeOther}
	}
	return transit.Line{ID: routeID, ShortName: routeID, Mode: transit.ModeOther}
}

// tripDirection is the schedule's direction_id for tu's trip, or else the
// update's own.
func (s *static) tripDirection(tu tripUpdate) string {
	if s != nil {
		if info, ok := s.trips[tu.tripID]; ok && info.direction >= 0 {
			return directionLabel(int(info.direction))
		}
	}
	return directionLabel(tu.direction)
}

// directionLabel is a direction_id as text: "0", "1", or "" when unknown.
func directionLabel(dir int) string {
	if dir < 0 {
		return ""
	}
	return strconv.Itoa(dir)
}

// headsign uses the schedule's trip headsign, or the name of the last stop
// in the update. Some agencies, like the New York subway, use realtime trip
// IDs that do not match their schedule.
func (s *static) headsign(tu tripUpdate) string {
	if s != nil {
		if h := s.trips[tu.tripID].headsign; h != "" {
			return h
		}
	}
	if len(tu.stops) == 0 {
		return ""
	}
	last := tu.stops[len(tu.stops)-1].stopID
	if s == nil {
		return last
	}
	return s.stopTitle(last)
}

// plannedHeadsign is a timetabled trip's headsign, or the name of its last
// stop, as for realtime trips.
func (s *static) plannedHeadsign(pl planned) string {
	return s.tripHeadsign(pl.trip, pl.last)
}

// tripHeadsign is a trip's headsign, or the name of last, its last stop.
func (s *static) tripHeadsign(trip, last string) string {
	if h := s.trips[trip].headsign; h != "" {
		return h
	}
	return s.stopTitle(last)
}

// stopTitle names a stop after its parent station when it has one.
func (s *static) stopTitle(id string) string {
	info, ok := s.stops[id]
	if !ok {
		return id
	}
	if parent, ok := s.stops[info.parent]; ok && parent.name != "" {
		return parent.name
	}
	return info.name
}
