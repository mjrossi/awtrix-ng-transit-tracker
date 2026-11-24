package clockout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Forgetter removes apps, and the places they keep, from the clock.
// *awtrix.Client implements it.
type Forgetter interface {
	ForgetApps(ctx context.Context, names []string) error
}

// Cleanup removes boards that left the configuration from the clock. The
// clock does not record who pushed an app, so the names are kept in a file.
type Cleanup struct {
	// Path is the state file.
	Path string
	// Clock identifies the clock. Names recorded for another one are
	// dropped, since they say nothing about who owns them on this one.
	Clock     string
	Boards    []string
	Forgetter Forgetter
	Log       *slog.Logger
	// Retry is the wait after the first failed attempt. It doubles after
	// each further one, up to MaxRetry.
	Retry    time.Duration
	MaxRetry time.Duration
}

// cleanupState is the state file: the apps this service may have pushed.
type cleanupState struct {
	Clock string   `json:"clock"`
	Apps  []string `json:"apps"`
}

// Run records the boards, then retries removing those no longer configured
// until it succeeds or ctx is done.
func (c Cleanup) Run(ctx context.Context) {
	prev, err := readState(c.Path)
	if err != nil {
		c.Log.WarnContext(ctx, "read state; boards removed earlier are not cleaned up", slog.Any("err", err))
	}
	if prev.Clock != c.Clock {
		prev.Apps = nil
	}
	stale := slices.DeleteFunc(slices.Clone(prev.Apps), func(n string) bool {
		return slices.Contains(c.Boards, n)
	})
	// Until they are gone from the clock, removed boards stay recorded, so
	// a restart resumes the cleanup.
	c.save(ctx, slices.Concat(c.Boards, stale))
	if len(stale) == 0 {
		return
	}
	for wait := c.Retry; ; wait = min(2*wait, c.MaxRetry) {
		err := c.Forgetter.ForgetApps(ctx, stale)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		c.Log.WarnContext(ctx, "remove old boards from the clock",
			slog.Any("boards", stale), slog.Duration("retry_in", wait), slog.Any("err", err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	c.Log.InfoContext(ctx, "removed old boards from the clock", slog.Any("boards", stale))
	c.save(ctx, c.Boards)
}

func (c Cleanup) save(ctx context.Context, apps []string) {
	if err := writeState(c.Path, cleanupState{Clock: c.Clock, Apps: apps}); err != nil {
		c.Log.WarnContext(ctx, "save state; boards removed later will keep their place on the clock", slog.Any("err", err))
	}
}

func readState(path string) (cleanupState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cleanupState{}, nil
	}
	if err != nil {
		return cleanupState{}, fmt.Errorf("state: %w", err)
	}
	var s cleanupState
	if err := json.Unmarshal(b, &s); err != nil {
		return cleanupState{}, fmt.Errorf("state: %s: %w", path, err)
	}
	return s, nil
}

// writeState replaces the file in one step, so a crash leaves the old or the
// new state.
func writeState(path string, s cleanupState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(append(b, '\n'))
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	return nil
}
