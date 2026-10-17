package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestConditionalArchiveBinaryPreservesActiveAndArchivesCompletedWorker(t *testing.T) {
	sb := harness.NewSandbox(t)
	const id, thread = "archive-worker", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	profile := filepath.Join(sb.Home, ".agent-deck", "profiles", "default")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(filepath.Join(profile, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	row := &statedb.InstanceRow{
		ID: id, Title: id, ParentSessionID: "parent", GroupPath: "group",
		ProjectPath: sb.Home, Tool: "codex", Command: "codex", Status: "waiting",
		TmuxSession: "agentdeck_archive_fixture", CreatedAt: time.Now().Add(-time.Hour),
		ToolData: json.RawMessage(`{"codex_session_id":"` + thread + `"}`),
	}
	if err := db.SaveInstance(row); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	codexHome := filepath.Join(sb.Home, ".codex")
	transcript := filepath.Join(codexHome, "sessions", "2026", "10", "10", "rollout-fixture-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0700); err != nil {
		t.Fatal(err)
	}
	complete := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn","last_agent_message":"===AGENTDECK_DONE=== status=ok summary=fixture"}}` + "\n"
	if err := os.WriteFile(transcript, []byte(complete), 0600); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(sb.Home, ".agent-deck", "hooks")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	hook, _ := json.Marshal(map[string]any{
		"status": "waiting", "session_id": thread, "event": "Stop", "ts": time.Now().Unix(),
		"codex_started_generation":   thread + ":turn",
		"codex_completed_generation": thread + ":turn",
		"codex_started_session_id":   thread, "codex_completed_session_id": thread,
	})
	if err := os.WriteFile(filepath.Join(hooks, id+".json"), hook, 0600); err != nil {
		t.Fatal(err)
	}
	shim := `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/archive-tmux-calls"
while [ "$#" -gt 0 ]; do
 case "$1" in
  has-session) [ ! -f "$HOME/archived-fixture" ]; exit $?;;
  kill-session) touch "$HOME/archived-fixture"; exit 0;;
  list-sessions) printf 'agentdeck_archive_fixture\n'; exit 0;;
  list-panes) printf '0\n'; exit 0;;
  show-environment) case "$*" in *CODEX_SESSION_ID*) printf 'CODEX_SESSION_ID=aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa\n';; esac; exit 0;;
  capture-pane) printf 'Ask Codex to do anything\n'; exit 0;;
  display-message) printf '0\n'; exit 0;;
 esac
 shift
done
exit 0
`
	if err := os.WriteFile(filepath.Join(sb.ShimDir, "tmux"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	env := append(sb.Env(), "CODEX_HOME="+codexHome)
	run := func(args ...string) ([]byte, error) {
		t.Helper()
		command := exec.Command(sb.BinPath, args...)
		command.Env = env
		return command.CombinedOutput()
	}
	output, err := run("session", "output", "--json", id)
	if err != nil {
		t.Fatalf("read fixture: %v %s", err, output)
	}
	var response struct {
		Version string `json:"content_version"`
	}
	if err := json.Unmarshal(output, &response); err != nil || response.Version == "" {
		t.Fatalf("fixture version unavailable: %s", output)
	}
	for _, parent := range []string{"wrong", "parent"} {
		version := response.Version
		if parent == "parent" {
			version = "stale"
		}
		if output, err := run("session", "archive", "--json", "--expected-parent", parent,
			"--expected-content-version", version, id); err == nil {
			t.Fatalf("unsafe archive accepted: %s", output)
		}
	}
	if _, err := os.Stat(filepath.Join(sb.Home, "archived-fixture")); err == nil {
		t.Fatal("refused archive killed fixture")
	}
	output, err = run("session", "archive", "--json", "--expected-parent", "parent",
		"--expected-content-version", response.Version, id)
	if err != nil {
		t.Fatalf("exact completed fixture archive: %v %s", err, output)
	}
	stored, err := db.LoadInstanceByID(id)
	if err != nil || stored == nil || stored.ArchivedAt.IsZero() {
		t.Fatalf("archive not persisted: %+v %v", stored, err)
	}
	calls, err := os.ReadFile(filepath.Join(sb.Home, "archive-tmux-calls"))
	if err != nil || strings.Count(string(calls), "kill-session") != 1 {
		t.Fatalf("archive calls: %s %v", calls, err)
	}
}
