package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/scheduler"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/transitland"
)

// The provider's retry interval and the scheduler's backoff must together
// retry a failed fetch before the board's data is too old to show.
func TestOneFailedTransitlandFetchKeepsTheBoard(t *testing.T) {
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	for _, poll := range []time.Duration{config.MinPollInterval, 45 * time.Second, config.DefaultPollInterval, 2 * time.Minute, 3 * time.Minute} {
		t.Run(poll.String(), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls == 2 {
					http.Error(w, "refused", http.StatusBadGateway)
					return
				}
				var rows []string
				for m := range 120 {
					at := start.Add(time.Duration(m) * time.Minute).Format(time.RFC3339)
					rows = append(rows, fmt.Sprintf(`{"departure":{"scheduled_utc":%q,"estimated_utc":%q},"trip":{"trip_id":"%d","route":{"route_id":"1"}}}`, at, at, m))
				}
				_, _ = io.WriteString(w, `{"stops":[{"departures":[`+strings.Join(rows, ",")+`]}]}`)
			}))
			defer srv.Close()
			p, err := transitland.New(transitland.Config{APIKey: "k", BaseURL: srv.URL}, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			b := config.Board{Name: "b", PollInterval: config.Duration(poll), Window: config.Duration(time.Hour), MaxStale: config.Duration(config.DefaultMaxStale)}
			b.Stops = []config.Stop{{Provider: "tl", Stop: "f-x:1"}}
			sb, err := boardFor(&config.Config{}, b, map[string]transit.Provider{"tl": p})
			if err != nil {
				t.Fatal(err)
			}
			r := scheduler.NewRunner(sb, nil, slog.New(slog.DiscardHandler), nil)

			failing := false
			for after := time.Duration(0); after <= 20*time.Minute; after += poll {
				now := start.Add(after)
				snap, ok := r.Snapshot(t.Context(), now)
				if !ok || len(snap.Rows(now)) == 0 {
					t.Fatalf("blank after %s", after)
				}
				failing = failing || snap.Sources[0].Failing
				for _, d := range snap.Rows(now)[0].Departures {
					if snap.Sources[0].Failing && d.Realtime() {
						t.Fatalf("after %s: live while failing", after)
					}
				}
			}
			if !failing {
				t.Error("the failed fetch did not reach the scheduler")
			}
		})
	}
}
