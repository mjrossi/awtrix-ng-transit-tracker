package gtfsrt

import (
	"archive/zip"
	"cmp"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// static is the part of a GTFS schedule feed kept in memory: line names and
// colors, stop names and stations, trips, and the calendar. stop_times.txt
// is far too large to keep (1.1 GB for HSL), so a timetable reads it from
// zr for the stops that are asked for.
type static struct {
	routes map[string]transit.Line
	stops  map[string]stopInfo
	// stopIDs lists stop IDs by stopInfo.idx.
	stopIDs []string
	trips   map[string]tripInfo
	// loc is the agency's time zone, which schedule times count in.
	loc      *time.Location
	services map[string]*service
	freqs    map[string][]frequency
	coverage coverage
	// zr is the schedule zip, or nil once it cannot be read again.
	zr *zip.Reader
	// tt is nil when the schedule's stop times cannot be read.
	tt *timetable
	// file holds zr's data until the provider adopts the schedule.
	file io.Closer
	// cached is true when file is in the cache directory.
	cached bool
	// checked is when the cached schedule was last downloaded or checked
	// with the server, by any process sharing the cache; zero when uncached.
	checked time.Time
	// revalidatable is true when the server can answer that the schedule
	// has not changed, so checking it costs little.
	revalidatable bool
}

// release closes the schedule's file, if it still holds one.
func (s *static) release() {
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

type stopInfo struct {
	name   string
	parent string
	idx    int32
}

type tripInfo struct {
	routeID   string
	headsign  string
	serviceID string
	direction int8 // -1 when unknown
	// idx numbers trips in file order, for per-trip slices during a scan.
	idx int32
}

func parseStatic(r io.ReaderAt, size int64) (*static, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open GTFS zip: %w", err)
	}
	s := &static{
		routes: map[string]transit.Line{}, stops: map[string]stopInfo{}, trips: map[string]tripInfo{},
		loc: time.UTC, services: map[string]*service{}, freqs: map[string][]frequency{}, zr: zr,
	}
	// Large feeds have hundreds of thousands of trips but few distinct
	// routes and headsigns (HSL: 444,000 trips, about 300 headsigns), so
	// those are stored once.
	in := interner{}

	err = readTable(zr, "routes.txt", func(r row) {
		id := in.get(r.get("route_id"))
		short := cmp.Or(r.get("route_short_name"), r.get("route_long_name"))
		s.routes[id] = transit.Line{ID: id, ShortName: strings.Clone(short), Color: transit.NormalizeColor(r.get("route_color")), Mode: modeFor(r.get("route_type"))}
	})
	if err != nil {
		return nil, err
	}
	err = readTable(zr, "stops.txt", func(r row) {
		id := strings.Clone(r.get("stop_id"))
		if _, dup := s.stops[id]; dup {
			return
		}
		s.stops[id] = stopInfo{name: in.get(r.get("stop_name")), parent: in.get(r.get("parent_station")), idx: int32(len(s.stopIDs))}
		s.stopIDs = append(s.stopIDs, id)
	})
	if err != nil {
		return nil, err
	}
	err = readTable(zr, "trips.txt", func(r row) {
		id := strings.Clone(r.get("trip_id"))
		if _, dup := s.trips[id]; dup {
			return
		}
		s.trips[id] = tripInfo{
			routeID: in.get(r.get("route_id")), headsign: in.get(r.get("trip_headsign")),
			serviceID: in.get(r.get("service_id")), direction: direction(r.get("direction_id")), idx: int32(len(s.trips)),
		}
	})
	if err != nil {
		return nil, err
	}

	// The rest is only needed for timetabled trips. GTFS requires agency.txt
	// and one of the calendar files, but a schedule without them still names
	// lines and stops, so they are optional here.
	var zones []string
	err = readOptionalTable(zr, "agency.txt", func(r row) { zones = append(zones, r.get("agency_timezone")) })
	if err != nil {
		return nil, err
	}
	for _, z := range zones {
		if loc, err := time.LoadLocation(z); z != "" && err == nil {
			s.loc = loc
			break
		}
	}
	err = readOptionalTable(zr, "calendar.txt", func(r row) { parseCalendarRow(s.services, in, r) })
	if err != nil {
		return nil, err
	}
	err = readOptionalTable(zr, "calendar_dates.txt", func(r row) { parseCalendarDateRow(s.services, in, r) })
	if err != nil {
		return nil, err
	}
	err = readOptionalTable(zr, "frequencies.txt", func(r row) {
		start, ok1 := parseClock(r.get("start_time"))
		end, ok2 := parseClock(r.get("end_time"))
		headway, err := strconv.Atoi(r.get("headway_secs"))
		if !ok1 || !ok2 || err != nil || headway <= 0 || end <= start {
			return
		}
		id := in.get(r.get("trip_id"))
		s.freqs[id] = append(s.freqs[id], frequency{start: start, end: end, headway: int32(headway)})
	})
	if err != nil {
		return nil, err
	}
	if s.coverage, err = readCoverage(zr, s.services); err != nil {
		return nil, err
	}
	return s, nil
}

// readCoverageOnly reads a schedule's coverage without its routes, stops and
// trips, for judging a cached schedule nobody is using.
func readCoverageOnly(r io.ReaderAt, size int64) (coverage, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return coverage{}, fmt.Errorf("open GTFS zip: %w", err)
	}
	services := map[string]*service{}
	in := interner{}
	if err := readOptionalTable(zr, "calendar.txt", func(r row) { parseCalendarRow(services, in, r) }); err != nil {
		return coverage{}, err
	}
	if err := readOptionalTable(zr, "calendar_dates.txt", func(r row) { parseCalendarDateRow(services, in, r) }); err != nil {
		return coverage{}, err
	}
	return readCoverage(zr, services)
}

