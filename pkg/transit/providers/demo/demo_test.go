package demo_test

import (
	"testing"
	"time"

	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit/providers/demo"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit/transittest"
)

var now = time.Date(2026, 11, 22, 8, 0, 30, 0, time.UTC)

func newProvider(t *testing.T) *demo.Provider {
	t.Helper()
	p, err := demo.New(demo.Config{Lines: []demo.LineConfig{
		{ID: "azul", Color: "#0072CE", Mode: "metro", Headsign: "Santa Apolónia", Headway: "5m", Offset: "1m", Realtime: true},
		{ID: "verde", Color: "#00A651", Mode: "metro", Headsign: "Telheiras", Headway: "7m", Stops: []string{"baixa-chiado"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContract(t *testing.T) {
	transittest.Run(t, newProvider(t), []transittest.Case{
		{Name: "two lines", Query: transit.Query{StopID: "baixa-chiado", Now: now, Window: 30 * time.Minute}},
		{Name: "line limited to other stops", Query: transit.Query{StopID: "rossio", Now: now, Window: 30 * time.Minute, Limit: 3}},
		{Name: "zero window", Query: transit.Query{StopID: "rossio", Now: time.Date(2026, 11, 22, 8, 2, 30, 0, time.UTC)}, WantEmpty: true},
	})
}

func TestTimetable(t *testing.T) {
	deps, err := newProvider(t).Departures(t.Context(), transit.Query{StopID: "rossio", Now: now, Window: 12 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	// Azul is scheduled at :01, :06 and :11, and every other departure is
	// predicted a minute late. Verde does not serve rossio.
	var got []string
	for _, d := range deps {
		s := d.Line.ID + "@" + d.Time().Format("15:04")
		if d.Realtime() {
			s += "~"
		}
		got = append(got, s)
	}
	want := []string{"azul@08:02~", "azul@08:06", "azul@08:12~"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	tests := map[string]demo.Config{
		"no lines":     {},
		"missing id":   {Lines: []demo.LineConfig{{Headway: "5m"}}},
		"bad headway":  {Lines: []demo.LineConfig{{ID: "a", Headway: "soon"}}},
		"zero headway": {Lines: []demo.LineConfig{{ID: "a", Headway: "0s"}}},
		"bad mode":     {Lines: []demo.LineConfig{{ID: "a", Headway: "5m", Mode: "zeppelin"}}},
	}
	for name, cfg := range tests {
		if _, err := demo.New(cfg); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
}
