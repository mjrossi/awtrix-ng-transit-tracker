// Package clockout is the scheduler output that renders boards for an
// AWTRIX NG clock and pushes them as apps.
package clockout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/internal/scheduler"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
)

// Pusher sends apps to the clock. *awtrix.Client implements it.
type Pusher interface {
	PushApp(ctx context.Context, name string, app awtrix.App) error
	ExpireApp(ctx context.Context, name string) error
}

// Board is how one board appears on the clock.
type Board struct {
	Render render.Options
	// Lifetime is how long the clock keeps the app without a fresh push.
	Lifetime time.Duration
	// Duration is how long the app is shown per rotation; zero uses the
	// clock's setting.
	Duration time.Duration
}

// Output implements scheduler.Output for one clock. Each board is pushed as
// an app named after the board.
type Output struct {
	pusher Pusher
	boards map[string]Board
}

var _ scheduler.Output = (*Output)(nil)

// New returns an output for the given boards, keyed by board name. pusher
// may be nil when the output is used only to build apps.
func New(pusher Pusher, boards map[string]Board) *Output {
	return &Output{pusher: pusher, boards: boards}
}

// App renders a snapshot into the payload pushed to the clock.
func (o *Output) App(snap scheduler.Snapshot) (awtrix.App, error) {
	b, ok := o.boards[snap.Board]
	if !ok {
		return awtrix.App{}, fmt.Errorf("clock: no settings for board %q", snap.Board)
	}
	return awtrix.App{
		DurationMs:     int(b.Duration / time.Millisecond),
		LifetimeMs:     int(b.Lifetime / time.Millisecond),
		LifetimeExpiry: awtrix.ExpiryRemove,
		Layout:         render.TC002(snap.Rows(snap.At), snap.At, b.Render),
	}, nil
}

// errNoPusher is returned by Publish and Withdraw on an output built
// without a pusher, which can only build apps.
var errNoPusher = errors.New("clock: output has no pusher")

// Publish implements scheduler.Output.
func (o *Output) Publish(ctx context.Context, snap scheduler.Snapshot) error {
	if o.pusher == nil {
		return errNoPusher
	}
	app, err := o.App(snap)
	if err != nil {
		return err
	}
	return o.pusher.PushApp(ctx, snap.Board, app)
}

// Withdraw implements scheduler.Output.
func (o *Output) Withdraw(ctx context.Context, board string) error {
	if o.pusher == nil {
		return errNoPusher
	}
	return o.pusher.ExpireApp(ctx, board)
}
