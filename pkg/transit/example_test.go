package transit_test

import (
	"fmt"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

func departure(line transit.Line, headsign string, now time.Time, in time.Duration) transit.Departure {
	return transit.Departure{Line: line, Headsign: headsign, Scheduled: now.Add(in), Status: transit.StatusScheduled}
}

// Group turns departures from any number of stops into one row per line and
// destination, soonest first, the way a platform display lists them.
func ExampleGroup() {
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	azul := transit.Line{ID: "azul", ShortName: "Azul", Mode: transit.ModeMetro}
	verde := transit.Line{ID: "verde", ShortName: "Verde", Mode: transit.ModeMetro}
	deps := []transit.Departure{
		departure(azul, "Reboleira", now, 4*time.Minute),
		departure(verde, "Telheiras", now, 2*time.Minute),
		departure(azul, "Reboleira", now, 10*time.Minute),
	}
	for _, row := range transit.Group(deps, 0, 3) {
		fmt.Print(row.Line.ShortName, " to ", row.Headsign, ":")
		for _, d := range row.Departures {
			fmt.Print(" ", d.MinutesUntil(now))
		}
		fmt.Println()
	}
	// Output:
	// Verde to Telheiras: 2
	// Azul to Reboleira: 4 10
}

// A Filter keeps the departures a rider cares about, here one line, leaving
// out those too soon to reach.
func ExampleFilter_Apply() {
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	azul := transit.Line{ID: "azul", ShortName: "Azul"}
	verde := transit.Line{ID: "verde", ShortName: "Verde"}
	deps := []transit.Departure{
		departure(azul, "Reboleira", now, 1*time.Minute),
		departure(verde, "Telheiras", now, 3*time.Minute),
		departure(azul, "Reboleira", now, 6*time.Minute),
	}
	f := transit.Filter{Lines: []string{"azul"}, MinLead: 2 * time.Minute}
	for _, d := range f.Apply(now, deps) {
		fmt.Println(d.Line.ShortName, d.MinutesUntil(now))
	}
	// Output:
	// Azul 6
}
