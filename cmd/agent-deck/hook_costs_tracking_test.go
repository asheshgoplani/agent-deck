package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestWriteCostEvent_TrackingDisabled(t *testing.T) {
	initTestLogging(t)
	for _, tc := range []struct {
		name, config string
		want         bool
	}{
		{"global off", "[costs]\nenabled=false\n", false},
		{"profile off", "[costs]\nenabled=true\n[profiles.personal.costs]\nenabled=false\n", false},
		{"profile overrides global off", "[costs]\nenabled=false\n[profiles.personal.costs]\nenabled=true\n", true},
		{"default on", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, personal, _ := hookHome(t, true)
			configPath := filepath.Join(home, "xdg_config_home", "agent-deck", "config.toml")
			original, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, append(original, []byte("\n"+tc.config)...), 0600); err != nil {
				t.Fatal(err)
			}
			session.ClearUserConfigCache()
			mine := filepath.Join(personal, "projects", "p", "aaaaaaaa-0000-4000-8000-000000000001.jsonl")
			writeTurn(t, mine, "aaaaaaaa-0000-4000-8000-000000000001")
			writeCostEvent("inst-1", []byte(fmt.Sprintf(`{"hook_event_name":"Stop","transcript_path":%q}`, mine)))
			files := costEventFiles(t)
			if (len(files) == 1) != tc.want {
				t.Fatalf("cost events=%v, want written=%v", files, tc.want)
			}
		})
	}
}

// Review M3: the hook's cost event carries the transcript record it read, so
// the TUI's watcher stores it under the id the recall importer uses for that
// record ("<instance>_<record uuid>") and the turn is not counted twice.
func TestWriteCostEventCarriesTheRecordUUID(t *testing.T) {
	initTestLogging(t)
	_, personal, _ := hookHome(t, true)
	session.ClearUserConfigCache()
	mine := filepath.Join(personal, "projects", "p", "aaaaaaaa-0000-4000-8000-000000000002.jsonl")
	writeTurn(t, mine, "aaaaaaaa-0000-4000-8000-000000000002")
	writeCostEvent("inst-1", []byte(fmt.Sprintf(`{"hook_event_name":"Stop","transcript_path":%q}`, mine)))
	files := costEventFiles(t)
	if len(files) != 1 {
		t.Fatalf("cost events = %v, want one", files)
	}
	data, err := os.ReadFile(filepath.Join(getCostEventsDir(), files[0]))
	if err != nil {
		t.Fatal(err)
	}
	var raw costs.RawCostEvent
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.MessageUUID != "a1" {
		t.Fatalf("message_uuid = %q, want a1 (the main-chain assistant record)", raw.MessageUUID)
	}
	if got := costs.HookCostEventID(raw.InstanceID, raw.MessageID, raw.MessageUUID, raw.Timestamp); got != "inst-1_a1" {
		t.Fatalf("cost event id = %q, want inst-1_a1", got)
	}
}

// The hook keys its cost event on the API message id, the key the importer
// counts a split message under.
func TestWriteCostEventCarriesTheMessageID(t *testing.T) {
	initTestLogging(t)
	_, personal, _ := hookHome(t, true)
	session.ClearUserConfigCache()
	mine := filepath.Join(personal, "projects", "p", "aaaaaaaa-0000-4000-8000-000000000003.jsonl")
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u1","timestamp":"2026-09-19T10:00:00.000Z","sessionId":"aaaaaaaa-0000-4000-8000-000000000003","cwd":"/tmp/p"}
{"type":"assistant","message":{"id":"msg_01","role":"assistant","model":"claude-test","content":[{"type":"thinking","thinking":"t"}],"usage":{"input_tokens":10,"output_tokens":5}},"uuid":"a1","timestamp":"2026-09-19T10:00:01.000Z","sessionId":"aaaaaaaa-0000-4000-8000-000000000003"}
{"type":"assistant","message":{"id":"msg_01","role":"assistant","model":"claude-test","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":5}},"uuid":"a2","timestamp":"2026-09-19T10:00:01.100Z","sessionId":"aaaaaaaa-0000-4000-8000-000000000003"}
`
	if err := os.WriteFile(mine, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCostEvent("inst-1", []byte(fmt.Sprintf(`{"hook_event_name":"Stop","transcript_path":%q}`, mine)))
	files := costEventFiles(t)
	if len(files) != 1 {
		t.Fatalf("cost events = %v, want one", files)
	}
	data, _ := os.ReadFile(filepath.Join(getCostEventsDir(), files[0]))
	var raw costs.RawCostEvent
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if got := costs.HookCostEventID(raw.InstanceID, raw.MessageID, raw.MessageUUID, raw.Timestamp); got != "inst-1_msg_01" {
		t.Fatalf("cost event id = %q, want inst-1_msg_01", got)
	}
}
