// Package scheduler polls providers for each board and pushes the rendered
// result to the clock.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// maxBackoff caps how long a failing source waits between attempts.
const maxBackoff = 10 * time.Minute

// Pusher sends apps to the clock. *awtrix.Client implements it.
type Pusher interface {
	PushApp(ctx context.Context, name string, app awtrix.App) error
	DeleteApp(ctx context.Context, name string) error
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
	Lifetime     time.Duration
	MaxStale     time.Duration
	Duration     time.Duration
	PerRow       int
	// Active reports whether the board should be on the clock at t. Nil
	// means always.
	Active   func(t time.Time) bool
	Location *time.Location
	Render   render.Options
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
	board  Board
	pusher Pusher
	log    *slog.Logger
	now    func() time.Time

	state []sourceState
	shown bool
}

// NewRunner returns a runner. now may be nil for the wall clock.
func NewRunner(b Board, p Pusher, log *slog.Logger, now func() time.Time) *Runner {
	if now == nil {
		now = time.Now
	}
	if b.Location == nil {
		b.Location = time.Local
	}
	return &Runner{
		board:  b,
		pusher: p,
		log:    log.With(slog.String("board", b.Name)),
		now:    now,
		state:  make([]sourceState, len(b.Sources)),
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

// Tick runs one cycle: outside active hours it removes the app; otherwise
// it refreshes due sources, renders and pushes. Errors are logged, not
// returned, because the next tick retries.
func (r *Runner) Tick(ctx context.Context) {
	now := r.now().In(r.board.Location)

	if r.board.Active != nil && !r.board.Active(now) {
		if r.shown {
			if err := r.pusher.DeleteApp(ctx, r.board.Name); err != nil {
				r.log.WarnContext(ctx, "remove app outside active hours", slog.Any("err", err))
				return
			}
			r.shown = false
			r.log.InfoContext(ctx, "outside active hours; app removed")
		}
		return
	}

	rows, ok := r.Rows(ctx, now)
	if !ok {
		// Every source failed with nothing fresh enough to reuse. Pushing
		// "No departures" would be a lie; the app's lifetime removes it.
		return
	}
	app := r.App(rows, now)
	if err := r.pusher.PushApp(ctx, r.board.Name, app); err != nil {
		r.log.WarnContext(ctx, "push app", slog.Any("err", err))
		return
	}
	r.shown = true
}

// Rows refreshes due sources and returns the board's rows. ok is false when
// no source has usable data.
func (r *Runner) Rows(ctx context.Context, now time.Time) (rows []transit.Row, ok bool) {
	var deps []transit.Departure
	for i, src := range r.board.Sources {
		st := &r.state[i]
		if !now.Before(st.nextAttempt) {
			r.refresh(ctx, src, st, now)
		}
		if st.fetchedAt.IsZero() || now.Sub(st.fetchedAt) > r.board.MaxStale {
			continue
		}
		ok = true
		// Filter at render time so cached departures that have left, or are
		// now within the walk time, drop out.
		kept := src.Filter.Apply(now, st.deps)
		if st.failures > 0 {
			// The last refresh failed, so these predictions are as old as
			// the last good fetch. Shown as schedule-only, they keep their
			// best estimate but no longer claim to be live.
			for j, d := range kept {
				kept[j].Scheduled, kept[j].Predicted = d.Time(), nil
			}
		}
		deps = append(deps, kept...)
	}
	return transit.Group(deps, 0, r.board.PerRow), ok
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

// App renders rows into the payload pushed to the clock.
func (r *Runner) App(rows []transit.Row, now time.Time) awtrix.App {
	return awtrix.App{
		DurationMs:     int(r.board.Duration / time.Millisecond),
		LifetimeMs:     int(r.board.Lifetime / time.Millisecond),
		LifetimeExpiry: awtrix.ExpiryRemove,
		Layout:         render.TC002(rows, now, r.board.Render),
	}
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
