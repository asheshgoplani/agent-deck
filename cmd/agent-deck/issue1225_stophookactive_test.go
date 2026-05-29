package main

// Audit B8 / GAP §5 — the MaxStopHookBlocks loop guard's RESET depends entirely
// on Claude Code sending stop_hook_active=false on a genuine user turn. Go's JSON
// unmarshal defaults a MISSING bool to false, which would silently reset the
// budget every Stop and defeat the guard (token-burn loop). Fail safe: an ABSENT
// flag is treated as active=true (counts against the budget); only an EXPLICIT
// false resets it.

import (
	"encoding/json"
	"testing"
)

func TestB8_StopHookActive_AbsentFailsSafeToActive(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"absent_field", `{"hook_event_name":"Stop","session_id":"s"}`, true},
		{"explicit_false", `{"hook_event_name":"Stop","stop_hook_active":false}`, false},
		{"explicit_true", `{"hook_event_name":"Stop","stop_hook_active":true}`, true},
		{"empty_payload", `{}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var p hookPayload
			if err := json.Unmarshal([]byte(c.raw), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := resolveStopHookActive(p); got != c.want {
				t.Fatalf("resolveStopHookActive(%s) = %v, want %v", c.raw, got, c.want)
			}
		})
	}
}
