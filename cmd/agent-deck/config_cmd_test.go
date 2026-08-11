package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestHandleConfigOrchestrate_PrintsResolvedPolicy(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	path := filepath.Join(configDir, "agent-deck", session.UserConfigFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("default_tool = \"codex\"\n\n[orchestrate]\ntool_strategy = \"default\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { handleConfig([]string{"orchestrate"}) })
	var got session.OrchestrateToolPolicy
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got.Strategy != "default" || got.FallbackTool != "codex" {
		t.Fatalf("policy = %+v, want strategy=default fallback=codex", got)
	}
}
