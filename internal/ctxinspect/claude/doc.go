// Package claude reports what occupies a Claude Code session's context window
// before its first turn.
//
// # Mechanism
//
// Claude Code writes the inventory it injects at startup into the session
// transcript, verbatim, in the handful of records that precede the first
// assistant turn. Four attachment records carry it:
//
//   - skill_listing — the exact catalogue line for every skill that loaded,
//     plus the names and the count;
//   - agent_listing_delta — one line per agent made available;
//   - deferred_tools_delta — the names (only the names) of tools whose schemas
//     load on demand;
//   - mcp_instructions_delta — the instruction block each MCP server
//     contributed, verbatim.
//
// Those are read back byte-exact, so their text provenance is
// [ctxinspect.TextCaptured]. The parse therefore reads only the head of the
// transcript and stops at the first assistant record; on a real corpus that is
// under twenty lines of a file that routinely exceeds a megabyte, which is what
// keeps this cheap enough to run from a keypress.
//
// # What is not on disk
//
// The memory-file chain is not in the transcript. Verified across the eighty
// most recent transcripts on a working machine: zero contain the injected
// CLAUDE.md text, because it goes straight into the system prompt. It is
// therefore *reconstructed* by re-walking Claude Code's own discovery rules and
// reproducing its wrapper strings, and every such item is labelled
// [ctxinspect.TextReconstructed] rather than passed off as captured. Where a
// nested_memory record does exist for a file, that file is upgraded to captured.
//
// The base system prompt and the built-in tool schemas are not on disk at all.
// They are reported by subtraction from the provider-measured first-turn total —
// [ctxinspect.Report.Reconcile] emits them as the explicit residual row, with
// text absent and the number marked [ctxinspect.TokenResidual].
//
// # The anchor, and one deliberate departure from the design note
//
// The design note specified disqualifying the first-turn anchor whenever
// cache_read_input_tokens is above zero, on the reasoning that a warm cache
// means a resumed session. Measurement contradicts that: across eight
// independent cold-start sessions the first turn reported cache_read of 20554,
// 20554, 20554, 20554, 20358, 22847, 22899 and 20576 tokens. The figure is
// near-constant because Anthropic's prompt cache is shared *between* sessions —
// a fresh session reads the previous one's cached prefix. Applying the rule
// literally would nil the anchor on essentially every session and reduce the
// feature to a projection.
//
// What the rule was protecting against is real, so it is implemented against
// signals that actually mean it: a compact_boundary record, a compact-summary
// user message, or a summary record appearing before the first assistant turn.
// The 45774d13 session on the same machine is exactly that case — a
// compact_boundary at line 25 and a first-turn total of 868k, which is a
// replayed conversation and not a fixed prefix — and it is correctly
// disqualified. Cache read is reported in the anchor's method string as
// information, not used as a veto.
package claude
