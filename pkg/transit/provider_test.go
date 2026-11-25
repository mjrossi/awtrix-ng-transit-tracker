package transit_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

func TestSortByTime(t *testing.T) {
	a, b, c := dep("1", "A", 5, false), dep("2", "B", 2, false), dep("3", "C", 5, false)
	got := []transit.Departure{a, b, c}
	transit.SortByTime(got)
	// Ties keep their order.
	if diff := cmp.Diff([]transit.Departure{b, a, c}, got); diff != "" {
		t.Errorf("SortByTime (-want +got):\n%s", diff)
	}
}

func TestQueryContains(t *testing.T) {
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	q := transit.Query{Now: now, Window: time.Hour}
	tests := map[string]struct {
		at   time.Time
		want bool
	}{
		"before":  {now.Add(-time.Nanosecond), false},
		"at now":  {now, true},
		"at end":  {now.Add(time.Hour), true},
		"too far": {now.Add(time.Hour + time.Nanosecond), false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := q.Contains(tt.at); got != tt.want {
				t.Errorf("Contains(%s) = %v, want %v", tt.at, got, tt.want)
			}
		})
	}
}
