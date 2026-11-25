package transittest_test

import (
	"context"
	"testing"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/transittest"
)

// every3 departs every three minutes.
type every3 struct{}

func (every3) Departures(ctx context.Context, q transit.Query) ([]transit.Departure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []transit.Departure
	for t := q.Now.Truncate(3 * time.Minute).Add(3 * time.Minute); q.Contains(t); t = t.Add(3 * time.Minute) {
		out = append(out, transit.Departure{Line: transit.Line{ID: "1", Mode: transit.ModeBus}, StopID: q.StopID, Scheduled: t, Status: transit.StatusScheduled})
	}
	return out, nil
}

// A provider's tests call Run with queries against recorded responses. The
// contract checks ordering, the window, concurrency and cancellation.
func ExampleRun() {
	testProvider := func(t *testing.T) {
		now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
		transittest.Run(t, every3{}, []transittest.Case{
			{Name: "next half hour", Query: transit.Query{StopID: "s1", Now: now, Window: 30 * time.Minute}},
			{Name: "zero window", Query: transit.Query{StopID: "s1", Now: now}, WantEmpty: true},
		})
	}
	_ = testProvider // in a _test.go file: func TestProvider(t *testing.T)
}

// The example's provider really passes the contract.
func TestEvery3(t *testing.T) {
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	transittest.Run(t, every3{}, []transittest.Case{
		{Name: "next half hour", Query: transit.Query{StopID: "s1", Now: now, Window: 30 * time.Minute}},
		{Name: "zero window", Query: transit.Query{StopID: "s1", Now: now}, WantEmpty: true},
	})
}
