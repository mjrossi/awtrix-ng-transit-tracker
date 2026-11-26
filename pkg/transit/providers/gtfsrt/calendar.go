package gtfsrt

import (
	"strconv"
	"strings"
	"time"
)

// date is a calendar date as the number YYYYMMDD, which orders like the
// date and is how GTFS writes dates.
type date int32

func parseDate(s string) (date, bool) {
	if len(s) != 8 {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return date(n), true
}

func dateOf(t time.Time) date {
	y, m, d := t.Date()
	return date(y*10000 + int(m)*100 + d)
}

func (d date) String() string { return strconv.Itoa(int(d)) }

// midnight returns the time GTFS counts a service day's times from in loc:
// noon minus 12 hours, which differs from midnight on days the clocks
// change.
func (d date) midnight(loc *time.Location) time.Time {
	return time.Date(int(d)/10000, time.Month(int(d)/100%100), int(d)%100, 12, 0, 0, 0, loc).Add(-12 * time.Hour)
}

func (d date) weekday() time.Weekday {
	return time.Date(int(d)/10000, time.Month(int(d)/100%100), int(d)%100, 12, 0, 0, 0, time.UTC).Weekday()
}

// service is one service_id's days, from calendar.txt and calendar_dates.txt.
type service struct {
	days       [7]bool // indexed by time.Weekday
	start, end date    // zero when calendar.txt has no row for it
	added      map[date]bool
	removed    map[date]bool
}

func (s *service) runsOn(d date) bool {
	if s.removed[d] {
		return false
	}
	if s.added[d] {
		return true
	}
	return s.start != 0 && d >= s.start && d <= s.end && s.days[d.weekday()]
}

// parseCalendarRow reads one calendar.txt row into services.
func parseCalendarRow(services map[string]*service, in interner, r row) {
	start, ok1 := parseDate(r.get("start_date"))
	end, ok2 := parseDate(r.get("end_date"))
	if !ok1 || !ok2 {
		return
	}
	s := serviceFor(services, in, r.get("service_id"))
	s.start, s.end = start, end
	for i, col := range [7]string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
		s.days[i] = r.get(col) == "1"
	}
}

// parseCalendarDateRow reads one calendar_dates.txt row into services.
func parseCalendarDateRow(services map[string]*service, in interner, r row) {
	d, ok := parseDate(r.get("date"))
	if !ok {
		return
	}
	s := serviceFor(services, in, r.get("service_id"))
	switch r.get("exception_type") {
	case "1":
		if s.added == nil {
			s.added = map[date]bool{}
		}
		s.added[d] = true
	case "2":
		if s.removed == nil {
			s.removed = map[date]bool{}
		}
		s.removed[d] = true
	}
}

func serviceFor(services map[string]*service, in interner, id string) *service {
	id = in.get(id)
	s, ok := services[id]
	if !ok {
		s = &service{}
		services[id] = s
	}
	return s
}

// parseClock parses a GTFS time, H:MM:SS or HH:MM:SS, as seconds since the
// service day's start. Hours may pass 24 for trips after midnight.
func parseClock(s string) (int32, bool) {
	h, rest, ok := strings.Cut(s, ":")
	if !ok {
		return 0, false
	}
	m, sec, ok := strings.Cut(rest, ":")
	if !ok || len(m) != 2 || len(sec) != 2 {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	ss, err3 := strconv.Atoi(sec)
	if err1 != nil || err2 != nil || err3 != nil || hh < 0 || mm < 0 || mm > 59 || ss < 0 || ss > 59 {
		return 0, false
	}
	return int32(hh*3600 + mm*60 + ss), true
}

// frequency is one frequencies.txt row: the trip runs every headway from
// start until end, with the times of its stop_times shifted to each start.
type frequency struct {
	start, end, headway int32
}
