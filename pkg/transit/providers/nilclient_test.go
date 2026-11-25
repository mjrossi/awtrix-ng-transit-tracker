package providers_test

import (
	"testing"

	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/carrismetropolitana"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/gtfsrt"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/metrolisboa"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/transitland"
)

// Importers can build every HTTP provider without an *http.Client.
func TestNilClient(t *testing.T) {
	tests := map[string]func() error{
		"carrismetropolitana": func() error { _, err := carrismetropolitana.New(carrismetropolitana.Config{}, nil); return err },
		"gtfsrt": func() error {
			_, err := gtfsrt.New(gtfsrt.Config{RealtimeURLs: []string{"https://example.org/tu.pb"}}, nil)
			return err
		},
		"metrolisboa": func() error {
			_, err := metrolisboa.New(metrolisboa.Config{ClientID: "id", ClientSecret: "secret"}, nil)
			return err
		},
		"transitland": func() error { _, err := transitland.New(transitland.Config{APIKey: "k"}, nil); return err },
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			if err := build(); err != nil {
				t.Errorf("New with a nil client: %v", err)
			}
		})
	}
}
