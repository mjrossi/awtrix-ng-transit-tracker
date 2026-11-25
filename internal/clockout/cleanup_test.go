package clockout_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/internal/clockout"
)

type fakeForgetter struct {
	calls [][]string
	times []time.Time
	// errs are returned by successive calls; after them, calls succeed.
	errs []error
}

func (f *fakeForgetter) ForgetApps(_ context.Context, names []string) ([]string, error) {
	f.calls = append(f.calls, names)
	f.times = append(f.times, time.Now())
	if n := len(f.calls); n <= len(f.errs) {
		return nil, f.errs[n-1]
	}
	return names, nil
}

func cleanup(path, clock string, f *fakeForgetter, boards ...string) clockout.Cleanup {
	return clockout.Cleanup{
		Path: path, Clock: clock, Boards: boards, Forgetter: f,
		Log: slog.New(slog.DiscardHandler), Retry: time.Minute, MaxRetry: 5 * time.Minute,
	}
}

func TestCleanupForgetsRemovedBoards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	f := &fakeForgetter{}
	cleanup(path, "clock.lan", f, "saldanha", "b65").Run(t.Context())
	if len(f.calls) != 0 {
		t.Fatalf("first run forgot %v, want nothing", f.calls)
	}
	cleanup(path, "clock.lan", f, "saldanha").Run(t.Context())
	cleanup(path, "clock.lan", f, "saldanha").Run(t.Context())
	if diff := cmp.Diff([][]string{{"b65"}}, f.calls); diff != "" {
		t.Errorf("forgotten (-want +got):\n%s", diff)
	}
}

func TestCleanupBacksOffUntilForgotten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	synctest.Test(t, func(t *testing.T) {
		offline := errors.New("clock offline")
		f := &fakeForgetter{errs: []error{offline, offline, offline, offline, offline}}
		cleanup(path, "clock.lan", f, "b65").Run(t.Context())
		cleanup(path, "clock.lan", f, "saldanha").Run(t.Context())
		var waits []time.Duration
		for i := 1; i < len(f.times); i++ {
			waits = append(waits, f.times[i].Sub(f.times[i-1]))
		}
		want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
		if diff := cmp.Diff(want, waits); diff != "" {
			t.Errorf("waits (-want +got):\n%s", diff)
		}
	})
}

// A service stopped before it reached the clock still owes the cleanup.
func TestCleanupResumesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	cleanup(path, "clock.lan", &fakeForgetter{}, "b65").Run(t.Context())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	offline := &fakeForgetter{errs: []error{errors.New("clock offline")}}
	cleanup(path, "clock.lan", offline, "saldanha").Run(ctx)

	f := &fakeForgetter{}
	cleanup(path, "clock.lan", f, "saldanha").Run(t.Context())
	if diff := cmp.Diff([][]string{{"b65"}}, f.calls); diff != "" {
		t.Errorf("forgotten (-want +got):\n%s", diff)
	}
}

// Names pushed to one clock say nothing about who owns them on another.
func TestCleanupIgnoresAnotherClocksApps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	f := &fakeForgetter{}
	cleanup(path, "old.lan", f, "b65").Run(t.Context())
	var logs strings.Builder
	c := cleanup(path, "new.lan", f, "saldanha")
	c.Log = slog.New(slog.NewTextHandler(&logs, nil))
	c.Run(t.Context())
	if len(f.calls) != 0 {
		t.Errorf("forgot %v on a different clock", f.calls)
	}
	for _, want := range []string{"level=INFO", "old.lan", "new.lan", "b65"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs = %q, want %q in them", logs.String(), want)
		}
	}
}

func TestCleanupBadState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeForgetter{}
	cleanup(path, "clock.lan", f, "b65").Run(t.Context())
	if len(f.calls) != 0 {
		t.Errorf("forgot %v from an unreadable state file", f.calls)
	}
	// The file is usable again afterwards.
	cleanup(path, "clock.lan", f, "saldanha").Run(t.Context())
	if diff := cmp.Diff([][]string{{"b65"}}, f.calls); diff != "" {
		t.Errorf("forgotten (-want +got):\n%s", diff)
	}
}

// The path is a symlink loop, which fails to read but which a save would
// replace, even as root.
func TestCleanupUnreadableStateIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.Symlink(path, path); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	f := &fakeForgetter{}
	c := cleanup(path, "clock.lan", f, "b65")
	c.Log = slog.New(slog.NewTextHandler(&logs, nil))
	c.Run(t.Context())
	if len(f.calls) != 0 {
		t.Errorf("forgot %v from an unreadable state file", f.calls)
	}
	if !strings.Contains(logs.String(), "the state file is left alone") {
		t.Errorf("logs = %q, want a warning", logs.String())
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("state path was replaced: fi = %v, err = %v", fi, err)
	}
}

func TestCleanupWarnsWhenStateCannotBeSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.json")
	var logs strings.Builder
	c := cleanup(path, "clock.lan", &fakeForgetter{}, "b65")
	c.Log = slog.New(slog.NewTextHandler(&logs, nil))
	c.Run(t.Context())
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("logs = %q, want a warning", logs.String())
	}
}
