package gtfsrt

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// This file decodes the parts of a GTFS-Realtime FeedMessage that departure
// boards need, its TripUpdates, straight from the protobuf wire format.
// Unknown fields, including agency extensions, are skipped.
// Field numbers follow gtfs-realtime.proto:
// https://gtfs.org/documentation/realtime/proto/

// Trip schedule relationships (TripDescriptor.ScheduleRelationship).
const (
	tripAdded      = 1
	tripCanceled   = 3
	tripDuplicated = 6
	tripDeleted    = 7
	// tripNew replaces the deprecated tripAdded in current feeds.
	tripNew = 8
)

// Stop schedule relationships (StopTimeUpdate.ScheduleRelationship).
const (
	stopSkipped = 1
	stopNoData  = 2
)

type feed struct {
	trips []tripUpdate
}

type tripUpdate struct {
	tripID  string
	routeID string
	// startDate is the trip's service day as YYYYMMDD, or empty.
	startDate string
	// startTime and direction identify the trip, with routeID and
	// startDate, in feeds that leave tripID out, such as HSL's.
	startTime string
	direction int // -1 when absent
	relation  int
	stops     []stopTimeUpdate
}

type stopTimeUpdate struct {
	stopID string
	// seq is the stop_sequence, valid when hasSeq.
	seq       uint32
	hasSeq    bool
	relation  int
	arrival   event
	departure event
}

type event struct {
	time     int64
	hasTime  bool
	delay    int32
	hasDelay bool
}

func decodeFeed(b []byte) (feed, error) {
	var f feed
	err := fields(b, func(num, wt int, _ uint64, data []byte) error {
		switch {
		case num == 1 && wt == wireBytes: // header
			// Nothing in it is needed, but a malformed one means the body
			// is not a feed.
			return fields(data, func(int, int, uint64, []byte) error { return nil })
		case num == 2 && wt == wireBytes: // entity
			var (
				tu      tripUpdate
				hasTrip bool
				deleted bool
			)
			err := fields(data, func(num, wt int, v uint64, data []byte) error {
				switch {
				case num == 2 && wt == wireVarint: // is_deleted
					deleted = v != 0
				case num == 3 && wt == wireBytes: // trip_update
					var err error
					tu, err = decodeTripUpdate(data)
					hasTrip = true
					return err
				}
				return nil
			})
			if err != nil {
				return err
			}
			// is_deleted is only set in differential feeds.
			if hasTrip && !deleted {
				f.trips = append(f.trips, tu)
			}
		}
		return nil
	})
	if err != nil {
		return feed{}, fmt.Errorf("decode GTFS-RT feed: %w", err)
	}
	return f, nil
}

func decodeTripUpdate(b []byte) (tripUpdate, error) {
	tu := tripUpdate{direction: -1}
	err := fields(b, func(num, wt int, _ uint64, data []byte) error {
		if wt != wireBytes {
			return nil
		}
		switch num {
		case 1: // trip
			return fields(data, func(num, wt int, v uint64, data []byte) error {
				switch {
				case num == 1 && wt == wireBytes:
					tu.tripID = string(data)
				case num == 2 && wt == wireBytes:
					tu.startTime = string(data)
				case num == 6 && wt == wireVarint:
					tu.direction = int(v)
				case num == 3 && wt == wireBytes:
					tu.startDate = string(data)
				case num == 4 && wt == wireVarint:
					tu.relation = int(v)
				case num == 5 && wt == wireBytes:
					tu.routeID = string(data)
				}
				return nil
			})
		case 2: // stop_time_update
			stu, err := decodeStopTimeUpdate(data)
			if err != nil {
				return err
			}
			tu.stops = append(tu.stops, stu)
		}
		return nil
	})
	return tu, err
}

func decodeStopTimeUpdate(b []byte) (stopTimeUpdate, error) {
	var s stopTimeUpdate
	err := fields(b, func(num, wt int, v uint64, data []byte) error {
		var err error
		switch {
		case num == 1 && wt == wireVarint:
			s.seq, s.hasSeq = uint32(v), true
		case num == 2 && wt == wireBytes:
			s.arrival, err = decodeEvent(data)
		case num == 3 && wt == wireBytes:
			s.departure, err = decodeEvent(data)
		case num == 4 && wt == wireBytes:
			s.stopID = string(data)
		case num == 5 && wt == wireVarint:
			s.relation = int(v)
		}
		return err
	})
	return s, err
}

func decodeEvent(b []byte) (event, error) {
	var e event
	err := fields(b, func(num, wt int, v uint64, _ []byte) error {
		switch {
		case num == 1 && wt == wireVarint:
			// int32 fields encode negative values as 64-bit two's complement.
			e.delay, e.hasDelay = int32(int64(v)), true
		case num == 2 && wt == wireVarint:
			e.time, e.hasTime = int64(v), true
		}
		return nil
	})
	return e, err
}

// Protobuf wire types.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

var errTruncated = errors.New("truncated message")

// fields calls fn for each field in a protobuf message. Varints arrive in
// v; length-delimited fields in data, which aliases b.
func fields(b []byte, fn func(num, wt int, v uint64, data []byte) error) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return errTruncated
		}
		b = b[n:]
		num, wt := int(key>>3), int(key&7)
		if num == 0 {
			return errors.New("field number 0")
		}
		var v uint64
		var data []byte
		switch wt {
		case wireVarint:
			v, n = binary.Uvarint(b)
			if n <= 0 {
				return errTruncated
			}
			b = b[n:]
		case wireFixed64:
			if len(b) < 8 {
				return errTruncated
			}
			b = b[8:]
		case wireFixed32:
			if len(b) < 4 {
				return errTruncated
			}
			b = b[4:]
		case wireBytes:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return errTruncated
			}
			data = b[n : n+int(l)]
			b = b[n+int(l):]
		default:
			// Groups (3, 4) are deprecated and not used by GTFS-RT.
			return fmt.Errorf("unsupported wire type %d", wt)
		}
		if err := fn(num, wt, v, data); err != nil {
			return err
		}
	}
	return nil
}
