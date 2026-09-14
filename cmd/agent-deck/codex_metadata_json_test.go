package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `list --json` must expose codex_session_id and resolved_codex_home for Codex
// sessions so a consumer can find the session's rollout JSONL. Non-Codex
// sessions carry neither key, and SSH Codex sessions carry the id but not a
// local rollout root (that path only exists on the remote host).
func TestBuildListJSON_CodexMetadata(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	instances := []*session.Instance{
		{ID: "codex-local", Title: "codex-local", Tool: "codex", CodexSessionID: "019a-local"},
		{ID: "codex-ssh", Title: "codex-ssh", Tool: "codex", CodexSessionID: "019a-remote", SSHHost: "build-host"},
		{ID: "claude", Title: "claude", Tool: "claude"},
	}
	out, err := buildListJSON("_test", instances)
	if err != nil {
		t.Fatalf("buildListJSON: %v", err)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	byID := map[string]map[string]interface{}{}
	for _, r := range rows {
		byID[r["id"].(string)] = r
	}

	local := byID["codex-local"]
	if got := local["codex_session_id"]; got != "019a-local" {
		t.Errorf("codex-local codex_session_id = %v, want 019a-local", got)
	}
	if got := local["resolved_codex_home"]; got != codexHome {
		t.Errorf("codex-local resolved_codex_home = %v, want %s", got, codexHome)
	}

	remote := byID["codex-ssh"]
	if got := remote["codex_session_id"]; got != "019a-remote" {
		t.Errorf("codex-ssh codex_session_id = %v, want 019a-remote", got)
	}
	if got, present := remote["resolved_codex_home"]; present {
		t.Errorf("codex-ssh resolved_codex_home must be omitted, got %v", got)
	}

	for _, key := range []string{"codex_session_id", "resolved_codex_home"} {
		if got, present := byID["claude"][key]; present {
			t.Errorf("non-Codex session must omit %s, got %v", key, got)
		}
	}
}

// `session show --json` carries the same two keys, with the same omission
// rules as `list --json`.
func TestAddCodexMetadataJSON(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	local := map[string]interface{}{}
	addCodexMetadataJSON(local, &session.Instance{Tool: "codex", CodexSessionID: "019a-local"})
	if local["codex_session_id"] != "019a-local" || local["resolved_codex_home"] != codexHome {
		t.Fatalf("local codex show JSON = %v", local)
	}

	remote := map[string]interface{}{}
	addCodexMetadataJSON(remote, &session.Instance{Tool: "codex", CodexSessionID: "019a-remote", SSHHost: "build-host"})
	if _, present := remote["resolved_codex_home"]; present || remote["codex_session_id"] != "019a-remote" {
		t.Fatalf("ssh codex show JSON = %v", remote)
	}

	other := map[string]interface{}{}
	addCodexMetadataJSON(other, &session.Instance{Tool: "claude", CodexSessionID: "stray"})
	if len(other) != 0 {
		t.Fatalf("non-Codex show JSON must add nothing, got %v", other)
	}
}
