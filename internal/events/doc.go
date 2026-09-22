// Package events is the single event bus for agent-deck (CORE-PLAN slice 4).
//
// It is additive: every existing producer (internal/session/event_writer.go,
// internal/session/transition_notifier.go, internal/tmux/pipemanager.go
// %output, internal/watcher/engine.go) keeps writing its own on-disk format
// exactly as before, and also calls Publish so the same event lands on the
// bus. No storage format, no state.db table, no events/ directory layout, no
// inbox jsonl and no per-parent outbox changes because of this package.
//
// The bus owns one new durable append log per profile, rotated into sealed
// segments and compacted on a retention window (see docs/events.md for the
// exact path and defaults). A Cursor is a durable, monotonically increasing,
// per-profile sequence number assigned as a frame is committed; Subscribe
// resumes with zero loss and zero duplication from any previously observed
// Cursor, including across process restarts, as long as the frame has not
// aged out of the retention window.
//
// Public API (kept intentionally small; slice 5's daemon streams this bus
// over a socket and should need nothing else from this package):
//
//	Publish(kind, sessionID string, data any)              // never blocks; drops under pressure
//	Subscribe(ctx context.Context, after Cursor) (*Subscription, error)
//	Cursor() Cursor
//	Close() error
//
// Producers publish through the process-wide Default() bus. Publish is
// non-blocking: a bounded queue feeds a background writer goroutine, and a
// full queue increments a drop counter (visible via Stats/`events stats
// --json`) instead of blocking the caller. A disabled or unwritable bus
// degrades to a no-op Publish with a single logged warning — nothing about
// existing behavior depends on the bus being present.
package events
