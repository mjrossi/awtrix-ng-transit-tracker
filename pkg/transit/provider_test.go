package transit_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit"
)

func TestSortAndLimit(t *testing.T) {
	a, b, c := dep("1", "A", 5, false), dep("2", "B", 2, false), dep("3", "C", 5, false)
	tests := []struct {
		name  string
		in    []transit.Departure
		limit int
		want  []transit.Departure
	}{
		{"empty", nil, 2, nil},
		{"no limit keeps ties in order", []transit.Departure{a, b, c}, 0, []transit.Departure{b, a, c}},
		{"limit", []transit.Departure{a, b, c}, 2, []transit.Departure{b, a}},
		{"limit above length", []transit.Departure{a, b}, 5, []transit.Departure{b, a}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transit.SortAndLimit(tt.in, tt.limit)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("SortAndLimit (-want +got):\n%s", diff)
			}
		})
	}
}
