package providers_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

// Open builds a catalog system's provider. Keeping schedules in a cache
// directory makes later runs fast. This example calls the live feeds, so it
// is compiled but not run.
func ExampleOpen() {
	cache := filepath.Join(os.TempDir(), "departures-example")
	p, err := providers.Open("bart", map[string]any{"system": "bart"}, providers.WithScheduleCache(cache))
	if err != nil {
		fmt.Println(err)
		return
	}
	now := time.Now()
	deps, err := p.Departures(context.Background(), transit.Query{StopID: "EMBR", Now: now, Window: 30 * time.Minute})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, d := range deps {
		fmt.Println(d.Line.ShortName, d.Headsign, d.MinutesUntil(now))
	}
}
