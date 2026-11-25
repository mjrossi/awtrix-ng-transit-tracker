// Package registry builds providers by name from their configuration, for
// programs that, like this one, read providers from a file.
package registry

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

// Options decodes a provider's configuration block into v.
type Options interface {
	Decode(v any) error
}

// Factory builds a provider from its configuration block.
type Factory func(opts Options) (transit.Provider, error)

// ErrUnknownProvider is returned when no factory is registered under a name.
var ErrUnknownProvider = errors.New("unknown provider")

// Registry maps provider names, as used in configuration, to factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// Register adds a factory. Registering the same name twice panics, since
// that is a programming error.
func (r *Registry) Register(name string, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.factories[name]; dup {
		panic(fmt.Sprintf("registry: provider %q registered twice", name))
	}
	r.factories[name] = f
}

// RegisterConfig registers a factory that decodes the configuration block
// into a C and passes it to build.
func RegisterConfig[C any, P transit.Provider](r *Registry, name string, build func(C) (P, error)) {
	r.Register(name, func(opts Options) (transit.Provider, error) {
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

// Build builds the provider registered under name.
func (r *Registry) Build(name string, opts Options) (transit.Provider, error) {
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
