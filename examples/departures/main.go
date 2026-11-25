// Command departures prints a stop's next departures using only the
// importable packages of Urbanist Timetable:
//
//	go run . nyc-subway 127N
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: departures <system> <stop>")
		os.Exit(2)
	}
	system, stop := os.Args[1], os.Args[2]
	p, err := providers.Open(system, map[string]any{"system": system})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	now := time.Now()
	deps, err := p.Departures(context.Background(), transit.Query{StopID: stop, Now: now, Window: time.Hour})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, d := range deps {
		fmt.Printf("%-6s %-30s %3d min\n", d.Line.ShortName, d.Headsign, d.MinutesUntil(now))
	}
}
