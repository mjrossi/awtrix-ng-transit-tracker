// Package providers registers every built-in provider and opens them by
// name.
package providers

import (
	"log/slog"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/carrismetropolitana"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/demo"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/gtfsrt"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/metrolisboa"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers/transitland"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/registry"
)

type options struct {
	scheduleCache string
	log           *slog.Logger
}

// Option configures the built-in providers.
type Option func(*options)

// WithScheduleCache keeps GTFS schedules in dir between processes; see
// gtfsrt.WithCacheDir. Empty turns it off.
func WithScheduleCache(dir string) Option {
	return func(o *options) { o.scheduleCache = dir }
}

// WithLogger reports slow work, such as GTFS schedule downloads, to log.
func WithLogger(log *slog.Logger) Option {
	return func(o *options) { o.log = log }
}

// Registry returns a registry with every built-in provider.
func Registry(opts ...Option) *registry.Registry {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	r := registry.New()
	carrismetropolitana.Register(r)
	demo.Register(r)
	gtfsOpts := []gtfsrt.Option{gtfsrt.WithCacheDir(o.scheduleCache)}
	if o.log != nil {
		gtfsOpts = append(gtfsOpts, gtfsrt.WithLogger(o.log))
	}
	gtfsrt.Register(r, gtfsOpts...)
	metrolisboa.Register(r)
	transitland.Register(r)
	return r
}

// Open builds the provider that name and settings describe, resolved as
// catalog.Resolve does: a catalog system, an explicit type, or name as the
// type.
func Open(name string, settings map[string]any, opts ...Option) (transit.Provider, error) {
	typ, o, err := catalog.Resolve(name, settings)
	if err != nil {
		return nil, err
	}
	return Registry(opts...).Build(typ, o)
}
