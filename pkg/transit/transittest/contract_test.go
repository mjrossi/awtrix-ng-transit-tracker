package transittest_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/transittest"
)

// recorder captures failures instead of failing the real test.
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func TestCheckDeparturesCatchesViolations(t *testing.T) {
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	q := transit.Query{StopID: "s1", Now: now, Window: 30 * time.Minute, Limit: 2}
	good := transit.Departure{
		Line: transit.Line{ID: "a", Mode: transit.ModeBus}, StopID: "s1",
		Scheduled: now.Add(5 * time.Minute), Status: transit.StatusScheduled,
	}
	var zero time.Time
	tests := []struct {
		name   string
		mutate func([]transit.Departure) []transit.Departure
		want   string
	}{
		{"valid", func(d []transit.Departure) []transit.Departure { return d }, ""},
		{"over limit", func(d []transit.Departure) []transit.Departure { return append(d, d[1], d[1]) }, "limit"},
		{"wrong stop", func(d []transit.Departure) []transit.Departure { d[0].StopID = "s2"; return d }, "stop"},
		{"no line id", func(d []transit.Departure) []transit.Departure { d[0].Line.ID = ""; return d }, "line ID"},
		{"zero prediction", func(d []transit.Departure) []transit.Departure { d[0].Predicted = &zero; return d }, "predicted"},
		{"bad status", func(d []transit.Departure) []transit.Departure { d[0].Status = "late"; return d }, "status"},
		{"bad color", func(d []transit.Departure) []transit.Departure { d[0].Line.Color = "#fff"; return d }, "color"},
		{"no mode", func(d []transit.Departure) []transit.Departure { d[0].Line.Mode = ""; return d }, "mode"},
		{"in the past", func(d []transit.Departure) []transit.Departure { d[0].Scheduled = now.Add(-time.Minute); return d }, "outside"},
		{"past window", func(d []transit.Departure) []transit.Departure { d[1].Scheduled = now.Add(time.Hour); return d }, "outside"},
		{"unsorted", func(d []transit.Departure) []transit.Departure { d[0], d[1] = d[1], d[0]; return d }, "sorted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			second := good
			second.Scheduled = now.Add(10 * time.Minute)
			deps := tt.mutate([]transit.Departure{good, second})
			r := &recorder{TB: t}
			transittest.CheckDepartures(r, q, deps)
			if tt.want == "" {
				if len(r.errs) > 0 {
					t.Fatalf("unexpected errors: %v", r.errs)
				}
				return
			}
			if !strings.Contains(strings.Join(r.errs, "\n"), tt.want) {
				t.Fatalf("errors %v do not mention %q", r.errs, tt.want)
			}
		})
	}
}
