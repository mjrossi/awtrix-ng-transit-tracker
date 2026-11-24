package scheduler_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/internal/scheduler"
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

type fakeOutput struct {
	pushes  []scheduler.Snapshot
	deletes int
	err     error
}

func (o *fakeOutput) Publish(_ context.Context, snap scheduler.Snapshot) error {
	if o.err != nil {
		return o.err
	}
	o.pushes = append(o.pushes, snap)
	return nil
}

func (o *fakeOutput) Withdraw(context.Context, string) error {
	o.deletes++
	return nil
}

type harness struct {
	now    time.Time
	prov   *fakeProvider
	out    *fakeOutput
	runner *scheduler.Runner
	logs   *logRecorder
}

// logRecorder keeps the messages of records at info and above in msgs, and
// the rest in debug.
type logRecorder struct {
	msgs  []string
	debug []string
}

func (l *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (l *logRecorder) Handle(_ context.Context, r slog.Record) error {
	if r.Level < slog.LevelInfo {
		l.debug = append(l.debug, r.Message)
	} else {
		l.msgs = append(l.msgs, r.Message)
	}
	return nil
}

func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return l }

func (l *logRecorder) WithGroup(string) slog.Handler { return l }

func newHarness(active func(time.Time) bool) *harness {
	h := &harness{now: start, prov: &fakeProvider{}, out: &fakeOutput{}, logs: &logRecorder{}}
	b := scheduler.Board{
		Name:         "metro",
		Sources:      []scheduler.Source{{Name: "fake:s1", Provider: h.prov, StopID: "s1"}},
		PollInterval: time.Minute,
		Window:       time.Hour,
		MaxStale:     5 * time.Minute,
		PerRow:       3,
		Active:       active,
		Location:     time.UTC,
	}
	h.runner = scheduler.NewRunner(b, h.out, slog.New(h.logs), func() time.Time { return h.now })
	return h
}

// minutes returns the minutes until each departure in a snapshot's first
// row, as the clock would show them.
func minutes(snap scheduler.Snapshot) []int {
	rows := snap.Rows(snap.At)
	if len(rows) == 0 {
		return nil
	}
	var out []int
	for _, d := range rows[0].Departures {
		out = append(out, d.MinutesUntil(snap.At))
	}
	return out
}

func TestPublishesSnapshot(t *testing.T) {
	h := newHarness(nil)
	h.runner.Tick(t.Context())
	if len(h.out.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(h.out.pushes))
	}
	snap := h.out.pushes[0]
	if snap.Board != "metro" || !snap.At.Equal(start) {
		t.Errorf("snapshot board %q at %s", snap.Board, snap.At)
	}
	if diff := cmp.Diff([]int{2, 6, 10}, minutes(snap)); diff != "" {
		t.Errorf("minutes (-want +got):\n%s", diff)
	}
}

func TestReusesLastGoodDataThenGivesUp(t *testing.T) {
	h := newHarness(nil)
	h.runner.Tick(t.Context())

	h.prov.err = errors.New("upstream down")
	h.now = start.Add(3 * time.Minute)
	h.runner.Tick(t.Context())
	if len(h.out.pushes) != 2 {
		t.Fatalf("pushes = %d, want 2 (cached data)", len(h.out.pushes))
	}
	// The 08:02 departure has left; minutes count from the new time.
	if diff := cmp.Diff([]int{3, 7}, minutes(h.out.pushes[1])); diff != "" {
		t.Errorf("minutes (-want +got):\n%s", diff)
	}

	// Past MaxStale, nothing is published.
	h.now = start.Add(6 * time.Minute)
	h.runner.Tick(t.Context())
	if len(h.out.pushes) != 2 {
		t.Fatalf("pushes = %d, want still 2 once data is stale", len(h.out.pushes))
	}
}

