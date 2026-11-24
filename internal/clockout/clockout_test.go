package clockout_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/internal/clockout"
	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/internal/scheduler"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

var now = time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)

type fakePusher struct {
	pushed  map[string]awtrix.App
	deleted []string
}

func (p *fakePusher) PushApp(_ context.Context, name string, app awtrix.App) error {
	p.pushed[name] = app
	return nil
}

func (p *fakePusher) DeleteApp(_ context.Context, name string) error {
	p.deleted = append(p.deleted, name)
	return nil
}

// snapshot has one source whose last fetch failed, with one predicted
// departure.
func snapshot() scheduler.Snapshot {
	pred := now.Add(4 * time.Minute)
	return scheduler.Snapshot{
		Board: "metro",
		At:    now,
		Sources: []scheduler.SourceResult{{
			Name: "demo:s1", StopID: "s1", FetchedAt: now.Add(-time.Minute),
			Failing: true, Usable: true,
			Departures: []transit.Departure{{
				Line:     transit.Line{ID: "azul", ShortName: "Azul", Mode: transit.ModeMetro},
				Headsign: "Santa Apolónia", StopID: "s1",
				Scheduled: now.Add(3 * time.Minute), Predicted: &pred,
				Status: transit.StatusScheduled,
			}},
		}},
	}
}

func TestPublishPushesRenderedApp(t *testing.T) {
	p := &fakePusher{pushed: map[string]awtrix.App{}}
	opts := render.Options{Labels: map[string]string{"Santa Apolónia": "Sta Apolónia"}}
	out := clockout.New(p, map[string]clockout.Board{
		"metro": {Render: opts, Lifetime: 3 * time.Minute, Duration: 10 * time.Second},
	})
	snap := snapshot()
	if err := out.Publish(t.Context(), snap); err != nil {
		t.Fatal(err)
	}
	got, ok := p.pushed["metro"]
	if !ok {
		t.Fatal("nothing pushed for metro")
	}
	// A failing source is shown schedule-only, as the clock dims it.
	want := awtrix.App{
		DurationMs:     10000,
		LifetimeMs:     180000,
		LifetimeExpiry: awtrix.ExpiryRemove,
		Layout:         render.TC002(snap.Rows(now, true), now, opts),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("pushed app (-want +got):\n%s", diff)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), render.AccentColor) {
		t.Error("a failing source's departure has a live marker")
	}
}

func TestWithdrawDeletesApp(t *testing.T) {
	p := &fakePusher{}
	if err := clockout.New(p, nil).Withdraw(t.Context(), "metro"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"metro"}, p.deleted); diff != "" {
		t.Errorf("deleted (-want +got):\n%s", diff)
	}
}

func TestAppNeedsBoardSettings(t *testing.T) {
	if _, err := clockout.New(nil, nil).App(snapshot()); err == nil {
		t.Error("App for an unknown board: want an error")
	}
}