// hasTimetable reports whether the schedule can give timetabled trips: it
// has a calendar and stop times.
func (s *static) hasTimetable() bool {
	if s.zr == nil || len(s.services) == 0 {
		return false
	}
	_, err := fs.Stat(s.zr, "stop_times.txt")
	return err == nil
}

func direction(s string) int8 {
	switch s {
	case "0":
		return 0
	case "1":
		return 1
	}
	return -1
}

// modeFor maps a GTFS route_type, as text, to a mode.
func modeFor(routeType string) transit.Mode {
	t, err := strconv.Atoi(strings.TrimSpace(routeType))
	if err != nil {
		return transit.ModeOther
	}
	return transit.ModeForRouteType(t)
}

// interner returns one shared copy of each distinct string. Strings read
// from encoding/csv share one allocation per record, so keeping any field
// would otherwise keep its whole line alive.
type interner map[string]string

func (in interner) get(s string) string {
	if v, ok := in[s]; ok {
		return v
	}
	v := strings.Clone(s)
	in[v] = v
	return v
}

type row struct {
	header map[string]int
	values []string
}

func (r row) get(col string) string {
	i, ok := r.header[col]
	if !ok || i >= len(r.values) {
		return ""
	}
	return strings.TrimSpace(r.values[i])
}

// readOptionalTable is readTable for a table the schedule may leave out.
func readOptionalTable(zr *zip.Reader, name string, fn func(row)) error {
	if _, err := fs.Stat(zr, name); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return readTable(zr, name, fn)
}

// readTable streams a CSV table from the zip. A missing table is an error:
// routes, stops and trips are all required by GTFS.
func readTable(zr *zip.Reader, name string, fn func(row)) error {
	f, err := zr.Open(name)
	if err != nil {
		return fmt.Errorf("GTFS %s: %w", name, err)
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	head, err := cr.Read()
	if err != nil {
		return fmt.Errorf("GTFS %s header: %w", name, err)
	}
	header := make(map[string]int, len(head))
	for i, h := range head {
		// Some producers start files with a UTF-8 byte order mark.
		header[strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF"))] = i
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("GTFS %s: %w", name, err)
		}
		fn(row{header: header, values: rec})
	}
}
