// Package core is the typed command layer shared by every agent-deck surface
// (CLI today; TUI, web and the daemon in later slices of docs/CORE-PLAN.md).
//
// A command is one Def: a stable id, the CLI path that reaches it, whether it
// reads or mutates state, whether remote callers may run it, and a typed
// Exec function over an input struct and an output struct. The registry holds
// the Defs; the executor runs one and wraps the result in an Envelope.
//
// Rules for everything in this package:
//   - never print, never read os.Args, never call os.Exit;
//   - wrap the existing internal/session functions, do not re-home their logic;
//   - report progress through an Observer and deferred work through AfterFunc,
//     so each surface decides how (and when) to render it.
//
// Slice 1 registers five commands (session.start, session.stop,
// session.restart, session.list, group.list). Anything not registered keeps
// its existing code path.
package core
