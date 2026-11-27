package gtfsrt

import (
	"archive/zip"
	"errors"
	"fmt"
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

// readServices reads calendar.txt and calendar_dates.txt, either of which
// may be missing, into services.
func readServices(zr *zip.Reader, services map[string]*service, in interner) error {
	if err := readOptionalTable(zr, "calendar.txt", func(r row) { parseCalendarRow(services, in, r) }); err != nil {
		return err
	}
	return readOptionalTable(zr, "calendar_dates.txt", func(r row) { parseCalendarDateRow(services, in, r) })
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

// coverage is the period a schedule is published for. A zero date is
// unknown.
type coverage struct {
	start, end date
	version    string
}

// readCoverage takes the period from services, the calendar, since no trip
// runs outside it, and from feed_info.txt only for dates the calendar leaves
// unknown. Agencies do not always update feed_info.txt: BART's said its
// schedule ended in August while its calendar ran into the next year.
func readCoverage(zr *zip.Reader, services map[string]*service) (coverage, error) {
	var c coverage
	widen := func(d date) {
		if c.start == 0 || d < c.start {
			c.start = d
		}
		c.end = max(c.end, d)
	}
	for _, s := range services {
		if s.start != 0 {
			widen(s.start)
			widen(s.end)
		}
		for d := range s.added {
			widen(d)
		}
	}
	read := false
	err := readOptionalTable(zr, "feed_info.txt", func(r row) {
		if read {
			return
		}
		read = true
		if c.start == 0 {
			c.start, _ = parseDate(r.get("feed_start_date"))
		}
		if c.end == 0 {
			c.end, _ = parseDate(r.get("feed_end_date"))
		}
		c.version = strings.Clone(r.get("feed_version"))
	})
	if err != nil {
		return coverage{}, err
	}
	return c, nil
}

// startsAt is when the coverage begins in loc, or zero if unknown.
func (c coverage) startsAt(loc *time.Location) time.Time {
	if c.start == 0 {
		return time.Time{}
	}
	return c.start.midnight(loc)
}

// endsAt is when the coverage's last day ends in loc, or zero if unknown.
func (c coverage) endsAt(loc *time.Location) time.Time {
	if c.end == 0 {
		return time.Time{}
	}
	return c.end.next().midnight(loc)
}

// next is the day after d.
func (d date) next() date {
	t := time.Date(int(d)/10000, time.Month(int(d)/100%100), int(d)%100+1, 12, 0, 0, 0, time.UTC)
	return dateOf(t)
}

// outOfRange returns errScheduleEnded or errScheduleNotStarted, with the
// date, when now is outside the coverage.
func (c coverage) outOfRange(now time.Time, loc *time.Location) error {
	if end := c.endsAt(loc); !end.IsZero() && !now.Before(end) {
		return fmt.Errorf("%w on %s; the departures and line names it gives may be out of date", errScheduleEnded, c.end.iso())
	}
	if start := c.startsAt(loc); !start.IsZero() && now.Before(start) {
		return fmt.Errorf("%w on %s; until then, the departures and line names it gives may be out of date", errScheduleNotStarted, c.start.iso())
	}
	return nil
}

var (
	errScheduleEnded      = errors.New("the schedule ended")
	errScheduleNotStarted = errors.New("the schedule starts")
)

// iso writes d as YYYY-MM-DD.
func (d date) iso() string {
	return fmt.Sprintf("%04d-%02d-%02d", int(d)/10000, int(d)/100%100, int(d)%100)
}
