package catalog_test

import (
	"fmt"

	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

func ExampleGet() {
	s, ok := catalog.Get("nyc-subway")
	if !ok {
		return
	}
	fmt.Println(s.Name, "in", s.City, "via", s.Provider)
	// Output: New York City Subway in New York via gtfsrt
}
