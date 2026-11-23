package scheduler_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/scheduler"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

var start = time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)

type fakeProvider struct {
	err error
	// partial, if set, comes back as a *transit.PartialError with the
	// departures.
	partial error
	// live gives every departure a prediction.
	live  bool
	calls int
}

func (p *fakeProvider) Departures(_ context.Context, q transit.Query) ([]transit.Departure, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	var err error
	if p.partial != nil {
		err = &transit.PartialError{Err: p.partial}
	}
	line := transit.Line{ID: "azul", ShortName: "Azul", Mode: transit.ModeMetro}
	var out []transit.Departure
	for _, m := range []int{2, 6, 10} {
		d := transit.Departure{
			Line: line, Headsign: "Santa Apolónia", StopID: q.StopID,
			Scheduled: start.Add(time.Duration(m) * time.Minute), Status: transit.StatusScheduled,
		}
		if p.live {
			late := d.Scheduled.Add(time.Minute)
			d.Predicted = &late
		}
		out = append(out, d)
	}
	return out, err
}

type fakePusher struct {
	pushes  []awtrix.App
	deletes int
	err     error
}

func (p *fakePusher) PushApp(_ context.Context, _ string, app awtrix.App) error {
	if p.err != nil {
		return p.err
	}
	p.pushes = append(p.pushes, app)
	return nil
}

func (p *fakePusher) DeleteApp(context.Context, string) error {
	p.deletes++
	return nil
}

type harness struct {
	now    time.Time
	prov   *fakeProvider
	pusher *fakePusher
	runner *scheduler.Runner
	logs   *logRecorder
}

// logRecorder keeps each record's message.
type logRecorder struct {
	msgs []string
}

func (l *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (l *logRecorder) Handle(_ context.Context, r slog.Record) error {
	l.msgs = append(l.msgs, r.Message)
	return nil
}

func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return l }

func (l *logRecorder) WithGroup(string) slog.Handler { return l }

func newHarness(active func(time.Time) bool) *harness {
	h := &harness{now: start, prov: &fakeProvider{}, pusher: &fakePusher{}, logs: &logRecorder{}}
	b := scheduler.Board{
		Name:         "metro",
		Sources:      []scheduler.Source{{Name: "fake:s1", Provider: h.prov, StopID: "s1"}},
		PollInterval: time.Minute,
		Window:       time.Hour,
		Lifetime:     3 * time.Minute,
		MaxStale:     5 * time.Minute,
		PerRow:       3,
		Active:       active,
		Location:     time.UTC,
	}
	h.runner = scheduler.NewRunner(b, h.pusher, slog.New(h.logs), func() time.Time { return h.now })
	return h
}

// minutes returns the minute numbers shown by a departure-layout push.
func minutes(app awtrix.App) []string {
	var out []string
	for _, r := range app.Layout.Regions {
		if r.ID == "min" {
			for _, f := range r.Text.Fragments {
				if strings.TrimSpace(f.Text) != "" {
					out = append(out, f.Text)
				}
			}
		}
	}
	return out
}

func TestPushesWithLifetime(t *testing.T) {
	h := newHarness(nil)
	h.runner.Tick(t.Context())
	if len(h.pusher.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(h.pusher.pushes))
	}
	app := h.pusher.pushes[0]
	if app.LifetimeMs != 180000 || app.LifetimeExpiry != awtrix.ExpiryRemove {
		t.Errorf("lifetime %d %q", app.LifetimeMs, app.LifetimeExpiry)
	}
	if got := minutes(app); len(got) != 3 || got[0] != "2" {
		t.Errorf("minutes = %v, want [2 6 10]", got)
	}
}

func TestReusesLastGoodDataThenGivesUp(t *testing.T) {
	h := newHarness(nil)
	h.runner.Tick(t.Context())

	h.prov.err = errors.New("upstream down")
	h.now = start.Add(3 * time.Minute)
	h.runner.Tick(t.Context())
	if len(h.pusher.pushes) != 2 {
		t.Fatalf("pushes = %d, want 2 (cached data)", len(h.pusher.pushes))
	}
	// The 08:02 departure has left; minutes count from the new time.
	if got := minutes(h.pusher.pushes[1]); len(got) != 2 || got[0] != "3" || got[1] != "7" {
		t.Errorf("minutes = %v, want [3 7]", got)
	}

	// Past MaxStale, nothing is pushed and the clock's lifetime removes the app.
	h.now = start.Add(6 * time.Minute)
	h.runner.Tick(t.Context())
	if len(h.pusher.pushes) != 2 {
		t.Fatalf("pushes = %d, want still 2 once data is stale", len(h.pusher.pushes))
	}
}

