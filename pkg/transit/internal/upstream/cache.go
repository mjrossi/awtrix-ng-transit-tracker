// Package upstream holds what providers share for reading upstream APIs: a
// cache for loaded data and URL validation.
package upstream

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Policy sets when a Cache loads again.
type Policy struct {
	// TTL is how long a successful load stays fresh.
	TTL time.Duration
	// Retry is how long a failed load is reported before loading again.
	// Zero loads again on the next Get.
	Retry time.Duration
	// RetryOnlyWithData applies Retry only while earlier data is served:
	// with none, every Get loads again, leaving backoff to the caller.
	RetryOnlyWithData bool
	// Background refreshes stale data without waiting for it: callers get
	// the earlier data while the load runs. Callers still wait when there
	// is no earlier data. It requires Timeout, because a background load
	// outlives the caller that started it and nothing else stops it.
	Background bool
	// Timeout bounds each load; zero leaves it to the caller's context.
	Timeout time.Duration
}

// Cache holds one value loaded from upstream. It is safe for concurrent
// use, and concurrent callers share one load: boards sharing a provider
// poll it at the same moment.
//
// Times come from callers, never the wall clock, so providers stay
// reproducible. A time before the last load counts as expired, so a clock
// stepped back cannot keep data fresh indefinitely.
type Cache[T any] struct {
	policy Policy
	load   func(context.Context) (T, error)
	// onWait, when set by tests, runs as a caller starts waiting for a load.
	onWait func()

	mu       sync.Mutex
	val      T
	has      bool
	loadedAt time.Time
	// err is the latest failed load, cleared by a success.
	err      error
	failedAt time.Time
	inflight *call[T]
}

type call[T any] struct {
	done chan struct{}
	val  T
	err  error
	// discarded is set when the load was canceled, so its outcome says
	// nothing about upstream and waiters load again.
	discarded bool
}

// NewCache returns an empty cache that fills itself with load. It panics if
// p sets Background without a Timeout.
func NewCache[T any](p Policy, load func(context.Context) (T, error)) *Cache[T] {
	if p.Background && p.Timeout <= 0 {
		panic("upstream: Policy.Background requires a Timeout")
	}
	return &Cache[T]{policy: p, load: load}
}

// Get returns the latest successfully loaded value, the zero value if none,
// and the error of the latest load if it failed. A failure keeps the
// earlier value. If ctx is done while waiting for a load, Get returns
// ctx.Err().
func (c *Cache[T]) Get(ctx context.Context, now time.Time) (T, error) {
	c.mu.Lock()
	for {
		fresh := c.has && within(now, c.loadedAt, c.policy.TTL)
		backingOff := c.err != nil && (c.has || !c.policy.RetryOnlyWithData) && within(now, c.failedAt, c.policy.Retry)
		if fresh || backingOff {
			val, err := c.val, c.err
			c.mu.Unlock()
			return val, err
		}
		cl := c.inflight
		if cl == nil {
			break
		}
		if c.policy.Background && c.has {
			val, err := c.val, c.err
			c.mu.Unlock()
			return val, err
		}
		c.mu.Unlock()
		if c.onWait != nil {
			c.onWait()
		}
		select {
		case <-cl.done:
		case <-ctx.Done():
			c.mu.Lock()
			val := c.val
			c.mu.Unlock()
			return val, ctx.Err()
		}
		if !cl.discarded {
			return cl.val, cl.err
		}
		c.mu.Lock()
	}
	cl := &call[T]{done: make(chan struct{})}
	c.inflight = cl
	if c.policy.Background && c.has {
		val, err := c.val, c.err
		c.mu.Unlock()
		// Detached so the refresh outlives the poll that started it.
		go c.run(context.WithoutCancel(ctx), now, cl)
		return val, err
	}
	c.mu.Unlock()
	c.run(ctx, now, cl)
	return cl.val, cl.err
}

// Wait blocks until the load in flight, if any, has finished.
func (c *Cache[T]) Wait() {
	c.mu.Lock()
	cl := c.inflight
	c.mu.Unlock()
	if cl != nil {
		<-cl.done
	}
}

func (c *Cache[T]) run(ctx context.Context, now time.Time, cl *call[T]) {
	if c.policy.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.policy.Timeout)
		defer cancel()
	}
	val, err := c.load(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case err == nil:
		c.val, c.has, c.loadedAt, c.err = val, true, now, nil
	case errors.Is(err, context.Canceled):
		// Shutting down: no new data, and nothing to back off from.
		cl.discarded = true
	default:
		c.err, c.failedAt = err, now
	}
	cl.val, cl.err = c.val, err
	c.inflight = nil
	close(cl.done)
}

func within(now, since time.Time, d time.Duration) bool {
	age := now.Sub(since)
	return age >= 0 && age < d
}
