package transit_test

import (
	"testing"

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
