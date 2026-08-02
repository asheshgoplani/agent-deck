// Issue #1821: SubcommandPassthrough JSON helper.
//
// Follows the idle_timeout_persist.go pattern (#1143) for WHERE the value
// lives: the tool_data blob, without changing the positional
// MarshalToolData / UnmarshalToolData signatures or requiring a SQL schema
// migration. It deliberately does NOT follow idle_timeout's "delete the key
// at the zero value" convention — see WriteSubcommandPassthroughToToolData's
// doc for why that would silently resurrect a stale `true` through
// MergeToolDataExtras (statedb/tool_data_extras.go).
package session

import "encoding/json"

const toolDataSubcommandPassthroughKey = "subcommand_passthrough"

// WriteSubcommandPassthroughToToolData merges subcommand_passthrough into
// the given tool_data JSON blob. Unlike WriteIdleTimeoutSecsToToolData,
// this ALWAYS writes the key explicitly — including false — rather than
// deleting it when false. subcommand_passthrough is not in toolDataKnownKeys
// (it lives in the extras zone, same as idle_timeout_secs), so
// MergeToolDataExtras (statedb/tool_data_extras.go) only skips
// re-preserving an old value when the new blob has the key present at all;
// deleting it on false would make MergeToolDataExtras treat "explicitly
// cleared to false" as "this binary doesn't know the key" and silently
// resurrect a stale `true` from the previous save — turning off passthrough
// on an edited session, then saving, would not actually turn it off
// (Codex review, PR #1821 follow-up). false is a meaningful assertion here,
// not "nothing to say", so it must always round-trip explicitly.
func WriteSubcommandPassthroughToToolData(td json.RawMessage, passthrough bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	raw, _ := json.Marshal(passthrough)
	m[toolDataSubcommandPassthroughKey] = raw
	out, _ := json.Marshal(m)
	return out
}

// ReadSubcommandPassthroughFromToolData extracts subcommand_passthrough
// from the blob. Returns false (not a passthrough instance) for
// missing/malformed/legacy rows — the safe default: an unrecognized or
// pre-#1821 row must never be retroactively treated as a validated
// claude/codex subcommand invocation (see Instance.SubcommandPassthrough's
// doc, Claude review PR #1821 HIGH #1).
func ReadSubcommandPassthroughFromToolData(td json.RawMessage) bool {
	if len(td) == 0 {
		return false
	}
	var blob struct {
		SubcommandPassthrough bool `json:"subcommand_passthrough"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.SubcommandPassthrough
}
