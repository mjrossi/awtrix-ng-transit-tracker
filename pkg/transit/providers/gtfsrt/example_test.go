package gtfsrt_test

import (
	"context"
	"fmt"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/gtfsrt"
)

// Any agency with a GTFS schedule and a GTFS-Realtime TripUpdates feed works
// from its URLs. A nil client gets a default. This example calls the network,
// so it is compiled but not run.
func ExampleNew() {
	p, err := gtfsrt.New(gtfsrt.Config{
		StaticURL:    "https://example.org/gtfs.zip",
		RealtimeURLs: []string{"https://example.org/tripupdates.pb"},
	}, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	deps, err := p.Departures(context.Background(), transit.Query{StopID: "STOP", Now: time.Now(), Window: time.Hour})
	fmt.Println(len(deps), err)
}