func TestFailingSourceIsDimmed(t *testing.T) {
	h := newHarness(nil)
	h.prov.live = true
	snap, _ := h.runner.Snapshot(t.Context(), h.now)
	if d := snap.Rows(h.now)[0].Departures[0]; !d.Realtime() {
		t.Fatalf("fresh departure %+v, want realtime", d)
	}

	h.prov.err = errors.New("upstream down")
	h.now = start.Add(time.Minute)
	snap, ok := h.runner.Snapshot(t.Context(), h.now)
	if !ok {
		t.Fatal("cached data not used")
	}
	src := snap.Sources[0]
	if !src.Failing || !src.Usable || !src.FetchedAt.Equal(start) {
		t.Errorf("source = failing %v usable %v fetched %s, want failing, usable, fetched at start",
			src.Failing, src.Usable, src.FetchedAt)
	}
	dimmed := snap.Rows(h.now)[0].Departures[0]
	if dimmed.Realtime() {
		t.Errorf("dimmed departure %+v, want schedule-only", dimmed)
	}
	// The prediction stays the best estimate of the time.
	if want := start.Add(3 * time.Minute); !dimmed.Time().Equal(want) {
		t.Errorf("time = %s, want %s", dimmed.Time(), want)
	}

	h.prov.err = nil
	h.now = start.Add(3 * time.Minute)
	snap, _ = h.runner.Snapshot(t.Context(), h.now)
	if snap.Sources[0].Failing || !snap.Rows(h.now)[0].Departures[0].Realtime() {
		t.Error("source not live again after recovery")
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
	if h.prov.calls != 3 || len(h.out.pushes) != 1 {
		t.Fatalf("calls = %d pushes = %d, want 3 and 1 after recovery", h.prov.calls, len(h.out.pushes))
	}
}

func TestRemovesAppOutsideActiveHours(t *testing.T) {
	active := true
	h := newHarness(func(time.Time) bool { return active })
	h.runner.Tick(t.Context())
	active = false
	h.runner.Tick(t.Context())
	h.runner.Tick(t.Context())
	if h.out.deletes != 1 {
		t.Errorf("deletes = %d, want 1", h.out.deletes)
	}
	if h.prov.calls != 1 {
		t.Errorf("calls = %d, want no fetching outside active hours", h.prov.calls)
	}
}

func TestPushFailureIsRetriedNextTick(t *testing.T) {
	h := newHarness(nil)
	h.out.err = errors.New("output offline")
	h.runner.Tick(t.Context())
	h.out.err = nil
	h.now = start.Add(time.Minute)
	h.runner.Tick(t.Context())
	if len(h.out.pushes) != 1 {
		t.Errorf("pushes = %d, want 1", len(h.out.pushes))
	}
}

func TestPartialResultsArePushedAndLoggedOnChange(t *testing.T) {
	h := newHarness(nil)
	h.prov.partial = errors.New("one feed down")
	for i := range 3 {
		h.now = start.Add(time.Duration(i) * time.Minute)
		h.runner.Tick(t.Context())
	}
	if len(h.out.pushes) != 3 {
		t.Errorf("pushes = %d, want every partial result pushed", len(h.out.pushes))
	}
	h.prov.partial = nil
	h.now = start.Add(3 * time.Minute)
	h.runner.Tick(t.Context())

	want := []string{"partial departures", "board shown", "source complete again"}
	if !slices.Equal(h.logs.msgs, want) {
		t.Errorf("logs = %q, want %q", h.logs.msgs, want)
	}
}

func TestLogsBoardStateOnChange(t *testing.T) {
	h := newHarness(nil)
	tick := func(after time.Duration) {
		h.now = start.Add(after)
		h.runner.Tick(t.Context())
	}
	tick(0)
	tick(time.Minute)

	h.prov.err = errors.New("upstream down")
	tick(2 * time.Minute)
	tick(20 * time.Minute) // past MaxStale
	tick(21 * time.Minute) // still backing off: not logged again
	h.prov.err = nil
	tick(40 * time.Minute)

	want := []string{
		"board shown",
		"fetch departures",
		"fetch departures",
		"no usable data; board not updated",
		"source recovered",
		"board shown",
	}
	if diff := cmp.Diff(want, h.logs.msgs); diff != "" {
		t.Errorf("logs (-want +got):\n%s", diff)
	}
}

func TestLogsPublishRecovery(t *testing.T) {
	h := newHarness(nil)
	h.runner.Tick(t.Context())
	h.out.err = errors.New("output offline")
	h.now = start.Add(time.Minute)
	h.runner.Tick(t.Context())
	h.out.err = nil
	h.now = start.Add(2 * time.Minute)
	h.runner.Tick(t.Context())

	want := []string{"board shown", "publish", "board shown"}
	if diff := cmp.Diff(want, h.logs.msgs); diff != "" {
		t.Errorf("logs (-want +got):\n%s", diff)
	}
}

func TestLogsStartOutsideActiveHours(t *testing.T) {
	h := newHarness(func(time.Time) bool { return false })
	h.runner.Tick(t.Context())
	h.runner.Tick(t.Context())
	if diff := cmp.Diff([]string{"outside active hours"}, h.logs.msgs); diff != "" {
		t.Errorf("logs (-want +got):\n%s", diff)
	}
	if h.out.deletes != 0 {
		t.Errorf("deletes = %d, want 0 for a board never shown", h.out.deletes)
	}
}

func TestEveryPollIsLoggedAtDebug(t *testing.T) {
	h := newHarness(nil)
	h.runner.Tick(t.Context())
	h.now = start.Add(time.Minute)
	h.runner.Tick(t.Context())
	want := []string{"fetched departures", "published", "fetched departures", "published"}
	if diff := cmp.Diff(want, h.logs.debug); diff != "" {
		t.Errorf("debug logs (-want +got):\n%s", diff)
	}
}

func TestShutdownDuringFetchIsNotLoggedAsNoData(t *testing.T) {
	h := newHarness(nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	h.prov.err = context.Canceled
	h.runner.Tick(ctx)
	if len(h.logs.msgs) != 0 {
		t.Errorf("logs = %q, want none while stopping", h.logs.msgs)
	}
}
