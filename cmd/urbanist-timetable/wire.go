package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/internal/scheduler"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

// buildProviders constructs each configured provider once, so boards that
// share a provider share its caches and connections.
func buildProviders(cfg *config.Config) (map[string]transit.Provider, error) {
	reg := providers.Registry()
	out := make(map[string]transit.Provider, len(cfg.Providers))
	for name := range cfg.Providers {
		typ, opts, err := cfg.Provider(name)
		if err != nil {
			return nil, fmt.Errorf("providers.%s: %w", name, err)
		}
		p, err := reg.New(typ, opts)
		if err != nil {
			return nil, fmt.Errorf("providers.%s: %w", name, err)
		}
		out[name] = p
	}
	return out, nil
}

func newClock(cfg *config.Config) (*awtrix.Client, error) {
	var opts []awtrix.Option
	if cfg.Clock.Username != "" {
		opts = append(opts, awtrix.WithBasicAuth(cfg.Clock.Username, cfg.Clock.Password))
	}
	return awtrix.NewClient(cfg.Clock.URL, opts...)
}

func boardFor(cfg *config.Config, b config.Board, provs map[string]transit.Provider) (scheduler.Board, error) {
	sb := scheduler.Board{
		Name:         b.Name,
		PollInterval: b.PollInterval.D(),
		Window:       b.Window.D(),
		Lifetime:     b.Lifetime.D(),
		MaxStale:     b.MaxStale.D(),
		Duration:     b.Duration.D(),
		PerRow:       b.PerRow,
		Active:       b.ActiveHours.Contains,
		Location:     cfg.Location(),
		Render:       renderOptions(b),
	}
	for _, s := range b.Stops {
		p, ok := provs[s.Provider]
		if !ok {
			return scheduler.Board{}, fmt.Errorf("board %s: provider %q not built", b.Name, s.Provider)
		}
		sb.Sources = append(sb.Sources, scheduler.Source{
			Name:     s.Provider + ":" + s.Stop,
			Provider: p,
			StopID:   s.Stop,
			Filter:   s.Filter(),
		})
	}
	return sb, nil
}

func newRunners(cfg *config.Config, pusher scheduler.Pusher, log *slog.Logger, now func() time.Time) ([]*scheduler.Runner, error) {
	provs, err := buildProviders(cfg)
	if err != nil {
		return nil, err
	}
	runners := make([]*scheduler.Runner, 0, len(cfg.Boards))
	for _, b := range cfg.Boards {
		sb, err := boardFor(cfg, b, provs)
		if err != nil {
			return nil, err
		}
		runners = append(runners, scheduler.NewRunner(sb, pusher, log, now))
	}
	return runners, nil
}

func renderOptions(b config.Board) render.Options {
	return render.Options{
		Layout:    b.Layout,
		MaxRows:   b.MaxRows,
		Labels:    b.Labels,
		Overflow:  b.Overflow,
		Font:      b.Font,
		EmptyText: b.EmptyText,
	}
}
