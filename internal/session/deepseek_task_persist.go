// DeepSeek headless-task persistence (PR #1942 review, P1c).
//
// Follows the idle_timeout_persist.go pattern (#1143) for WHERE the value lives:
// the tool_data blob, without changing the positional MarshalToolData /
// UnmarshalToolData signatures or requiring a SQL schema migration.
//
// It DOES follow idle_timeout's "delete the key at the zero value" convention,
// unlike subcommand_passthrough (#1821). The reasoning there was that an
// explicit `false` is a meaningful assertion that must not be resurrected by
// MergeToolDataExtras. Here the zero value is an empty task, and "no task
// recorded" and "an empty task recorded" describe the same unlaunchable
// invocation — `dsh --profile headless ""` is the same usage error as
// `dsh --profile headless`. There is nothing for a stale value to silently
// re-enable: a carried-forward task can only ever be replayed as itself, and
// CanRestart already refuses when none is known.
package session

import (
	"encoding/json"
	"strings"
)

const toolDataDeepSeekTaskKey = "deepseek_task"

// WriteDeepSeekTaskToToolData merges the headless task into the given tool_data
// blob, removing the key when the task is empty. Sibling keys are preserved.
func WriteDeepSeekTaskToToolData(td json.RawMessage, task string) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	trimmed := strings.TrimSpace(task)
	if trimmed == "" {
		delete(m, toolDataDeepSeekTaskKey)
	} else {
		raw, err := json.Marshal(trimmed)
		if err != nil {
			return td
		}
		m[toolDataDeepSeekTaskKey] = raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadDeepSeekTaskFromToolData extracts the headless task from the blob.
// Returns "" for missing, malformed, or legacy rows — the honest default, and
// the reason CanRestart can tell "this one-shot can be replayed" from "this row
// predates the field, so promising a restart would land on a usage error".
func ReadDeepSeekTaskFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(td, &m); err != nil {
		return ""
	}
	raw, ok := m[toolDataDeepSeekTaskKey]
	if !ok {
		return ""
	}
	var task string
	if err := json.Unmarshal(raw, &task); err != nil {
		return ""
	}
	return strings.TrimSpace(task)
}
