package main

import (
	"encoding/json"
	"testing"
)

// Issue #2202: `agent-deck add --json` never carried a `started` field, so a
// caller had no way to tell "registered, not started" (add is registration-
// only — it never spawns tmux) from a payload shape that simply omitted the
// field. Pin the explicit `"started": false`.
func TestHandleAddJSON_ReportsStartedFalse(t *testing.T) {
	_, _, profile := setupAddDefaultPathTest(t)

	raw := captureStdout(t, func() {
		handleAdd(profile, []string{"--title", "issue-2202-add", "--json"})
	})

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("add --json output is not JSON (%v): %s", err, raw)
	}
	started, ok := payload["started"].(bool)
	if !ok {
		t.Fatalf("payload[\"started\"] = %v (%T), want bool false", payload["started"], payload["started"])
	}
	if started {
		t.Fatalf("payload[\"started\"] = true, want false: add only registers the session, it never spawns tmux")
	}
}
