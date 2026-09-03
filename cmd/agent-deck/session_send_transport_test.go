package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// alwaysSocketOK is a resolve stub that always succeeds, so any row using it
// reaches chooseSendTransport's final "otherwise" branch when nothing earlier
// routes to tmux. resolveCalls counts invocations so tests can assert
// resolve runs exactly once per chooseSendTransport call (NIT: one resolve
// per send, not two).
func alwaysSocketOK(resolveCalls *int) func(string) (send.ClaudeSocketTarget, error) {
	return func(sessionID string) (send.ClaudeSocketTarget, error) {
		if resolveCalls != nil {
			*resolveCalls++
		}
		return send.ClaudeSocketTarget{SocketPath: "/tmp/whatever.sock", Pid: 1, SessionID: sessionID}, nil
	}
}

// alwaysSocketUnavailable is a resolve stub simulating a specific pre-write
// refusal, e.g. a dead pid.
func alwaysSocketUnavailable(reason send.UnavailableReason) func(string) (send.ClaudeSocketTarget, error) {
	return func(string) (send.ClaudeSocketTarget, error) {
		return send.ClaudeSocketTarget{}, &send.Unavailable{Reason: reason}
	}
}

func TestChooseSendTransport(t *testing.T) {
	cases := []struct {
		name          string
		in            transportInputs
		wantTransport sendTransport
		wantReason    send.UnavailableReason
	}{
		{
			name: "SSH-backed instance always takes tmux, even with an otherwise-good record",
			in: transportInputs{
				tool: "claude", configValue: "auto", message: "hello",
				claudeSessionID: "sid", isSSH: true, resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportTmux, wantReason: reasonRemoteSession,
		},
		{
			name: "explicit tmux pin wins over an otherwise-good record",
			in: transportInputs{
				tool: "claude", configValue: "tmux", message: "hello",
				claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportTmux, wantReason: reasonConfigPinnedTmux,
		},
		{
			name: "codex is not Claude-compatible",
			in: transportInputs{
				tool: "codex", configValue: "auto", message: "hello",
				claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportTmux, wantReason: reasonNotClaudeCompatible,
		},
		{
			name: "opencode is not Claude-compatible",
			in: transportInputs{
				tool: "opencode", configValue: "auto", message: "hello",
				claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportTmux, wantReason: reasonNotClaudeCompatible,
		},
		{
			name: "bare slash command routes to tmux",
			in: transportInputs{
				tool: "claude", configValue: "auto", message: "/compact",
				claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportTmux, wantReason: reasonSlashCommand,
		},
		{
			name: "slash command with leading whitespace still routes to tmux",
			in: transportInputs{
				tool: "claude", configValue: "auto", message: "  /help",
				claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportTmux, wantReason: reasonSlashCommand,
		},
		{
			name: "a path that merely starts with / is not a slash command message here (message body, not composer literal) but still matches the safe prefix rule",
			in: transportInputs{
				tool: "claude", configValue: "auto", message: "/Users/tarek/notes.md please read this",
				claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
			},
			// §7.5: the predicate is a prefix match, so a message beginning
			// with an absolute path also routes to tmux. Safe direction
			// (status quo), documented quirk.
			wantTransport: transportTmux, wantReason: reasonSlashCommand,
		},
		{
			name: "plain text is not a slash command",
			in: transportInputs{
				tool: "claude", configValue: "auto", message: "not/a/slash because it has no leading slash",
				claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportSocket, wantReason: "",
		},
		{
			name: "no known claude session id routes to tmux",
			in: transportInputs{
				tool: "claude", configValue: "auto", message: "hello",
				claudeSessionID: "", resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportTmux, wantReason: reasonNoClaudeSessionID,
		},
		{
			name: "resolve() unavailable (dead pid) surfaces that exact reason",
			in: transportInputs{
				tool: "claude", configValue: "auto", message: "hello",
				claudeSessionID: "sid", resolve: alwaysSocketUnavailable(send.ReasonDeadPid),
			},
			wantTransport: transportTmux, wantReason: send.ReasonDeadPid,
		},
		{
			name: "resolve() unavailable (old protocol) surfaces that exact reason",
			in: transportInputs{
				tool: "claude", configValue: "auto", message: "hello",
				claudeSessionID: "sid", resolve: alwaysSocketUnavailable(send.ReasonOldProtocol),
			},
			wantTransport: transportTmux, wantReason: send.ReasonOldProtocol,
		},
		{
			name: "everything clear -> socket, no reason",
			in: transportInputs{
				tool: "claude", configValue: "auto", message: "do the thing",
				claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportSocket, wantReason: "",
		},
		{
			name: "config value empty string behaves like auto",
			in: transportInputs{
				tool: "claude", configValue: "", message: "do the thing",
				claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
			},
			wantTransport: transportSocket, wantReason: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTransport, gotReason, gotTarget := chooseSendTransport(tc.in)
			if gotTransport != tc.wantTransport {
				t.Errorf("transport = %q, want %q", gotTransport, tc.wantTransport)
			}
			if gotReason != tc.wantReason {
				t.Errorf("reason = %q, want %q", gotReason, tc.wantReason)
			}
			if tc.wantTransport == transportSocket && gotTarget.SocketPath == "" {
				t.Errorf("expected a resolved target on a socket decision, got zero value")
			}
			if tc.wantTransport == transportTmux && gotTarget != (send.ClaudeSocketTarget{}) {
				t.Errorf("expected a zero-value target on a tmux decision, got %+v", gotTarget)
			}
		})
	}
}

// TestChooseSendTransport_ResolvesExactlyOnce guards the NIT fix: earlier
// code called resolve() once inside chooseSendTransport and again inside
// executeSocketSend, doubling the ~/.claude/sessions scan and the `ps`
// fork per socket send. chooseSendTransport must be the only caller.
func TestChooseSendTransport_ResolvesExactlyOnce(t *testing.T) {
	var calls int
	transport, _, target := chooseSendTransport(transportInputs{
		tool: "claude", configValue: "auto", message: "hello",
		claudeSessionID: "sid", resolve: alwaysSocketOK(&calls),
	})
	if transport != transportSocket {
		t.Fatalf("transport = %q, want socket", transport)
	}
	if calls != 1 {
		t.Errorf("resolve called %d times, want exactly 1", calls)
	}
	if target.SocketPath != "/tmp/whatever.sock" {
		t.Errorf("target = %+v, want the resolved target threaded through", target)
	}
}

// TestChooseSendTransport_ClaudeCompatibleAlias covers a custom tool defined
// with compatible_with = "claude" (session.IsClaudeCompatible's own
// aliasing), proving chooseSendTransport treats it exactly like the "claude"
// built-in rather than only recognizing the literal string.
func TestChooseSendTransport_ClaudeCompatibleAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	if err := os.MkdirAll(filepath.Join(home, ".agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &session.UserConfig{
		Tools: map[string]session.ToolDef{
			"claude_wrapper": {Command: "claude-wrapper", CompatibleWith: "claude"},
		},
	}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()

	transport, reason, _ := chooseSendTransport(transportInputs{
		tool: "claude_wrapper", configValue: "auto", message: "hello",
		claudeSessionID: "sid", resolve: alwaysSocketOK(nil),
	})
	if transport != transportSocket || reason != "" {
		t.Errorf("claude-compatible alias: transport=%q reason=%q, want socket/\"\"", transport, reason)
	}
}

// TestPerformSend_SSHBackedInstance_TakesTmuxWithEmptyFallbackReason is the
// end-to-end proof behind the "SSH-backed instance always takes tmux" table
// row above: an otherwise-perfectly-resolvable local Claude session record
// (alwaysSocketOK never fails) still routes to tmux for a remote instance,
// and — because reasonRemoteSession is a selector-level reason, filtered by
// runTmuxSend just like an explicit send_transport=tmux pin — the resulting
// sendDeliveryResult.fallbackReason (the --json fallback_reason field) stays
// empty rather than leaking an internal-only reason string.
func TestPerformSend_SSHBackedInstance_TakesTmuxWithEmptyFallbackReason(t *testing.T) {
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{""}}
	inst := &session.Instance{ID: "i1", Title: "target", Tool: "claude", ClaudeSessionID: "sid", SSHHost: "example.com"}
	if !inst.IsSSH() {
		t.Fatal("test setup: inst should be SSH-backed")
	}

	res, err := performSend(inst, mock, "hello", false, defaultSendTuning(), "auto", alwaysSocketOK(nil), nil)
	if err != nil {
		t.Fatalf("performSend: %v", err)
	}
	if res.transport != "tmux" {
		t.Errorf("transport = %q, want %q (SSH-backed instance must never use the socket)", res.transport, "tmux")
	}
	if res.fallbackReason != "" {
		t.Errorf("fallbackReason = %q, want empty (remote_session is a selector-level reason, not a socket-refusal reason)", res.fallbackReason)
	}
	if mock.sendKeysCalls == 0 && mock.sendChunkedCalls == 0 {
		t.Errorf("expected the tmux target to be exercised for an SSH-backed instance")
	}
}

// TestSendTransportFromConfig is the regression test for the CodeRabbit
// finding: a malformed config.toml makes LoadUserConfig return a default
// config (GetSendTransport() would say "auto") PLUS a non-nil error, and the
// original code silently ignored that error, defaulting to "auto" even for
// a user who had pinned send_transport = "tmux" — the pin looked like it
// silently stopped applying whenever the file happened to have an unrelated
// typo. sendTransportFromConfig must treat a load error as "tmux"
// (conservative) and report a one-line warning.
func TestSendTransportFromConfig(t *testing.T) {
	t.Run("load error -> tmux, with a warning", func(t *testing.T) {
		orig := loadUserConfigForSend
		t.Cleanup(func() { loadUserConfigForSend = orig })
		loadErr := errors.New("config.toml:3: expected '=', found EOF")
		loadUserConfigForSend = func() (*session.UserConfig, error) {
			// LoadUserConfig's own contract: default config PLUS a non-nil
			// error, not a nil config.
			return &session.UserConfig{}, loadErr
		}

		value, warn := sendTransportFromConfig()
		if value != "tmux" {
			t.Errorf("value = %q, want %q (conservative on a load error)", value, "tmux")
		}
		if warn == "" {
			t.Fatal("expected a non-empty warning on a load error")
		}
		if !strings.Contains(warn, loadErr.Error()) {
			t.Errorf("warning %q does not mention the underlying error %q", warn, loadErr)
		}
		if !strings.Contains(warn, "tmux") {
			t.Errorf("warning %q should say it's using the tmux transport", warn)
		}
	})

	t.Run("clean load, explicit tmux pin", func(t *testing.T) {
		orig := loadUserConfigForSend
		t.Cleanup(func() { loadUserConfigForSend = orig })
		loadUserConfigForSend = func() (*session.UserConfig, error) {
			return &session.UserConfig{SendTransport: "tmux"}, nil
		}

		value, warn := sendTransportFromConfig()
		if value != "tmux" {
			t.Errorf("value = %q, want %q", value, "tmux")
		}
		if warn != "" {
			t.Errorf("warn = %q, want empty on a clean load", warn)
		}
	})

	t.Run("clean load, no pin -> auto", func(t *testing.T) {
		orig := loadUserConfigForSend
		t.Cleanup(func() { loadUserConfigForSend = orig })
		loadUserConfigForSend = func() (*session.UserConfig, error) {
			return &session.UserConfig{}, nil
		}

		value, warn := sendTransportFromConfig()
		if value != "auto" {
			t.Errorf("value = %q, want %q", value, "auto")
		}
		if warn != "" {
			t.Errorf("warn = %q, want empty on a clean load", warn)
		}
	})

	t.Run("nil config, no error -> auto, no warning", func(t *testing.T) {
		orig := loadUserConfigForSend
		t.Cleanup(func() { loadUserConfigForSend = orig })
		loadUserConfigForSend = func() (*session.UserConfig, error) {
			return nil, nil
		}

		value, warn := sendTransportFromConfig()
		if value != "auto" {
			t.Errorf("value = %q, want %q", value, "auto")
		}
		if warn != "" {
			t.Errorf("warn = %q, want empty", warn)
		}
	})
}

func TestIsBareSlashCommand(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"/compact", true},
		{"  /help", true},
		{"\t/rename foo", true},
		{"", false},
		{"   ", false},
		{"not a slash command", false},
		{"regular message", false},
	}
	for _, tc := range cases {
		if got := isBareSlashCommand(tc.msg); got != tc.want {
			t.Errorf("isBareSlashCommand(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
