package upstream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var t0 = time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)

type result struct {
	val string
	err error
}

func ok(v string) *result     { return &result{val: v} }
func fail(msg string) *result { return &result{err: errors.New(msg)} }

var canceled = &result{err: context.Canceled}

type got struct{ Val, Err string }

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestCacheSequence(t *testing.T) {
	policy := Policy{TTL: 10 * time.Minute, Retry: 3 * time.Minute}
	type step struct {
		at time.Duration
		// load is what the loader returns; nil means it must not be called.
		load *result
		want got
	}
	tests := []struct {
		name   string
		policy Policy
		steps  []step
	}{
		{"fresh hit", policy, []step{
			{0, ok("a"), got{"a", ""}},
			{9 * time.Minute, nil, got{"a", ""}},
		}},
		{"a moment early counts as due", policy, []step{
			{0, ok("a"), got{"a", ""}},
			{10*time.Minute - 1001*time.Millisecond, nil, got{"a", ""}},
			{10*time.Minute - time.Second, fail("boom"), got{"a", "boom"}},
			{13*time.Minute - 2001*time.Millisecond, nil, got{"a", "boom"}},
			{13*time.Minute - 2*time.Second, ok("b"), got{"b", ""}},
		}},
		{"expiry", policy, []step{
			{0, ok("a"), got{"a", ""}},
			{10 * time.Minute, ok("b"), got{"b", ""}},
		}},
		{"failure keeps old data until retry", policy, []step{
			{0, ok("a"), got{"a", ""}},
			{10 * time.Minute, fail("boom"), got{"a", "boom"}},
			{12 * time.Minute, nil, got{"a", "boom"}},
			{13 * time.Minute, ok("b"), got{"b", ""}},
		}},
		{"failure without data", policy, []step{
			{0, fail("boom"), got{"", "boom"}},
			{2 * time.Minute, nil, got{"", "boom"}},
			{3 * time.Minute, fail("again"), got{"", "again"}},
			{6 * time.Minute, ok("a"), got{"a", ""}},
		}},
		{"retry only with data: none yet loads again", Policy{TTL: 10 * time.Minute, Retry: 3 * time.Minute, RetryOnlyWithData: true}, []step{
			{0, fail("boom"), got{"", "boom"}},
			{time.Minute, ok("a"), got{"a", ""}},
		}},
		{"retry only with data: old data backs off", Policy{TTL: 10 * time.Minute, Retry: 3 * time.Minute, RetryOnlyWithData: true}, []step{
			{0, ok("a"), got{"a", ""}},
			{10 * time.Minute, fail("boom"), got{"a", "boom"}},
			{12 * time.Minute, nil, got{"a", "boom"}},
			{13 * time.Minute, ok("b"), got{"b", ""}},
		}},
		{"canceled load records nothing", policy, []step{
			{0, ok("a"), got{"a", ""}},
			{10 * time.Minute, canceled, got{"a", "context canceled"}},
			{10 * time.Minute, ok("b"), got{"b", ""}},
		}},
		{"time slightly before the load stays fresh", policy, []step{
			{10 * time.Minute, ok("a"), got{"a", ""}},
			{9 * time.Minute, nil, got{"a", ""}},
		}},
		{"time slightly before a failure backs off", policy, []step{
			{10 * time.Minute, fail("boom"), got{"", "boom"}},
			{9 * time.Minute, nil, got{"", "boom"}},
		}},
		{"clock stepped back past TTL counts as expired", policy, []step{
			{20 * time.Minute, ok("a"), got{"a", ""}},
			{9 * time.Minute, ok("b"), got{"b", ""}},
		}},
		{"zero retry loads again at once", Policy{TTL: time.Minute}, []step{
			{0, fail("boom"), got{"", "boom"}},
			{0, fail("again"), got{"", "again"}},
			{0, ok("a"), got{"a", ""}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var next *result
			c := NewCache(tt.policy, func(context.Context) (string, error) {
				if next == nil {
					t.Fatal("unexpected load")
				}
				r := next
				next = nil
				return r.val, r.err
			})
			for i, s := range tt.steps {
				next = s.load
				v, err := c.Get(t.Context(), t0.Add(s.at))
				if diff := cmp.Diff(s.want, got{v, errString(err)}); diff != "" {
					t.Errorf("step %d mismatch (-want +got):\n%s", i, diff)
				}
				if next != nil {
					t.Errorf("step %d: loader not called", i)
				}
			}
		})
	}
}

// gatedLoader holds every load until release, reporting each start.
type gatedLoader struct {
	loads   atomic.Int32
	started chan struct{}
	gate    chan result
}

func newGatedLoader() *gatedLoader {
	return &gatedLoader{started: make(chan struct{}, 16), gate: make(chan result, 16)}
}

func (g *gatedLoader) load(ctx context.Context) (string, error) {
	g.loads.Add(1)
	g.started <- struct{}{}
	select {
	case r := <-g.gate:
		return r.val, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestCacheSharesOneLoad(t *testing.T) {
	const callers = 8
	tests := []struct {
		name   string
		result result
		want   got
	}{
		{"success", result{val: "a"}, got{"a", ""}},
		// Even with no retry delay, callers waiting on a failed load share
		// its outcome rather than each loading in turn.
		{"failure", result{err: errors.New("boom")}, got{"", "boom"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGatedLoader()
			c := NewCache(Policy{TTL: time.Minute}, g.load)
			waiting := make(chan struct{}, callers)
			c.onWait = func() { waiting <- struct{}{} }

			results := make(chan got, callers)
			var wg sync.WaitGroup
			for range callers {
				wg.Go(func() {
					v, err := c.Get(t.Context(), t0)
					results <- got{v, errString(err)}
				})
			}
			<-g.started
			for range callers - 1 {
				<-waiting
			}
			g.gate <- tt.result
			wg.Wait()
			close(results)
			for r := range results {
				if diff := cmp.Diff(tt.want, r); diff != "" {
					t.Errorf("result mismatch (-want +got):\n%s", diff)
				}
			}
			if n := g.loads.Load(); n != 1 {
				t.Errorf("loaded %d times, want 1", n)
			}
		})
	}
}

func TestCacheWaiterCanceled(t *testing.T) {
	g := newGatedLoader()
	c := NewCache(Policy{TTL: time.Minute}, g.load)
	waiting := make(chan struct{}, 1)
	c.onWait = func() { waiting <- struct{}{} }

	first := make(chan got, 1)
	go func() {
		v, err := c.Get(t.Context(), t0)
		first <- got{v, errString(err)}
	}()
	<-g.started

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := c.Get(ctx, t0)
		done <- err
	}()
	<-waiting
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("waiter err = %v, want context.Canceled", err)
	}

	g.gate <- result{val: "a"}
	if diff := cmp.Diff(got{"a", ""}, <-first); diff != "" {
		t.Errorf("loading caller mismatch (-want +got):\n%s", diff)
	}
}

// A load canceled by its caller says nothing about upstream; a caller that
// was waiting on it loads for itself.
func TestCacheWaiterLoadsAfterCanceledLoad(t *testing.T) {
	g := newGatedLoader()
	c := NewCache(Policy{TTL: time.Minute}, g.load)
	waiting := make(chan struct{}, 1)
	c.onWait = func() { waiting <- struct{}{} }

	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() {
		_, err := c.Get(ctx, t0)
		first <- err
	}()
	<-g.started

	second := make(chan got, 1)
	go func() {
		v, err := c.Get(t.Context(), t0)
		second <- got{v, errString(err)}
	}()
	<-waiting
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("canceled caller err = %v, want context.Canceled", err)
	}
	<-g.started
	g.gate <- result{val: "a"}
	if diff := cmp.Diff(got{"a", ""}, <-second); diff != "" {
		t.Errorf("waiter mismatch (-want +got):\n%s", diff)
	}
	if n := g.loads.Load(); n != 2 {
		t.Errorf("loaded %d times, want 2", n)
	}
}

