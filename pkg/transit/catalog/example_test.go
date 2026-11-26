package catalog_test

import (
	"fmt"

	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

func ExampleGet() {
	s, ok := catalog.Get("mta-subway")
	if !ok {
		return
	}
	fmt.Println(s.Title(), "in", s.City, "via", s.Provider)
	// Output: MTA Subway in New York via gtfsrt
}
