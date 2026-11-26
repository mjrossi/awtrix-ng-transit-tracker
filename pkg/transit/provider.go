package transit

import (
	"context"
	"errors"
	"slices"
	"time"
)

// Query asks a provider for departures at one stop.
type Query struct {
	StopID string
	// Now is the reference time. Providers must use it instead of the wall
	// clock so results are reproducible in tests.
	Now time.Time
	// Window is how far ahead of Now to look.
	Window time.Duration
}

// Contains reports whether t is in [Now, Now+Window].
func (q Query) Contains(t time.Time) bool {
	return !t.Before(q.Now) && !t.After(q.Now.Add(q.Window))
}

// Provider returns departures from one data source.
//
// Implementations must return departures for q.StopID only, sorted by
// Departure.Time ascending, with every Time in [q.Now, q.Now+q.Window], and
// must return promptly with ctx.Err() once ctx is done. Implementations must
// be safe for concurrent use, because boards that name the same provider
// share one instance and poll it from their own goroutines. The transittest
// package checks these rules. A provider that can still answer despite a
// problem returns its departures with a *PartialError, and one that can tell
// a stop does not exist returns ErrUnknownStop.
type Provider interface {
	Departures(ctx context.Context, q Query) ([]Departure, error)
}

// ErrUnknownStop is returned, wrapped, for a stop ID the source does not
// know.
var ErrUnknownStop = errors.New("unknown stop")

// SortByTime sorts deps in place by Departure.Time, keeping ties in input
// order, as the Provider contract requires.
func SortByTime(deps []Departure) {
	slices.SortStableFunc(deps, func(a, b Departure) int { return a.Time().Compare(b.Time()) })
}

// PartialError reports a problem that did not stop a provider from
// answering, such as one of several feeds failing. The departures returned
// with it are valid but may be incomplete or less detailed.
type PartialError struct {
	Err error
}

func (e *PartialError) Error() string { return "partial result: " + e.Err.Error() }

func (e *PartialError) Unwrap() error { return e.Err }

// Stop is a place where vehicles stop.
type Stop struct {
	ID   string
	Name string
}

// StopSearcher is implemented by providers that can look up stops by name.
// It is optional: providers that cannot search still work if the user
// supplies stop IDs.
type StopSearcher interface {
	SearchStops(ctx context.Context, search string) ([]Stop, error)
}

// StopNamer is implemented by providers that already hold stop names, so a
// display can title a stop without a search. ok is false when the name is
// unknown or could not be loaded.
type StopNamer interface {
	StopName(ctx context.Context, id string, now time.Time) (name string, ok bool)
}

// StopLiner is implemented by providers that can tell which lines serve
// each of a list of stops, so stops that share a name can be told apart.
// It can cost a scan of the schedule or a request per stop, so callers ask
// only where it helps. Stops whose lines are unknown are left out.
type StopLiner interface {
	StopLines(ctx context.Context, ids []string, now time.Time) (map[string][]Line, error)
}

// Metered is implemented by providers whose upstream API has a monthly
// request quota, so the configuration can be checked against it.
type Metered interface {
	Quota() Quota
}

// Quota describes a provider's monthly request quota.
type Quota struct {
	// Account identifies what the quota belongs to, such as an API key,
	// without revealing it. Providers with the same account share one quota.
	Account string
	// Limit is the configured number of upstream requests allowed a month,
	// or zero if the configuration does not set it.
	Limit int
	// DefaultLimit applies when no provider with the account sets Limit.
	DefaultLimit int
	// RefreshInterval is the shortest time between two successful upstream
	// requests for one stop.
	RefreshInterval time.Duration
}
