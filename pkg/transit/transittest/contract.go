// Package transittest is the contract suite every transit.Provider must
// pass. Provider packages call Run from their own tests, against recorded
// responses, so the rules in transit.Provider's doc comment are enforced the
// same way for every data source.
package transittest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// Case is one query the provider under test can answer.
type Case struct {
	Name  string
	Query transit.Query
	// WantEmpty marks a query that should return no departures, such as a
	// stop with no service. Otherwise at least one departure is required.
	WantEmpty bool
}

// Run checks p against every case, then checks limit, concurrent use and
// cancellation handling using the first non-empty case.
func Run(t *testing.T, p transit.Provider, cases []Case) {
	t.Helper()
	if len(cases) == 0 {
		t.Fatal("transittest: no cases")
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			deps, err := p.Departures(t.Context(), c.Query)
			if err != nil {
				t.Fatalf("Departures: %v", err)
			}
			if c.WantEmpty {
				if len(deps) != 0 {
					t.Fatalf("got %d departures, want none", len(deps))
				}
				return
			}
			if len(deps) == 0 {
				t.Fatal("got no departures; mark the case WantEmpty if that is expected")
			}
			CheckDepartures(t, c.Query, deps)
		})
	}

	var base *Case
	for i := range cases {
		if !cases[i].WantEmpty {
			base = &cases[i]
			break
		}
	}
	if base == nil {
		return
	}

	t.Run("respects limit", func(t *testing.T) {
		q := base.Query
		q.Limit = 1
		deps, err := p.Departures(t.Context(), q)
		if err != nil {
			t.Fatalf("Departures: %v", err)
		}
		if len(deps) != 1 {
			t.Fatalf("got %d departures with Limit 1, want 1", len(deps))
		}
	})

	// Boards share one provider and poll it from their own goroutines, so
	// this only catches data races under the race detector.
	t.Run("safe for concurrent use", func(t *testing.T) {
		const callers = 8
		var wg sync.WaitGroup
		for range callers {
			wg.Go(func() {
				deps, err := p.Departures(t.Context(), base.Query)
				if err != nil {
					t.Errorf("Departures: %v", err)
					return
				}
				CheckDepartures(t, base.Query, deps)
			})
		}
		wg.Wait()
	})

	t.Run("honors canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := p.Departures(ctx, base.Query)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got error %v, want context.Canceled", err)
		}
	})
}

// CheckDepartures reports every way deps breaks the Provider rules for q.
func CheckDepartures(t testing.TB, q transit.Query, deps []transit.Departure) {
	t.Helper()
	if q.Limit > 0 && len(deps) > q.Limit {
		t.Errorf("got %d departures, limit is %d", len(deps), q.Limit)
	}
	end := q.Now.Add(q.Window)
	for i, d := range deps {
		if d.StopID != q.StopID {
			t.Errorf("departure %d: stop %q, want %q", i, d.StopID, q.StopID)
		}
		if d.Line.ID == "" {
			t.Errorf("departure %d: empty line ID", i)
		}
		if d.Line.Color != "" && transit.NormalizeColor(d.Line.Color) != d.Line.Color {
			t.Errorf("departure %d: line color %q is not #RRGGBB", i, d.Line.Color)
		}
		if d.Scheduled.IsZero() {
			t.Errorf("departure %d: zero scheduled time", i)
		}
		if d.Predicted != nil && d.Predicted.IsZero() {
			t.Errorf("departure %d: predicted time set but zero; use nil for no prediction", i)
		}
		switch d.Status {
		case transit.StatusScheduled, transit.StatusCanceled, transit.StatusAdded:
		default:
			t.Errorf("departure %d: invalid status %q", i, d.Status)
		}
		if _, err := transit.ParseMode(string(d.Line.Mode)); err != nil || d.Line.Mode == "" {
			t.Errorf("departure %d: invalid mode %q", i, d.Line.Mode)
		}
		if tm := d.Time(); tm.Before(q.Now) || tm.After(end) {
			t.Errorf("departure %d: time %s outside [%s, %s]", i, tm, q.Now, end)
		}
		if i > 0 && d.Time().Before(deps[i-1].Time()) {
			t.Errorf("departure %d: not sorted (%s before %s)", i, d.Time(), deps[i-1].Time())
		}
	}
}
