// Issue #1821: SubcommandPassthrough JSON helper.
//
// Mirrors the idle_timeout_persist.go pattern (#1143): merges / extracts a
// single boolean on the tool_data blob without changing the positional
// MarshalToolData / UnmarshalToolData signatures or requiring a SQL schema
// migration. The MergeToolDataExtras layer in statedb preserves keys
// outside the typed schema across INSERT OR REPLACE, so a row written by an
// old binary (without this key) survives a round-trip through a new binary,
// and vice versa.
package session

import "encoding/json"

const toolDataSubcommandPassthroughKey = "subcommand_passthrough"

// WriteSubcommandPassthroughToToolData merges subcommand_passthrough into
// the given tool_data JSON blob. Passing false removes the key (keeps the
// blob shape identical to a pre-#1821 row, so downgrades stay clean).
func WriteSubcommandPassthroughToToolData(td json.RawMessage, passthrough bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if passthrough {
		raw, _ := json.Marshal(passthrough)
		m[toolDataSubcommandPassthroughKey] = raw
	} else {
		delete(m, toolDataSubcommandPassthroughKey)
	}
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
