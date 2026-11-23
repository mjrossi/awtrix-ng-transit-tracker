// Package providers registers every built-in provider.
package providers

import (
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/carrismetropolitana"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/demo"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/gtfsrt"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/metrolisboa"
)

// Registry returns a registry with every built-in provider.
func Registry() *transit.Registry {
	r := transit.NewRegistry()
	carrismetropolitana.Register(r)
	demo.Register(r)
	gtfsrt.Register(r)
	metrolisboa.Register(r)
	return r
}
