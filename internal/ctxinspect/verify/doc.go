// Package verify compares an agent-deck context report against the harness's
// own accounting.
//
// The context inspector derives every figure from what a harness wrote to disk.
// That is a claim about someone else's format, and a claim about someone else's
// format is worth exactly as much as the last time it was checked. This package
// is the check: it reads the number the harness prints for itself — Claude
// Code's /context panel, Codex's /status panel — and diffs it, per comparable
// group, against the report agent-deck produced from disk.
//
// # Three deliberate limits
//
// Parsing another program's TUI is inherently brittle, so the package is built
// so that brittleness surfaces as a loud failure rather than as a wrong number:
//
//  1. A pane that does not parse is an error ([ErrNoAccounting]), never an
//     empty report and never a zero. Lines that look like a figure but do not
//     parse are kept in [HarnessReport.Unrecognized] and printed, so a format
//     change is visible in the output rather than silently narrowing the diff.
//  2. Only quantities that genuinely mean the same thing are compared. Claude's
//     "System tools" row prices whole tool schemas while agent-deck prices only
//     the deferred tool *names*, so the two are never diffed one-to-one; they
//     are compared as a group together with the residual, which is the level at
//     which the arithmetic is actually equivalent. Groups that cannot be made
//     equivalent are reported as informational, with the reason.
//  3. Running the comparison types a slash command into the user's live
//     session. That is a mutation, so it is an explicit verb
//     (`agent-deck session context <ref> --verify`) that asks for confirmation.
//     Nothing in the TUI reaches this package; pressing the context hotkey
//     never sends a keystroke to a session.
//
// # Layout
//
// The parsing and diffing halves are pure functions over strings and a
// [ctxinspect.Report], so the whole comparison is testable from a fixture with
// no tmux, no harness and no HOME. Only [RunLive] touches a live session, and it
// does so through two one-method interfaces that *tmux.Session satisfies.
package verify
