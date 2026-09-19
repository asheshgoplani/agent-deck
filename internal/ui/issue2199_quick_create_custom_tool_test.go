package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue2199_QuickCreatePreservesCustomTool verifies that quick-creating
// a session via quickCreateSession inherits the custom tool name rather than
// downgrading to the underlying binary (issue #2199).
func TestIssue2199_QuickCreatePreservesCustomTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}

	cfgContent := `
[tools.claude-qwen]
command = "claude"
compatible_with = "claude"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	if def := session.GetToolDef("claude-qwen"); def == nil {
		t.Fatal("expected claude-qwen tool definition to be loaded, got nil")
	}

	t.Run("cursor on custom tool session", func(t *testing.T) {
		projectDir := t.TempDir()
		sourceInst := session.NewInstanceWithGroupAndTool("orig-session", projectDir, "test-group", "claude-qwen")
		sourceInst.Command = "claude"
		sourceInst.CreatedAt = time.Now().Add(-1 * time.Minute)

		h := &Home{
			instances: []*session.Instance{sourceInst},
			flatItems: []session.Item{
				{
					Type:    session.ItemTypeSession,
					Session: sourceInst,
				},
			},
			cursor: 0,
		}

		cmd := h.quickCreateSession()
		if cmd == nil {
			t.Fatal("quickCreateSession returned nil cmd")
		}

		msg := cmd()
		createMsg, ok := msg.(sessionCreatedMsg)
		if !ok {
			t.Fatalf("quickCreateSession returned %T, want sessionCreatedMsg", msg)
		}
		if createMsg.err != nil {
			t.Fatalf("create session failed: %v", createMsg.err)
		}
		inst := createMsg.instance
		if inst == nil {
			t.Fatal("created instance is nil")
		}
		t.Cleanup(func() {
			_ = inst.KillAndWait()
		})

		if inst.Tool != "claude-qwen" {
			t.Errorf("Tool = %q, want %q (custom tool identity lost)", inst.Tool, "claude-qwen")
		}
		if inst.Command != "claude" {
			t.Errorf("Command = %q, want %q", inst.Command, "claude")
		}
	})

	t.Run("cursor on custom tool session whose session command was edited before quick-create", func(t *testing.T) {
		projectDir := t.TempDir()
		sourceInst := session.NewInstanceWithGroupAndTool("edited-session", projectDir, "test-group", "claude-qwen")
		// Simulate an explicit session command override (e.g. via session set <id> command ...)
		sourceInst.Command = "my-custom-wrapper --model qwen-turbo"
		sourceInst.CreatedAt = time.Now().Add(-1 * time.Minute)

		h := &Home{
			instances: []*session.Instance{sourceInst},
			flatItems: []session.Item{
				{
					Type:    session.ItemTypeSession,
					Session: sourceInst,
				},
			},
			cursor: 0,
		}

		cmd := h.quickCreateSession()
		if cmd == nil {
			t.Fatal("quickCreateSession returned nil cmd")
		}

		msg := cmd()
		createMsg, ok := msg.(sessionCreatedMsg)
		if !ok {
			t.Fatalf("quickCreateSession returned %T, want sessionCreatedMsg", msg)
		}
		if createMsg.err != nil {
			t.Fatalf("create session failed: %v", createMsg.err)
		}
		inst := createMsg.instance
		if inst == nil {
			t.Fatal("created instance is nil")
		}
		t.Cleanup(func() {
			_ = inst.KillAndWait()
		})

		// Both custom tool identity and explicit command override must be preserved.
		if inst.Tool != "claude-qwen" {
			t.Errorf("Tool = %q, want %q (custom tool identity lost)", inst.Tool, "claude-qwen")
		}
		if inst.Command != "my-custom-wrapper --model qwen-turbo" {
			t.Errorf("Command = %q, want %q (explicit command override lost)", inst.Command, "my-custom-wrapper --model qwen-turbo")
		}
	})

	t.Run("cursor on group header whose most recent session is custom tool", func(t *testing.T) {
		projectDir := t.TempDir()
		sourceInst := session.NewInstanceWithGroupAndTool("group-session", projectDir, "my-group", "claude-qwen")
		sourceInst.Command = "claude"
		sourceInst.CreatedAt = time.Now().Add(-1 * time.Minute)

		group := &session.Group{
			Path: "my-group",
			Name: "my-group",
		}

		h := &Home{
			instances: []*session.Instance{sourceInst},
			flatItems: []session.Item{
				{
					Type:  session.ItemTypeGroup,
					Group: group,
				},
				{
					Type:    session.ItemTypeSession,
					Session: sourceInst,
				},
			},
			cursor: 0, // cursor on group header
		}

		cmd := h.quickCreateSession()
		if cmd == nil {
			t.Fatal("quickCreateSession returned nil cmd")
		}

		msg := cmd()
		createMsg, ok := msg.(sessionCreatedMsg)
		if !ok {
			t.Fatalf("quickCreateSession returned %T, want sessionCreatedMsg", msg)
		}
		if createMsg.err != nil {
			t.Fatalf("create session failed: %v", createMsg.err)
		}
		inst := createMsg.instance
		if inst == nil {
			t.Fatal("created instance is nil")
		}
		t.Cleanup(func() {
			_ = inst.KillAndWait()
		})

		if inst.Tool != "claude-qwen" {
			t.Errorf("Tool = %q, want %q (custom tool identity lost from group header)", inst.Tool, "claude-qwen")
		}
		if inst.Command != "claude" {
			t.Errorf("Command = %q, want %q", inst.Command, "claude")
		}
	})

	t.Run("cursor on remote session row", func(t *testing.T) {
		t.Skip("quick creation does not apply to remote rows: quickCreateSession is local-only and spawns a local tmux session via createSessionInGroupWithWorktreeAndOptions; remote rows (ItemTypeRemoteSession) do not populate sourceSession (*session.Instance)")
	})
}

// TestIssue2199_QuickCreate_RemoteSessionNotApplicable documents, per the
// internal/ui RemoteSession guideline, why quickCreateSession does not inherit
// from or apply to RemoteSession rows.
func TestIssue2199_QuickCreate_RemoteSessionNotApplicable(t *testing.T) {
	t.Skip("RemoteSession N/A: quickCreateSession is local-only by design and spawns " +
		"a local tmux instance via createSessionInGroupWithWorktreeAndOptions. " +
		"Cursor on ItemTypeRemoteSession rows does not set sourceSession (*session.Instance), " +
		"so remote sessions cannot provide custom tool templates for local quick-create.")
}

// TestIssue2199_QuickCreateSessionAt_CustomDefaultTool verifies that
// quickCreateSessionAt resolves the configured executable command when
// default_tool is a custom tool or cursor, rather than assigning the bare tool identifier.
func TestIssue2199_QuickCreateSessionAt_CustomDefaultTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}

	cfgContent := `
