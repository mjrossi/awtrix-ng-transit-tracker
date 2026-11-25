package main

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
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

// quotaUse is a metered provider block's estimated requests a month.
type quotaUse struct {
	Provider        string
	Requests, Quota int
}

// Over reports whether the estimate exceeds the quota.
func (u quotaUse) Over() bool { return u.Requests > u.Quota }

// quotaUses estimates monthly requests for every provider with a quota,
// sorted by block name.
func quotaUses(cfg *config.Config, provs map[string]transit.Provider) []quotaUse {
	refresh := map[string]time.Duration{}
	quota := map[string]int{}
	for name, p := range provs {
		if m, ok := p.(transit.Metered); ok {
			refresh[name], quota[name] = m.RefreshInterval(), m.MonthlyQuota()
		}
	}
	est := cfg.EstimateRequests(refresh)
	out := make([]quotaUse, 0, len(est))
	for name, n := range est {
		out = append(out, quotaUse{Provider: name, Requests: n, Quota: quota[name]})
	}
	slices.SortFunc(out, func(a, b quotaUse) int { return strings.Compare(a.Provider, b.Provider) })
	return out
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
		Active:       b.Active,
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

func newRunners(cfg *config.Config, provs map[string]transit.Provider, out scheduler.Output, log *slog.Logger, now func() time.Time) ([]*scheduler.Runner, error) {
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
