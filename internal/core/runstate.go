package core

import "context"

// Event is a progress notice a command emits while it runs. Surfaces render
// it (the CLI prints the same lines it always printed); core never prints.
type Event struct {
	Kind    EventKind
	ID      string
	Title   string
	Message string
	Err     error
}

// EventKind names a progress notice.
type EventKind string

const (
	// EventQueueDrainFailed: stopping a session freed a slot but the queued
	// sibling failed to start. Title/Err name it.
	EventQueueDrainFailed EventKind = "queue.drain_failed"
	// EventRestartBegin: restart --all is about to restart Title.
	EventRestartBegin EventKind = "restart.begin"
	// EventRestartFailed: a restart failed; Message is the full error text.
	EventRestartFailed EventKind = "restart.failed"
	// EventRestartWarning: a restart succeeded with a warning in Message.
	EventRestartWarning EventKind = "restart.warning"
	// EventRestartDone: restart --all finished Title.
	EventRestartDone EventKind = "restart.done"
	// EventRestartSkipped: restart --all skipped Title; Message is the reason.
	EventRestartSkipped EventKind = "restart.skipped"
)

// Observer receives progress events synchronously, in order.
type Observer func(Event)

type observerKey struct{}
type runStateKey struct{}

// WithObserver attaches an observer to ctx.
func WithObserver(ctx context.Context, obs Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, obs)
}

// Emit sends ev to the observer on ctx, if any.
func Emit(ctx context.Context, ev Event) {
	if obs, ok := ctx.Value(observerKey{}).(Observer); ok && obs != nil {
		obs(ev)
	}
}

type runState struct {
	warnings []string
	after    []func()
}

func withRunState(ctx context.Context, rs *runState) context.Context {
	return context.WithValue(ctx, runStateKey{}, rs)
}

func runStateFrom(ctx context.Context) *runState {
	rs, _ := ctx.Value(runStateKey{}).(*runState)
	return rs
}

// Warn records a non-fatal warning on the running command's Result.
func Warn(ctx context.Context, msg string) {
	if rs := runStateFrom(ctx); rs != nil {
		rs.warnings = append(rs.warnings, msg)
	}
}

// AfterFunc defers fn until the surface has delivered the answer
// (Result.Finish). Outside Registry.Run, fn runs immediately.
func AfterFunc(ctx context.Context, fn func()) {
	if rs := runStateFrom(ctx); rs != nil {
		rs.after = append(rs.after, fn)
		return
	}
	fn()
}
