// Package scheduler polls providers for each board and hands the result to
// an Output, such as the clock.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// maxBackoff caps how long a failing source waits between attempts.
const maxBackoff = 10 * time.Minute

// Output receives each board's snapshots.
type Output interface {
	// Publish shows a board's latest snapshot. It is called on every tick
	// that has usable data, so it also restores an output that lost state.
	Publish(ctx context.Context, snap Snapshot) error
	// Withdraw removes a board, such as outside its active hours.
	Withdraw(ctx context.Context, board string) error
}

// Snapshot is one board's state after a tick.
type Snapshot struct {
	Board string
	// At is when the tick ran, in the board's location.
	At time.Time
	// PerRow caps departures per row in Rows; zero means no cap.
	PerRow  int
	Sources []SourceResult
}

// SourceResult is one stop's data and health in a snapshot.
type SourceResult struct {
	Name   string
	StopID string
	// FetchedAt is the last successful fetch, or zero if none.
	FetchedAt time.Time
	// Failing reports that the latest fetch failed, so Departures are as
	// old as FetchedAt.
	Failing bool
	// Partial is the problem the latest fetch reported alongside its
	// departures, or empty for a complete result.
	Partial string
	// Usable reports whether Departures are fresh enough to show: fetched,
	// and no older than the board's max_stale.
	Usable bool
	// Departures are the cached, unfiltered departures; nil when not
	// Usable.
	Departures []transit.Departure
	Filter     transit.Filter
}

// Rows filters every usable source at now and groups the result into rows.
// With dimFailing, departures from a failing source lose their prediction:
// it is as old as the last good fetch, so a display should not show it as
// live, though it stays the best estimate of the time.
func (s Snapshot) Rows(now time.Time, dimFailing bool) []transit.Row {
	var deps []transit.Departure
	for _, src := range s.Sources {
		if !src.Usable {
			continue
		}
		// Filter at display time so cached departures that have left, or
		// are now within the walk time, drop out.
		kept := src.Filter.Apply(now, src.Departures)
		if src.Failing && dimFailing {
			for j, d := range kept {
				kept[j].Scheduled, kept[j].Predicted = d.Time(), nil
			}
		}
		deps = append(deps, kept...)
	}
	return transit.Group(deps, 0, s.PerRow)
}

// Source is one stop on a board.
type Source struct {
	Name     string // for logs, such as "demo:baixa-chiado"
	Provider transit.Provider
	StopID   string
	Filter   transit.Filter
}

// Board is everything one runner needs.
type Board struct {
	Name         string
	Sources      []Source
	PollInterval time.Duration
	Window       time.Duration
	MaxStale     time.Duration
	PerRow       int
	// Active reports whether the board should be shown at t. Nil means
	// always.
	Active   func(t time.Time) bool
	Location *time.Location
}

type sourceState struct {
	deps        []transit.Departure
	fetchedAt   time.Time
	failures    int
	nextAttempt time.Time
	// partial is the last partial-result problem reported, so a lasting
	// one is logged when it starts and ends rather than every poll.
	partial string
}

// Runner drives one board. Create it with NewRunner.
type Runner struct {
	board Board
	out   Output
	log   *slog.Logger
	now   func() time.Time

	state []sourceState
	shown bool
}

// NewRunner returns a runner. now may be nil for the wall clock.
func NewRunner(b Board, out Output, log *slog.Logger, now func() time.Time) *Runner {
	if now == nil {
		now = time.Now
	}
	if b.Location == nil {
		b.Location = time.Local
	}
	return &Runner{
		board: b,
		out:   out,
		log:   log.With(slog.String("board", b.Name)),
		now:   now,
		state: make([]sourceState, len(b.Sources)),
	}
}

