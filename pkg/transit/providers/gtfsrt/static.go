package gtfsrt

import (
	"archive/zip"
	"cmp"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// static is the part of a GTFS schedule feed needed to describe realtime
// departures: line names and colors, stop names and stations, and trip
// headsigns. stop_times.txt is not read, so trips without realtime data
// are not shown.
type static struct {
	routes map[string]transit.Line
	stops  map[string]stopInfo
	trips  map[string]tripInfo
}

type stopInfo struct {
	name   string
	parent string
}

type tripInfo struct {
	routeID  string
	headsign string
}

func parseStatic(r io.ReaderAt, size int64) (*static, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open GTFS zip: %w", err)
	}
	s := &static{routes: map[string]transit.Line{}, stops: map[string]stopInfo{}, trips: map[string]tripInfo{}}
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
		s.stops[strings.Clone(r.get("stop_id"))] = stopInfo{name: in.get(r.get("stop_name")), parent: in.get(r.get("parent_station"))}
	})
	if err != nil {
		return nil, err
	}
	err = readTable(zr, "trips.txt", func(r row) {
		s.trips[strings.Clone(r.get("trip_id"))] = tripInfo{routeID: in.get(r.get("route_id")), headsign: in.get(r.get("trip_headsign"))}
	})
	if err != nil {
		return nil, err
	}
	return s, nil
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