func TestLastGoodDataIsNotShownAsLive(t *testing.T) {
	h := newHarness(nil)
	h.prov.live = true
	rows, _ := h.runner.Rows(t.Context(), h.now)
	if d := rows[0].Departures[0]; !d.Realtime() {
		t.Fatalf("fresh departure %+v, want realtime", d)
	}

	h.prov.err = errors.New("upstream down")
	h.now = start.Add(time.Minute)
	rows, ok := h.runner.Rows(t.Context(), h.now)
	if !ok {
		t.Fatal("cached data not used")
	}
	d := rows[0].Departures[0]
	if d.Realtime() {
		t.Errorf("cached departure %+v, want schedule-only", d)
	}
	// The prediction stays the best estimate of the time.
	if want := start.Add(3 * time.Minute); !d.Time().Equal(want) {
		t.Errorf("time = %s, want %s", d.Time(), want)
	}

	h.prov.err = nil
	h.now = start.Add(3 * time.Minute)
	rows, _ = h.runner.Rows(t.Context(), h.now)
	if !rows[0].Departures[0].Realtime() {
		t.Error("departure not realtime again after recovery")
	}
}

func TestBacksOffFailingSource(t *testing.T) {
	h := newHarness(nil)
	h.prov.err = errors.New("upstream down")
	h.runner.Tick(t.Context()) // fails, retry in 2m
	h.now = start.Add(time.Minute)
	h.runner.Tick(t.Context()) // skipped
	if h.prov.calls != 1 {
		t.Fatalf("calls = %d, want 1 during backoff", h.prov.calls)
	}
	h.now = start.Add(2 * time.Minute)
	h.runner.Tick(t.Context()) // retried, fails, retry in 4m
	h.now = start.Add(5 * time.Minute)
	h.runner.Tick(t.Context()) // skipped
	if h.prov.calls != 2 {
		t.Fatalf("calls = %d, want 2", h.prov.calls)
	}
	h.prov.err = nil
	h.now = start.Add(6 * time.Minute)
	h.runner.Tick(t.Context())
	if h.prov.calls != 3 || len(h.pusher.pushes) != 1 {
		t.Fatalf("calls = %d pushes = %d, want 3 and 1 after recovery", h.prov.calls, len(h.pusher.pushes))
	}
}

func TestRemovesAppOutsideActiveHours(t *testing.T) {
	active := true
	h := newHarness(func(time.Time) bool { return active })
	h.runner.Tick(t.Context())
	active = false
	h.runner.Tick(t.Context())
	h.runner.Tick(t.Context())
	if h.pusher.deletes != 1 {
		t.Errorf("deletes = %d, want 1", h.pusher.deletes)
	}
	if h.prov.calls != 1 {
		t.Errorf("calls = %d, want no fetching outside active hours", h.prov.calls)
	}
}

func TestPushFailureIsRetriedNextTick(t *testing.T) {
	h := newHarness(nil)
	h.pusher.err = errors.New("clock offline")
	h.runner.Tick(t.Context())
	h.pusher.err = nil
	h.now = start.Add(time.Minute)
	h.runner.Tick(t.Context())
	if len(h.pusher.pushes) != 1 {
		t.Errorf("pushes = %d, want 1", len(h.pusher.pushes))
	}
}

func TestPartialResultsArePushedAndLoggedOnChange(t *testing.T) {
	h := newHarness(nil)
	h.prov.partial = errors.New("one feed down")
	for i := range 3 {
		h.now = start.Add(time.Duration(i) * time.Minute)
		h.runner.Tick(t.Context())
	}
	if len(h.pusher.pushes) != 3 {
		t.Errorf("pushes = %d, want every partial result pushed", len(h.pusher.pushes))
	}
	h.prov.partial = nil
	h.now = start.Add(3 * time.Minute)
	h.runner.Tick(t.Context())

	want := []string{"partial departures", "source complete again"}
	if !slices.Equal(h.logs.msgs, want) {
		t.Errorf("logs = %q, want %q", h.logs.msgs, want)
	}
}
