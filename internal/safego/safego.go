// Package safego provides a panic-recovering wrapper for fire-and-forget
// goroutines.
//
// A single un-recovered panic in a background goroutine kills the entire
// process — disastrous for the TUI. The standard pattern of inlining
//
//	go func() {
//	    defer func() {
//	        if r := recover(); r != nil {
//	            log.Error("worker_panic", slog.Any("panic", r))
//	        }
//	    }()
//	    ...
//	}()
//
// at every site is verbose, easy to forget, and easy to mistype. safego.Go
// centralises the pattern so that wrapping a goroutine is a one-liner.
//
// See V1.9 plan §T6 (arch-review §5).
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a new goroutine with a deferred recover. If fn panics, the
// panic value and a goroutine stack trace are logged at WARN level on
// logger and then swallowed — the caller's process keeps running.
//
// name is a short identifier included in the log record so operators can
// trace which goroutine crashed (e.g. "startup-pipe-connect").
//
// logger may be nil; in that case the panic is still recovered but the
// log record is dropped. This makes safego.Go safe to call from packages
// that have not yet wired up a component logger.
func Go(logger *slog.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if logger != nil {
					logger.Warn("safego_panic",
						slog.String("name", name),
						slog.Any("panic", r),
						slog.String("stack", string(debug.Stack())),
					)
				}
			}
		}()
		fn()
	}()
}
