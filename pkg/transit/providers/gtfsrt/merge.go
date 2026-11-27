package gtfsrt

import (
	"cmp"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

const (
	// maxDelay is how late a timetabled trip can run and still be found.
	maxDelay = 3 * time.Hour
	// maxEarly is how early it can run.
	maxEarly = 30 * time.Minute
)

// merge turns timetabled trips into departures, with the prediction of the
// matching trip update where a feed has one. It returns the updates it
// used, which departures then leaves out.
func merge(st *static, plans []planned, feeds []feed, q transit.Query) ([]transit.Departure, map[*tripUpdate]bool) {
	used := map[*tripUpdate]bool{}
	if len(plans) == 0 {
		return nil, used
	}
	byTrip := map[string][]*tripUpdate{}
	byStart := map[tripStart]*tripUpdate{}
	for _, f := range feeds {
		for i := range f.trips {
			tu := &f.trips[i]
			if _, ok := st.trips[tu.tripID]; ok {
				byTrip[tu.tripID] = append(byTrip[tu.tripID], tu)
			} else if k, ok := startOf(tu); ok {
				byStart[k] = tu
			}
		}
	}
	var out []transit.Departure
	for _, pl := range plans {
		d := transit.Departure{
			Line:      st.routeLine(pl.route),
			Headsign:  st.tripHeadsign(pl.trip, pl.last),
			Direction: directionLabel(int(pl.dir)),
			StopID:    q.StopID,
			Scheduled: pl.at,
			Status:    transit.StatusScheduled,
		}
		if tu := matchUpdate(byTrip, byStart, pl); tu != nil {
			used[tu] = true
			d.Direction = cmp.Or(d.Direction, directionLabel(tu.direction))
			switch tu.relation {
			case tripDeleted:
				continue
			case tripCanceled:
				d.Status = transit.StatusCanceled
			default:
				at, skipped, ok := predict(st, tu, pl, q.StopID)
				if skipped {
					continue
				}
				if ok {
					at = at.In(q.Now.Location())
					d.Predicted = &at
				}
			}
		}
		d.Scheduled = d.Scheduled.In(q.Now.Location())
		if q.Contains(d.Time()) {
			out = append(out, d)
		}
	}
	return out, used
}

// tripStart identifies a trip without its ID, as GTFS-RT allows: route,
// direction (-1 when not given), first departure and service day.
type tripStart struct {
	route string
	dir   int
	start int32
	day   string
}

func startOf(tu *tripUpdate) (tripStart, bool) {
	secs, ok := parseClock(tu.startTime)
	if !ok || tu.routeID == "" || tu.startDate == "" {
		return tripStart{}, false
	}
	return tripStart{route: tu.routeID, dir: tu.direction, start: secs, day: tu.startDate}, true
}

// matchUpdate finds the update for a timetabled trip: by trip ID, where an
// update without a start date matches any day, or else by route, direction
// and start. Every run of a frequency trip shares its trip ID, so GTFS-RT
// names the run by its start time too, and an update without one matches
// none of them.
func matchUpdate(byTrip map[string][]*tripUpdate, byStart map[tripStart]*tripUpdate, pl planned) *tripUpdate {
	day := pl.day.String()
	for _, tu := range byTrip[pl.trip] {
		if tu.startDate != "" && tu.startDate != day {
			continue
		}
		if pl.freq {
			if secs, ok := parseClock(tu.startTime); !ok || secs != pl.start {
				continue
			}
		}
		return tu
	}
	if len(byStart) == 0 {
		return nil
	}
	k := tripStart{route: pl.route, dir: int(pl.dir), start: pl.start, day: day}
	dirs := []int{int(pl.dir), -1}
	if pl.dir < 0 {
		// The schedule gives no direction, but the feed may.
		dirs = []int{-1, 0, 1}
	}
	for _, k.dir = range dirs {
		if tu, ok := byStart[k]; ok {
			return tu
		}
	}
	return nil
}

// predict returns a timetabled departure's predicted time from its trip
// update: the update for the stop, or the delay of the latest earlier stop,
// which GTFS-RT says carries forward. Only updates with a stop_sequence can
// be placed before the stop, and an earlier stop with a time but no delay
// carries nothing, since its scheduled time is not kept. skipped is true
// when the update says the trip will not stop there.
func predict(st *static, tu *tripUpdate, pl planned, stop string) (at time.Time, skipped, ok bool) {
	var (
		delay    int32
		hasDelay bool
	)
	for _, stu := range tu.stops {
		here := stu.hasSeq && stu.seq == pl.seq || !stu.hasSeq && st.atStop(stu.stopID, stop)
		if !here {
			if stu.hasSeq && stu.seq < pl.seq && stu.relation != stopSkipped {
				delay, hasDelay = carriedDelay(stu)
				if stu.relation == stopNoData {
					hasDelay = false
				}
			}
			continue
		}
		switch stu.relation {
		case stopSkipped:
			return time.Time{}, true, false
		case stopNoData:
			return time.Time{}, false, false
		}
		ev := stu.departure
		if !ev.hasTime && !ev.hasDelay {
			ev = stu.arrival
		}
		switch {
		case ev.hasTime:
			return time.Unix(ev.time, 0), false, true
		case ev.hasDelay:
			return pl.at.Add(time.Duration(ev.delay) * time.Second), false, true
		}
		return time.Time{}, false, false
	}
	if hasDelay {
		return pl.at.Add(time.Duration(delay) * time.Second), false, true
	}
	return time.Time{}, false, false
}

// carriedDelay is the delay a stop's update passes on to later stops: its
// departure delay, or else its arrival delay.
func carriedDelay(stu stopTimeUpdate) (int32, bool) {
	if stu.departure.hasDelay {
		return stu.departure.delay, true
	}
	return stu.arrival.delay, stu.arrival.hasDelay
}

// duplicatedLines returns the lines on which realtime trips that should be
// in the schedule mostly matched none of its trips, so their timetabled
// trips are probably the same trips again. New York's subway, whose
// realtime trip IDs differ from its schedule's, is one such feed. Trips
// added in realtime are not expected in the schedule and do not count.
func duplicatedLines(merged, live []transit.Departure) map[string]bool {
	planned := map[string]bool{}
	matched := map[string]int{}
	for _, d := range merged {
		planned[d.Line.ID] = true
		if d.Realtime() {
			matched[d.Line.ID]++
		}
	}
	unmatched := map[string]int{}
	for _, d := range live {
		if planned[d.Line.ID] && d.Status != transit.StatusAdded {
			unmatched[d.Line.ID]++
		}
	}
	dup := map[string]bool{}
	for line, n := range unmatched {
		if n >= minUnmatched && n > matched[line] {
			dup[line] = true
		}
	}
	return dup
}

// minUnmatched is the fewest unmatched realtime trips on a line taken as a
// sign of a mismatch rather than a trip the schedule lacks.
const minUnmatched = 2

// departures returns the realtime departures in f at q's stop, leaving out
// the updates in used, which merge has already shown.
func departures(st *static, f feed, q transit.Query, used map[*tripUpdate]bool) []transit.Departure {
	var out []transit.Departure
	for ti := range f.trips {
		tu := &f.trips[ti]
		if tu.relation == tripDeleted || used[tu] {
			continue
		}
		for i, stu := range tu.stops {
			if !st.atStop(stu.stopID, q.StopID) || stu.relation == stopSkipped || stu.relation == stopNoData {
				continue
			}
			ev := stu.departure
			if !ev.hasTime {
				if i == len(tu.stops)-1 && len(tu.stops) > 1 {
					// The trip ends here: an arrival, not a departure. An
					// update listing a single stop, as feeds that publish
					// only the next stop do, says nothing about where the
					// trip ends, so its arrival is shown.
					continue
				}
				// Some feeds give only arrival times.
				ev = stu.arrival
			}
			if !ev.hasTime {
				continue
			}
			t := time.Unix(ev.time, 0).In(q.Now.Location())
			if !q.Contains(t) {
				continue
			}
			out = append(out, transit.Departure{
				Line:      st.line(*tu),
				Headsign:  st.headsign(*tu),
				Direction: st.tripDirection(*tu),
				StopID:    q.StopID,
				Scheduled: t.Add(-time.Duration(ev.delay) * time.Second),
				Predicted: &t,
				Status:    status(tu.relation),
			})
		}
	}
	return out
}

func status(tripRelation int) transit.Status {
	switch tripRelation {
	case tripCanceled:
		return transit.StatusCanceled
	case tripAdded, tripDuplicated, tripNew:
		return transit.StatusAdded
	default:
		return transit.StatusScheduled
	}
}
