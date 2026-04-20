package session

// S8 defense-in-depth layer: even if the shell-level `unset
// TELEGRAM_STATE_DIR` in buildEnvSourceCommand is somehow bypassed
// (corrupted env_file, wrapper rewriting the sources chain, a future
// refactor that relocates the strip), the claude process itself MUST
// NOT see TELEGRAM_STATE_DIR for non-channel-owning sessions. We
// achieve this by wrapping the final claude invocation in
// `env -u TELEGRAM_STATE_DIR ` so the child process is spawned with
// the variable explicitly cleared regardless of the parent shell
// state.
//
// Two layers intentionally — shell unset + exec-level unset — so
// either one is load-bearing on its own.

import (
	"strings"
	"testing"
)

// Fresh-start path (new session, no resume) MUST prefix the exec
// with `env -u TELEGRAM_STATE_DIR` for non-channel-owning claude
// sessions.
func TestS8_ExecLayer_FreshStart_UnsetTSDInvocation(t *testing.T) {
	cfg := &UserConfig{MCPs: make(map[string]MCPDef)}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		ID:          "id-1",
		Title:       "launch-child",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	cmd := child.buildClaudeCommandWithMessage("claude", "")

	if !strings.Contains(cmd, "env -u TELEGRAM_STATE_DIR") {
		t.Errorf("fresh-start claude exec must be prefixed with `env -u TELEGRAM_STATE_DIR`\ncmd = %q", cmd)
	}
}

// Continue mode (`claude -c`) MUST also apply the exec-layer unset
// for non-channel-owning sessions.
func TestS8_ExecLayer_ContinueMode_UnsetTSDInvocation(t *testing.T) {
	cfg := &UserConfig{MCPs: make(map[string]MCPDef)}
	defer resetUserConfigCache(t, cfg)()

	opts := &ClaudeOptions{SessionMode: "continue"}
	child := &Instance{
		ID:          "id-2",
		Title:       "launch-child",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}
	_ = child.SetClaudeOptions(opts)

	cmd := child.buildClaudeCommandWithMessage("claude", "")

	if !strings.Contains(cmd, "env -u TELEGRAM_STATE_DIR") {
		t.Errorf("continue-mode claude exec must be prefixed with `env -u TELEGRAM_STATE_DIR`\ncmd = %q", cmd)
	}
}

// Resume picker (`claude -r`) MUST apply the exec-layer unset for
// non-channel-owning sessions.
func TestS8_ExecLayer_ResumePicker_UnsetTSDInvocation(t *testing.T) {
	cfg := &UserConfig{MCPs: make(map[string]MCPDef)}
	defer resetUserConfigCache(t, cfg)()

	opts := &ClaudeOptions{SessionMode: "resume"}
	child := &Instance{
		ID:          "id-3",
		Title:       "launch-child",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}
	_ = child.SetClaudeOptions(opts)

	cmd := child.buildClaudeCommandWithMessage("claude", "")

	if !strings.Contains(cmd, "env -u TELEGRAM_STATE_DIR") {
		t.Errorf("resume-picker claude exec must be prefixed with `env -u TELEGRAM_STATE_DIR`\ncmd = %q", cmd)
	}
}

// Conductor session must NOT get the exec-layer unset — conductors
// legitimately own the telegram bot token.
func TestS8_ExecLayer_Conductor_NoUnsetInvocation(t *testing.T) {
	cfg := &UserConfig{MCPs: make(map[string]MCPDef)}
	defer resetUserConfigCache(t, cfg)()

	conductor := &Instance{
		ID:          "id-c1",
		Title:       "conductor-travel",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	cmd := conductor.buildClaudeCommandWithMessage("claude", "")

	if strings.Contains(cmd, "env -u TELEGRAM_STATE_DIR") {
		t.Errorf("conductor-* claude exec must NOT be prefixed with `env -u TELEGRAM_STATE_DIR`\ncmd = %q", cmd)
	}
}

// Session with explicit telegram channel ownership must NOT get the
// exec-layer unset.
func TestS8_ExecLayer_TelegramChannelOwner_NoUnsetInvocation(t *testing.T) {
	cfg := &UserConfig{MCPs: make(map[string]MCPDef)}
	defer resetUserConfigCache(t, cfg)()

	owner := &Instance{
		ID:          "id-o1",
		Title:       "bot-owner",
		Tool:        "claude",
		ProjectPath: "/tmp",
		Channels:    []string{"plugin:telegram@claude-plugins-official"},
	}

	cmd := owner.buildClaudeCommandWithMessage("claude", "")

	if strings.Contains(cmd, "env -u TELEGRAM_STATE_DIR") {
		t.Errorf("telegram channel owner claude exec must NOT be prefixed with `env -u TELEGRAM_STATE_DIR`\ncmd = %q", cmd)
	}
}

// When a message is provided, the exec-layer unset must still prefix
// the claude invocation (not the background tmux send-keys subshell).
func TestS8_ExecLayer_FreshStartWithMessage_UnsetOnExecOnly(t *testing.T) {
	cfg := &UserConfig{MCPs: make(map[string]MCPDef)}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		ID:          "id-m1",
		Title:       "launch-child",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	cmd := child.buildClaudeCommandWithMessage("claude", "hello world")

	// The final exec must carry the unset.
	if !strings.Contains(cmd, "env -u TELEGRAM_STATE_DIR") {
		t.Errorf("fresh-start-with-message claude exec must be prefixed with `env -u TELEGRAM_STATE_DIR`\ncmd = %q", cmd)
	}
	// Sanity check: the claude exec should still follow `exec `.
	if !strings.Contains(cmd, "exec env -u TELEGRAM_STATE_DIR ") && !strings.Contains(cmd, "exec  env -u TELEGRAM_STATE_DIR ") {
		// Allow either one or two spaces after `exec` to be flexible about the
		// exact composition without locking in spacing.
		if !strings.Contains(cmd, "exec ") {
			t.Errorf("fresh-start path lost its `exec` wrapper\ncmd = %q", cmd)
		}
	}
}