func TestCacheBackgroundRefresh(t *testing.T) {
	tests := []struct {
		name    string
		refresh result
		want    got
	}{
		{"success", result{val: "b"}, got{"b", ""}},
		{"failure keeps old data", result{err: errors.New("boom")}, got{"a", "boom"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGatedLoader()
			c := NewCache(Policy{TTL: 10 * time.Minute, Retry: time.Minute, Background: true, Timeout: time.Hour}, g.load)
			c.onWait = func() { t.Error("waited although earlier data exists") }

			g.gate <- result{val: "a"}
			if _, err := c.Get(t.Context(), t0); err != nil {
				t.Fatal(err)
			}
			<-g.started

			// The refresh outlives the poll that starts it.
			ctx, cancel := context.WithCancel(t.Context())
			stale := t0.Add(10 * time.Minute)
			for range 2 {
				v, err := c.Get(ctx, stale)
				if diff := cmp.Diff(got{"a", ""}, got{v, errString(err)}); diff != "" {
					t.Errorf("during refresh (-want +got):\n%s", diff)
				}
			}
			cancel()
			<-g.started
			g.gate <- tt.refresh
			c.Wait()

			v, err := c.Get(t.Context(), stale)
			if diff := cmp.Diff(tt.want, got{v, errString(err)}); diff != "" {
				t.Errorf("after refresh (-want +got):\n%s", diff)
			}
			if n := g.loads.Load(); n != 2 {
				t.Errorf("loaded %d times, want 2", n)
			}
		})
	}
}

// A load that overruns Timeout fails like any other upstream failure, so
// callers back off from it.
func TestCacheTimeout(t *testing.T) {
	loads := 0
	c := NewCache(Policy{TTL: time.Minute, Retry: time.Minute, Timeout: time.Millisecond}, func(ctx context.Context) (string, error) {
		loads++
		<-ctx.Done()
		return "", ctx.Err()
	})
	for range 2 {
		if _, err := c.Get(t.Context(), t0); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	}
	if loads != 1 {
		t.Errorf("loaded %d times, want 1", loads)
	}
}

func TestCacheBackgroundNeedsTimeout(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewCache with Background and no Timeout: want a panic")
		}
	}()
	NewCache(Policy{TTL: time.Minute, Background: true}, func(context.Context) (string, error) { return "", nil })
}

func TestHTTPURL(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"https://example.com/feed", true},
		{"http://example.com", true},
		{"ftp://example.com", false},
		{"http://", false},
		{"not a url", false},
		{"", false},
	}
	for _, tt := range tests {
		_, err := HTTPURL("base_url", tt.raw)
		if got := err == nil; got != tt.want {
			t.Errorf("HTTPURL(%q): err = %v, want valid = %v", tt.raw, err, tt.want)
		}
		if err != nil && err.Error() != "base_url must be an http(s) URL" {
			t.Errorf("HTTPURL(%q): err = %q, want it to name only the option", tt.raw, err)
		}
	}
}
