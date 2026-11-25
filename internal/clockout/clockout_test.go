package clockout_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
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
	expired []string
	err     error
}

func (p *fakePusher) PushApp(_ context.Context, name string, app awtrix.App) error {
	p.pushed[name] = app
	return p.err
}

func (p *fakePusher) ExpireApp(_ context.Context, name string) error {
	p.expired = append(p.expired, name)
	return p.err
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
		Layout:         render.TC002(snap.Rows(now), now, opts),
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

func TestWithdrawExpiresApp(t *testing.T) {
	p := &fakePusher{}
	if err := clockout.New(p, nil).Withdraw(t.Context(), "metro"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"metro"}, p.expired); diff != "" {
		t.Errorf("expired (-want +got):\n%s", diff)
	}
}

// An app whose lifetime has passed left the clock by itself, so a clock that
// stays unreachable is not asked again.
func TestWithdrawStopsOnceTheAppHasExpired(t *testing.T) {
	offline := errors.New("clock offline")
	tests := map[string]struct {
		lifetime time.Duration
		pushErr  error
		after    time.Duration
		want     int
	}{
		"within the lifetime":  {3 * time.Minute, nil, 2 * time.Minute, 1},
		"past the lifetime":    {3 * time.Minute, nil, 4 * time.Minute, 0},
		"after a failed push":  {3 * time.Minute, offline, 2 * time.Minute, 1},
		"without any lifetime": {0, nil, 24 * time.Hour, 1},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := &fakePusher{pushed: map[string]awtrix.App{}, err: tt.pushErr}
				o := clockout.New(p, map[string]clockout.Board{"metro": {Lifetime: tt.lifetime}})
				if err := o.Publish(t.Context(), snapshot()); !errors.Is(err, tt.pushErr) {
					t.Fatalf("Publish: %v", err)
				}
				time.Sleep(tt.after)
				p.err = offline
				err := o.Withdraw(t.Context(), "metro")
				if len(p.expired) != tt.want || (err != nil) != (tt.want > 0) {
					t.Errorf("requests = %d, err = %v; want %d requests", len(p.expired), err, tt.want)
				}
			})
		})
	}
}

// A failed push may still have reached the clock, so it counts too.
func TestFailedPushRestartsTheLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &fakePusher{pushed: map[string]awtrix.App{}}
		o := clockout.New(p, map[string]clockout.Board{"metro": {Lifetime: 3 * time.Minute}})
		if err := o.Publish(t.Context(), snapshot()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		p.err = errors.New("timeout")
		_ = o.Publish(t.Context(), snapshot())
		time.Sleep(2 * time.Minute)
		_ = o.Withdraw(t.Context(), "metro")
		if len(p.expired) != 1 {
			t.Errorf("requests = %d, want 1", len(p.expired))
		}
	})
}

func TestAppNeedsBoardSettings(t *testing.T) {
	if _, err := clockout.New(nil, nil).App(snapshot()); err == nil {
		t.Error("App for an unknown board: want an error")
	}
}

func TestNoPusher(t *testing.T) {
	o := clockout.New(nil, map[string]clockout.Board{"metro": {}})
	if err := o.Publish(t.Context(), snapshot()); err == nil {
		t.Error("Publish without a pusher: want an error")
	}
	if err := o.Withdraw(t.Context(), "metro"); err == nil {
		t.Error("Withdraw without a pusher: want an error")
	}
}
