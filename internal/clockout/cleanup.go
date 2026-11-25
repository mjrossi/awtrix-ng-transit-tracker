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
	// ForgetApps returns the names it removed. Others are not on the clock,
	// or belong to a script or built-in app.
	ForgetApps(ctx context.Context, names []string) ([]string, error)
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
		if !errors.Is(err, errCorrupt) {
			// Saving would lose the names the file still records.
			c.Log.WarnContext(ctx, "read state; boards removed earlier are not cleaned up, and the state file is left alone", slog.Any("err", err))
			return
		}
		c.Log.WarnContext(ctx, "state file is corrupt; starting without it", slog.Any("err", err))
		prev = cleanupState{}
	}
	if prev.Clock != c.Clock {
		if len(prev.Apps) > 0 {
			c.Log.InfoContext(ctx, "the clock's address changed; boards recorded for the old address are forgotten",
				slog.String("from", prev.Clock), slog.String("to", c.Clock), slog.Any("boards", prev.Apps))
		}
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
		removed, err := c.Forgetter.ForgetApps(ctx, stale)
		if err == nil {
			if len(removed) > 0 {
				c.Log.InfoContext(ctx, "removed old boards from the clock", slog.Any("boards", removed))
			}
			if left := slices.DeleteFunc(slices.Clone(stale), func(n string) bool {
				return slices.Contains(removed, n)
			}); len(left) > 0 {
				c.Log.InfoContext(ctx, "old boards not in the clock's rotation, or their name belongs to a script or built-in app", slog.Any("boards", left))
			}
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
	c.save(ctx, c.Boards)
}

func (c Cleanup) save(ctx context.Context, apps []string) {
	if err := writeState(c.Path, cleanupState{Clock: c.Clock, Apps: apps}); err != nil {
		c.Log.WarnContext(ctx, "save state; boards removed later will keep their place on the clock", slog.Any("err", err))
	}
}

// errCorrupt marks a state file that cannot be parsed. It is safe to
// replace: it records nothing usable.
var errCorrupt = errors.New("state: corrupt file")

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
		return cleanupState{}, fmt.Errorf("%w: %s: %w", errCorrupt, path, err)
	}
	return s, nil
}

// writeState replaces the file in one step and flushes it first, so a crash
// or power loss leaves the old or the new state. The old one is safe: its
// cleanup is repeated.
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
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("state: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	return nil
}