default_tool = "claude-qwen"

[tools.claude-qwen]
command = "qwen-wrapped-binary --some-flag"
compatible_with = "claude"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	t.Run("custom default tool resolves configured command", func(t *testing.T) {
		h := &Home{}
		projectDir := t.TempDir()

		cmd := h.quickCreateSessionAt(projectDir)
		if cmd == nil {
			t.Fatal("quickCreateSessionAt returned nil cmd")
		}

		msg := cmd()
		createMsg, ok := msg.(sessionCreatedMsg)
		if !ok {
			t.Fatalf("quickCreateSessionAt returned %T, want sessionCreatedMsg", msg)
		}
		if createMsg.err != nil {
			t.Fatalf("create session failed: %v", createMsg.err)
		}
		inst := createMsg.instance
		if inst == nil {
			t.Fatal("created instance is nil")
		}
		t.Cleanup(func() {
			_ = inst.KillAndWait()
		})

		if inst.Tool != "claude-qwen" {
			t.Errorf("Tool = %q, want %q", inst.Tool, "claude-qwen")
		}
		if inst.Command != "qwen-wrapped-binary --some-flag" {
			t.Errorf("Command = %q, want %q (bare tool identifier assigned instead of configured command)", inst.Command, "qwen-wrapped-binary --some-flag")
		}
	})

	t.Run("cursor default tool resolves configured cursor command", func(t *testing.T) {
		cfgContentCursor := `
default_tool = "cursor"
`
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(cfgContentCursor), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		session.ClearUserConfigCache()

		h := &Home{}
		projectDir := t.TempDir()

		cmd := h.quickCreateSessionAt(projectDir)
		if cmd == nil {
			t.Fatal("quickCreateSessionAt returned nil cmd")
		}

		msg := cmd()
		createMsg, ok := msg.(sessionCreatedMsg)
		if !ok {
			t.Fatalf("quickCreateSessionAt returned %T, want sessionCreatedMsg", msg)
		}
		if createMsg.err != nil {
			t.Fatalf("create session failed: %v", createMsg.err)
		}
		inst := createMsg.instance
		if inst == nil {
			t.Fatal("created instance is nil")
		}
		t.Cleanup(func() {
			_ = inst.KillAndWait()
		})

		wantCmd := session.GetToolCommand("cursor")
		if inst.Tool != "cursor" {
			t.Errorf("Tool = %q, want %q", inst.Tool, "cursor")
		}
		if inst.Command != wantCmd {
			t.Errorf("Command = %q, want %q", inst.Command, wantCmd)
		}
	})
}
