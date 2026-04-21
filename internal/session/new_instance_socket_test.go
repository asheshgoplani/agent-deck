package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewInstance_SocketName_EmptyConfig is the zero-config guarantee:
// when `[tmux].socket_name` is absent, NewInstance must leave
// Instance.TmuxSocketName empty AND the underlying tmux.Session's
// SocketName empty. This is the backward-compat contract for every
// existing agent-deck install (v1.7.46 and older).
func TestNewInstance_SocketName_EmptyConfig(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0o700)
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstance("no-socket-session", tempDir)

	if inst.TmuxSocketName != "" {
		t.Fatalf("Instance.TmuxSocketName must be empty for zero-config install; got %q", inst.TmuxSocketName)
	}
	if ts := inst.GetTmuxSession(); ts == nil {
		t.Fatal("NewInstance must allocate a tmux.Session")
	} else if ts.SocketName != "" {
		t.Fatalf("tmux.Session.SocketName must be empty when no config socket is set; got %q", ts.SocketName)
	}
}

// TestNewInstance_SocketName_InheritsConfigValue is the primary wiring
// check: a `[tmux].socket_name = "agent-deck"` config must flow into both
// the Instance and the underlying tmux.Session at creation time. If this
// regressed, every new session would land on the default server even with
// the config set, silently defeating the whole feature.
func TestNewInstance_SocketName_InheritsConfigValue(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0o700)
	configContent := `
[tmux]
socket_name = "agent-deck"
`
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstance("isolated-session", tempDir)

	if inst.TmuxSocketName != "agent-deck" {
		t.Fatalf("Instance.TmuxSocketName must pick up [tmux].socket_name; got %q want %q", inst.TmuxSocketName, "agent-deck")
	}
	if ts := inst.GetTmuxSession(); ts == nil {
		t.Fatal("NewInstance must allocate a tmux.Session")
	} else if ts.SocketName != "agent-deck" {
		t.Fatalf("tmux.Session.SocketName must be seeded from config; got %q want %q", ts.SocketName, "agent-deck")
	}
}

// TestNewInstanceWithTool_SocketName_InheritsConfigValue mirrors the
// previous test for the tool-aware constructor used by `agent-deck add
// -c claude`. Both constructors must agree — otherwise claude-specific
// sessions would escape socket isolation.
func TestNewInstanceWithTool_SocketName_InheritsConfigValue(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0o700)
	configContent := `
[tmux]
socket_name = "ad-isolated"
`
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	inst := NewInstanceWithTool("claude-session", tempDir, "claude")

	if inst.TmuxSocketName != "ad-isolated" {
		t.Fatalf("NewInstanceWithTool must pick up config socket; got %q want %q", inst.TmuxSocketName, "ad-isolated")
	}
	if ts := inst.GetTmuxSession(); ts == nil || ts.SocketName != "ad-isolated" {
		var got string
		if ts != nil {
			got = ts.SocketName
		}
		t.Fatalf("tmux.Session.SocketName must be seeded from config for claude sessions; got %q want %q", got, "ad-isolated")
	}
}

// TestRecreateTmuxSession_PreservesSocketName is the regression test for
// the restart path. Instance.recreateTmuxSession is called on every
// Restart(); if it let the socket reset to the current config default
// (or empty), an existing session would be forked onto a DIFFERENT tmux
// server than the one holding its live pane, creating an invisible
// duplicate and stranding the original. This test pins the invariant:
// recreate keeps the instance-captured socket exactly as-is, regardless
// of what the current config says.
func TestRecreateTmuxSession_PreservesSocketName(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0o700)
	// Current config says one socket…
	configContent := `
[tmux]
socket_name = "new-socket"
`
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()

	// …but the Instance was created earlier under a different socket.
	inst := &Instance{
		ID:             "inst-existing",
		Title:          "legacy",
		ProjectPath:    tempDir,
		GroupPath:      "my-sessions",
		Tool:           "shell",
		Status:         StatusIdle,
		TmuxSocketName: "old-socket",
	}

	inst.recreateTmuxSession()

	if ts := inst.tmuxSession; ts == nil {
		t.Fatal("recreateTmuxSession must allocate a tmux.Session")
	} else if ts.SocketName != "old-socket" {
		t.Fatalf("recreateTmuxSession leaked new config socket onto existing instance; got %q want %q", ts.SocketName, "old-socket")
	}
}
