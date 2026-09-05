// Package ctxinspect reconstructs, for a single agent session, everything that
// occupies the agent's context window before the first user turn — and records,
// for every number and every byte it reports, where that came from.
//
// # Purity
//
// The package is deliberately pure. It imports no other agent-deck package, it
// never shells out, it never touches tmux, and it makes no network call. Every
// entry point takes plain values through [Request], so a report can be produced
// from golden fixtures with no HOME, no live session and no harness installed.
// Anything that needs agent-deck's own knowledge of a session (per-tool MCP
// configuration, the [mcps.*] catalog, harness-compatibility predicates) enters
// through the [Host] interface, which the caller wires; the
// internal/ctxinspect/sessionhost subpackage provides the production
// implementation backed by internal/session.
//
// # The rule the design follows
//
// A wrong token number is worse than no token number. Two mechanisms enforce
// that:
//
//   - Provenance is two-axis. [TextProv] says how good the *bytes* are
//     (captured verbatim from the harness, reconstructed by us, or absent);
//     [TokenProv] says how good the *number* is (provider-measured, computed
//     with a real tokenizer, estimated, derived by subtraction, or unknown).
//     Collapsing those onto one axis forces a lie in one direction: the skill
//     listing is verbatim text with an estimated cost, while a first-turn usage
//     record is a measured cost with no text at all.
//
//   - Numbers cannot exist without provenance. [TokenCount] has no exported
//     fields and no literal form; the zero value is unknown, and
//     [TokenCount.Value] returns ok=false so a renderer cannot print a number
//     that was never established. Sums degrade to unknown rather than to zero,
//     and subtraction never clamps — a negative residual is a parser bug we
//     want on screen, not hidden.
//
// # Shape of a report
//
// An [Adapter] produces a [Report]: adapter-declared [Category] values, each
// holding [Item] values that carry a [Load] (what the item costs now versus
// what it would cost if fully loaded), an [Origin] (who put it there) and a
// [Lever] (what the user can do about it). [Report.Reconcile] then computes the
// explicit [Report.Unaccounted] residual against the provider-measured
// [Anchor], and [Report.Validate] rejects reports that violate the invariants
// the honesty guarantees rest on.
package ctxinspect
