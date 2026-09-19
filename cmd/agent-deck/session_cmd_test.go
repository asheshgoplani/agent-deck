package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestMCPInfoForJSON_NilOrEmpty(t *testing.T) {
	if got := mcpInfoForJSON(nil); got != nil {
		t.Fatalf("mcpInfoForJSON(nil) = %#v, want nil", got)
	}

	if got := mcpInfoForJSON(&session.MCPInfo{}); got != nil {
		t.Fatalf("mcpInfoForJSON(empty) = %#v, want nil", got)
	}
}

func TestMCPInfoForJSON_UsesSlicesAndIsMarshalable(t *testing.T) {
	info := &session.MCPInfo{
		Global:  []string{"global-a"},
		Project: []string{"project-a"},
		LocalMCPs: []session.LocalMCP{
			{Name: "local-a", SourcePath: "/tmp"},
		},
	}

	got := mcpInfoForJSON(info)
	if got == nil {
		t.Fatal("mcpInfoForJSON returned nil for populated MCP info")
	}

	local, ok := got["local"].([]string)
	if !ok {
		t.Fatalf("mcps.local type = %T, want []string", got["local"])
	}
	if len(local) != 1 || local[0] != "local-a" {
		t.Fatalf("mcps.local = %#v, want []string{\"local-a\"}", local)
	}

	payload := map[string]interface{}{"mcps": got}
	if _, err := json.Marshal(payload); err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
}

// TestRemotePrimerUnsupported locks the graceful degradation: an older
// remote's raw "unknown session command: primer" reads as one clear
// update-hint line instead of raw stderr plus its help text.
func TestRemotePrimerUnsupported(t *testing.T) {
	old := "Error: unknown session command: primer\nUsage: agent-deck session <command> [options]\n..."
	msg, ok := remotePrimerUnsupported("lab", []string{"session", "primer", "abc"}, 1, old)
	if !ok || strings.Count(msg, "\n") != 0 || !strings.Contains(msg, "lab") || !strings.Contains(msg, "session primer") {
		t.Fatalf("older remote must read as one clear line: %q %v", msg, ok)
	}
	if _, ok := remotePrimerUnsupported("lab", []string{"session", "primer", "abc"}, 1, "Error: session not found"); ok {
		t.Fatal("other remote errors must pass through")
	}
	if _, ok := remotePrimerUnsupported("lab", []string{"session", "show", "abc"}, 1, old); ok {
		t.Fatal("only session primer must trigger the graceful message")
	}
	if _, ok := remotePrimerUnsupported("lab", []string{"session", "primer", "abc"}, 0, old); ok {
		t.Fatal("exit code 0 must never trigger the graceful message")
	}
}

func TestIsSessionPrimerArgs(t *testing.T) {
	if !isSessionPrimerArgs([]string{"session", "primer", "abc"}) {
		t.Error("session primer abc should match")
	}
	if isSessionPrimerArgs([]string{"session", "metrics", "abc"}) {
		t.Error("session metrics must not match primer")
	}
	if isSessionPrimerArgs([]string{"primer"}) {
		t.Error("bare primer must not match")
	}
}
