package gtfsrt

import (
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// Builders for hand-made protobuf messages.
func key(num, wt int) []byte { return binary.AppendUvarint(nil, uint64(num<<3|wt)) }

func varint(num int, v uint64) []byte {
	return append(key(num, wireVarint), binary.AppendUvarint(nil, v)...)
}

func bytesField(num int, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	out := key(num, wireBytes)
	out = binary.AppendUvarint(out, uint64(len(body)))
	return append(out, body...)
}

func TestDecodeRejectsBadHeader(t *testing.T) {
	if _, err := decodeFeed(bytesField(1, []byte{0x0b})); err == nil {
		t.Error("decoded a feed with a malformed header")
	}
}

func TestDecodeHandBuilt(t *testing.T) {
	negDelay := int64(-90)
	msg := append(
		bytesField(1, varint(3, 1700000000)), // header.timestamp
		bytesField(2, // entity
			bytesField(1, []byte("e1")), // id
			bytesField(3, // trip_update
				bytesField(1, bytesField(1, []byte("t1")), varint(4, tripCanceled), bytesField(5, []byte("R"))),
				bytesField(2, // stop_time_update
					bytesField(2, varint(1, uint64(negDelay)), varint(2, 1700000300)),
					bytesField(4, []byte("S1N")),
					varint(5, stopSkipped),
				),
				append(key(9, wireFixed32), 1, 2, 3, 4), // unknown fixed32 field
				append(key(10, wireFixed64), 1, 2, 3, 4, 5, 6, 7, 8),
			),
		)...,
	)
	f, err := decodeFeed(msg)
	if err != nil {
		t.Fatal(err)
	}
	want := feed{trips: []tripUpdate{{
		tripID: "t1", routeID: "R", relation: tripCanceled,
		stops: []stopTimeUpdate{{stopID: "S1N", relation: stopSkipped, arrival: event{time: 1700000300, hasTime: true, delay: -90}}},
	}}}
	if diff := cmp.Diff(want, f, cmp.AllowUnexported(feed{}, tripUpdate{}, stopTimeUpdate{}, event{})); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeRejectsTruncated(t *testing.T) {
	b, err := os.ReadFile("testdata/bart/realtime.pb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeFeed(b[:len(b)/2]); err == nil {
		t.Error("truncated feed decoded without error")
	}
}

func FuzzDecodeFeed(f *testing.F) {
	for _, path := range []string{"testdata/bart/realtime.pb", "testdata/mta/realtime.pb"} {
		b, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b[:min(len(b), 4096)])
	}
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _ = decodeFeed(b) // must not panic or hang
	})
}

func TestDecodeSkipsDeletedEntities(t *testing.T) {
	trip := bytesField(3, bytesField(1, bytesField(1, []byte("t1"))))
	msg := append(
		bytesField(2, bytesField(1, []byte("e1")), varint(2, 1), trip),
		bytesField(2, bytesField(1, []byte("e2")), trip, varint(2, 0))...,
	)
	f, err := decodeFeed(msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.trips) != 1 {
		t.Errorf("got %d trips, want the one not marked is_deleted", len(f.trips))
	}
}

func TestDepartureRules(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	at := func(mins int) event { return event{time: now.Unix() + int64(mins*60), hasTime: true} }
	f := feed{trips: []tripUpdate{
		{tripID: "new", routeID: "R", relation: tripNew, stops: []stopTimeUpdate{{stopID: "S", departure: at(1)}, {stopID: "T", arrival: at(5)}}},
		{tripID: "dup", routeID: "R", relation: tripDuplicated, stops: []stopTimeUpdate{{stopID: "S", departure: at(2)}, {stopID: "T", arrival: at(6)}}},
		// Arrival-only feeds still give departures at intermediate stops.
		{tripID: "arr", routeID: "R", stops: []stopTimeUpdate{{stopID: "S", arrival: at(3)}, {stopID: "T", arrival: at(7)}}},
		{tripID: "cxl", routeID: "R", relation: tripCanceled, stops: []stopTimeUpdate{{stopID: "S", departure: at(8)}, {stopID: "T", arrival: at(9)}}},
		// Trips ending at S arrive there; they do not depart.
		{tripID: "ends", routeID: "R", stops: []stopTimeUpdate{{stopID: "Q", departure: at(0)}, {stopID: "S", arrival: at(4)}}},
		// Feeds that publish only the next stop give no hint of the end.
		{tripID: "next", routeID: "R", stops: []stopTimeUpdate{{stopID: "S", arrival: at(10)}}},
	}}
	deps := departures(nil, f, transit.Query{StopID: "S", Now: now, Window: time.Hour})
	var got []string
	for _, d := range deps {
		got = append(got, string(d.Status))
	}
	want := []string{"added", "added", "scheduled", "canceled", "scheduled"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("statuses mismatch (-want +got):\n%s", diff)
	}
}
