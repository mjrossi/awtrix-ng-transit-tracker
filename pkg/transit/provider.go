package transit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
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
	// Limit caps the number of departures returned. Zero means no limit.
	Limit int
}

// Provider returns departures from one data source.
//
// Implementations must return departures for q.StopID only, sorted by
// Departure.Time ascending, with every Time in [q.Now, q.Now+q.Window], at
// most q.Limit of them, and must return promptly with ctx.Err() once ctx is
// done. Implementations must be safe for concurrent use, because boards
// that name the same provider share one instance and poll it from their own
// goroutines. The transittest package checks these rules. A provider that can
// still answer despite a problem returns its departures with a
// *PartialError.
type Provider interface {
	Departures(ctx context.Context, q Query) ([]Departure, error)
}

// SortAndLimit sorts deps by Departure.Time, keeping ties in input order,
// and returns at most limit of them (zero means no limit), as the Provider
// contract requires. It sorts deps in place.
func SortAndLimit(deps []Departure, limit int) []Departure {
	slices.SortStableFunc(deps, func(a, b Departure) int { return a.Time().Compare(b.Time()) })
	if limit > 0 && len(deps) > limit {
		return deps[:limit]
	}
	return deps
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
	// Modes served at the stop, if the provider knows them.
	Modes []Mode
}

// StopSearcher is implemented by providers that can look up stops by name.
// It is optional: providers that cannot search still work if the user
// supplies stop IDs.
type StopSearcher interface {
	SearchStops(ctx context.Context, search string) ([]Stop, error)
}

// Metered is implemented by providers whose upstream API has a monthly
// request quota, so the configuration can be checked against it.
type Metered interface {
	// RefreshInterval is the shortest time between two upstream requests
	// for one stop.
	RefreshInterval() time.Duration
	// MonthlyQuota is the number of upstream requests allowed a month.
	MonthlyQuota() int
}

// Options decodes a provider's configuration block into v.
type Options interface {
	Decode(v any) error
}

// Factory builds a provider from its configuration block.
type Factory func(opts Options) (Provider, error)

// ErrUnknownProvider is returned when no factory is registered under a name.
var ErrUnknownProvider = errors.New("unknown provider")

// Registry maps provider names, as used in configuration, to factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// Register adds a factory. Registering the same name twice panics, since
// that is a programming error.
func (r *Registry) Register(name string, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.factories[name]; dup {
		panic(fmt.Sprintf("transit: provider %q registered twice", name))
	}
	r.factories[name] = f
}

// RegisterConfig registers a factory that decodes the configuration block
// into a C and passes it to build.
func RegisterConfig[C any, P Provider](r *Registry, name string, build func(C) (P, error)) {
	r.Register(name, func(opts Options) (Provider, error) {
		var cfg C
		if err := opts.Decode(&cfg); err != nil {
			return nil, err
		}
		p, err := build(cfg)
		if err != nil {
			// Returning p would wrap a nil pointer in a non-nil interface.
			return nil, err
		}
		return p, nil
	})
}

// New builds the provider registered under name.
func (r *Registry) New(name string, opts Options) (Provider, error) {
	r.mu.RLock()
	f, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownProvider, name)
	}
	p, err := f(opts)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", name, err)
	}
	return p, nil
}

// Names lists registered provider names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for n := range r.factories {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
