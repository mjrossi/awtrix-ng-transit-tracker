package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/clockout"
	"github.com/mjrossi/urbanist-timetable/internal/config"
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
	for name, raw := range cfg.Providers {
		typ, opts, err := config.ResolveProvider(name, raw)
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
		MaxStale:     b.MaxStale.D(),
		PerRow:       b.PerRow,
		Active:       b.ActiveHours.Contains,
		Quiet:        cfg.Quiet,
		Location:     b.Location(),
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

func newRunners(cfg *config.Config, out scheduler.Output, log *slog.Logger, now func() time.Time) ([]*scheduler.Runner, error) {
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
		runners = append(runners, scheduler.NewRunner(sb, out, log, now))
	}
	return runners, nil
}

// newClockOutput returns the clock output for every board. pusher may be nil
// when apps are only built, not pushed.
func newClockOutput(cfg *config.Config, pusher clockout.Pusher) *clockout.Output {
	boards := make(map[string]clockout.Board, len(cfg.Boards))
	for _, b := range cfg.Boards {
		boards[b.Name] = clockout.Board{
			Render:   b.Clock.Render(),
			Lifetime: b.Clock.Lifetime.D(),
			Duration: b.Clock.Duration.D(),
		}
	}
	return clockout.New(pusher, boards)
}

// Waits for a clock that is off or unreachable. Cleanup is not urgent, so
// the cap is long.
const (
	cleanupRetry    = time.Minute
	cleanupMaxRetry = time.Hour
)

func newCleanup(cfg *config.Config, path string, clock *awtrix.Client, log *slog.Logger) clockout.Cleanup {
	boards := make([]string, len(cfg.Boards))
	for i, b := range cfg.Boards {
		boards[i] = b.Name
	}
	return clockout.Cleanup{
		Path:      path,
		Clock:     clock.ID(),
		Boards:    boards,
		Forgetter: clock,
		Log:       log,
		Retry:     cleanupRetry,
		MaxRetry:  cleanupMaxRetry,
		Quiet:     cfg.Quiet,
	}
}