// Run ticks every poll interval until ctx is done.
func (r *Runner) Run(ctx context.Context) error {
	t := time.NewTicker(r.board.PollInterval)
	defer t.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Tick runs one cycle: outside active hours it withdraws the board;
// otherwise it refreshes due sources and publishes a snapshot. Errors are
// logged, not returned, because the next tick retries.
func (r *Runner) Tick(ctx context.Context) {
	now := r.now().In(r.board.Location)

	if r.board.Active != nil && !r.board.Active(now) {
		if r.shown {
			if err := r.out.Withdraw(ctx, r.board.Name); err != nil {
				r.log.WarnContext(ctx, "withdraw outside active hours", slog.Any("err", err))
				return
			}
			r.shown = false
			r.log.InfoContext(ctx, "outside active hours; withdrawn")
		}
		return
	}

	snap, ok := r.Snapshot(ctx, now)
	if !ok {
		// Every source failed with nothing fresh enough to reuse. Showing
		// "No departures" would be a lie, so nothing is published; on the
		// clock, the app's lifetime removes it.
		return
	}
	if err := r.out.Publish(ctx, snap); err != nil {
		r.log.WarnContext(ctx, "publish", slog.Any("err", err))
		return
	}
	r.shown = true
}

// Snapshot refreshes due sources and returns the board's state. ok is false
// when no source has usable data.
func (r *Runner) Snapshot(ctx context.Context, now time.Time) (snap Snapshot, ok bool) {
	snap = Snapshot{Board: r.board.Name, At: now, PerRow: r.board.PerRow}
	for i, src := range r.board.Sources {
		st := &r.state[i]
		if !now.Before(st.nextAttempt) {
			r.refresh(ctx, src, st, now)
		}
		res := SourceResult{
			Name:      src.Name,
			StopID:    src.StopID,
			FetchedAt: st.fetchedAt,
			Failing:   st.failures > 0,
			Partial:   st.partial,
			Usable:    !st.fetchedAt.IsZero() && now.Sub(st.fetchedAt) <= r.board.MaxStale,
			Filter:    src.Filter,
		}
		if res.Usable {
			res.Departures, ok = st.deps, true
		}
		snap.Sources = append(snap.Sources, res)
	}
	return snap, ok
}

func (r *Runner) refresh(ctx context.Context, src Source, st *sourceState, now time.Time) {
	deps, err := src.Provider.Departures(ctx, transit.Query{
		StopID: src.StopID,
		Now:    now,
		Window: r.board.Window,
	})
	var partial *transit.PartialError
	if errors.As(err, &partial) {
		r.notePartial(ctx, src, st, partial.Err.Error())
		err = nil
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		st.failures++
		wait := min(r.board.PollInterval<<min(st.failures, 6), maxBackoff)
		st.nextAttempt = now.Add(wait)
		r.log.WarnContext(ctx, "fetch departures",
			slog.String("source", src.Name),
			slog.Int("failures", st.failures),
			slog.Duration("retry_in", wait),
			slog.Any("err", err))
		return
	}
	if partial == nil {
		r.notePartial(ctx, src, st, "")
	}
	if st.failures > 0 {
		r.log.InfoContext(ctx, "source recovered", slog.String("source", src.Name))
	}
	st.deps, st.fetchedAt, st.failures, st.nextAttempt = deps, now, 0, time.Time{}
}

// notePartial logs when a source's partial-result problem appears,
// changes or clears; problem is empty for a complete result.
func (r *Runner) notePartial(ctx context.Context, src Source, st *sourceState, problem string) {
	if problem == st.partial {
		return
	}
	if problem == "" {
		r.log.InfoContext(ctx, "source complete again", slog.String("source", src.Name))
	} else {
		r.log.WarnContext(ctx, "partial departures", slog.String("source", src.Name), slog.String("err", problem))
	}
	st.partial = problem
}

// RunAll runs every runner until ctx is done.
func RunAll(ctx context.Context, runners []*Runner) error {
	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Go(func() { _ = r.Run(ctx) })
	}
	wg.Wait()
	return ctx.Err()
}
