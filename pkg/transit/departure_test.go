package transit_test

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

func TestModeForRouteType(t *testing.T) {
	tests := map[transit.Mode][]int{
		transit.ModeTram:     {0, 5, 900, 999},
		transit.ModeMetro:    {1, 12, 400, 499},
		transit.ModeRail:     {2, 100, 199},
		transit.ModeBus:      {3, 11, 200, 299, 700, 799},
		transit.ModeFerry:    {4, 1000, 1099, 1200, 1299},
		transit.ModeCableway: {6, 7, 1300, 1499},
		transit.ModeOther:    {-1, 8, 300, 800, 1100, 1500},
	}
	for want, types := range tests {
		for _, rt := range types {
			if got := transit.ModeForRouteType(rt); got != want {
				t.Errorf("route_type %d = %q, want %q", rt, got, want)
			}
		}
	}
}

func TestCompareLines(t *testing.T) {
	lines := []transit.Line{
		{ID: "c", ShortName: "Azul"},
		{ID: "b", ShortName: "10"},
		{ID: "d", ShortName: "C Line"},
		{ID: "a", ShortName: "2"},
		{ID: "e", ShortName: "10"},
	}
	slices.SortFunc(lines, transit.CompareLines)
	var got []string
	for _, l := range lines {
		got = append(got, l.ID)
	}
	if diff := cmp.Diff([]string{"a", "b", "e", "c", "d"}, got); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}
}
