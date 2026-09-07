// Package codex reports what occupies a Codex CLI session's context window
// before its first turn.
//
// # Mechanism
//
// Codex writes its whole injected prefix to disk, verbatim, in the head of the
// session rollout. Two kinds of record carry it:
//
//   - session_meta.payload.base_instructions.text — the base system prompt, in
//     full. Codex is the one supported harness that ships this, which is why
//     [Adapter.Capabilities] reports CanVerbatimSystem for it and the Claude
//     Code adapter does not.
//   - response_item message records with role "developer" or "user", written
//     before the first model turn. Each such message carries a content array,
//     and every element of that array is one separately injected block opened
//     by its own marker: <permissions instructions>, <collaboration_mode>,
//     <apps_instructions>, <plugins_instructions>, <skills_instructions>,
//     <multi_agent_mode>, <environment_context>, and the "# AGENTS.md
//     instructions" block.
//
// Those content parts are the measurement substrate. They are the literal items
// of the model request, so every figure attributed to them describes bytes that
// were actually sent, and their text provenance is [ctxinspect.TextCaptured].
//
// # Why world_state is an attribution key and not a source
//
// Codex also writes a world_state record holding agents_md, host_skills,
// environments and permissions. It is tempting to price those directly, and
// doing so would be wrong: measurement on real rollouts shows host_skills.body
// (16,457 characters in the reference session) is embedded *inside* the
// <skills_instructions> content part (16,500 characters), and agents_md.text
// (7,522) inside the "# AGENTS.md instructions" part (7,579). Summing both the
// world state and the parts would double count almost the entire prefix.
//
// So world_state is used only to locate content within a part. Where a world
// state blob cannot be located in any part, the adapter says so in a caveat and
// declines to add it as a separate cost — its bytes are already inside another
// item's residue, and counting them twice would be the exact failure this
// feature exists to prevent. World state becomes a primary source only when a
// rollout carries no prefix content parts at all.
//
// # Format drift is real and is handled by omission
//
// The world_state key set moves between Codex releases. Measured on one
// machine: 0.142–0.144 write agents_md, apps_instructions, environments,
// plugins_instructions, skills; 0.145–0.146 add host_skills, permissions,
// realtime and environments_instructions; releases up to 0.101 write no
// world_state record at all, and record the session instructions as a plain
// session_meta.instructions string instead of a base_instructions object.
//
// The adapter declares a category only when the underlying source exists in the
// rollout it is reading. A category Codex did not record is omitted rather than
// emitted with a zero, because a zero is a claim that the content costs nothing
// and an omission is the truth that it was not recorded.
//
// # The anchor
//
// The first event_msg token_count record whose info is populated carries
// info.last_token_usage.input_tokens — the provider's own measurement of the
// first request's prompt — and info.model_context_window, which is the context
// window measured rather than inferred from a model name.
//
// It is accepted as a cold-prefix anchor only when the same record's
// total_token_usage.input_tokens equals last_token_usage.input_tokens. A
// difference means the rollout's first recorded turn is not the session's first
// turn, so it measures a warm conversation and not a fixed prefix; measured
// across sixty recent rollouts, fifty-five agreed and five did not.
//
// A non-zero cached_input_tokens is deliberately *not* treated as
// disqualifying. Prompt caching is normal on a cold start — twenty-seven of the
// same sixty reported one — and vetoing on it would reduce the feature to a
// projection. It is reported as information.
package codex
