package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil/logassert"
)

// FX-11 sentinel: one recognisable secret value is fed through every env path
// FX-11 covers ([tools.X].env, [tools.X].env_file, [groups.X.claude].env,
// `restart --env`, and mis-parsed / invalid env names carrying the value) and
// must never show up in the launch command (the argv of tmux / systemd-run /
// the pane's bash), in any captured log record, or in a returned error. The
// positive control proves the value still reaches the launched process.

const fx11Sentinel = "sk-test-SENTINEL-1234"

// fx11CaptureLogs routes the session and MCP loggers to a logassert capture
// for the duration of the test.
func fx11CaptureLogs(t *testing.T) *logassert.Capture {
	t.Helper()
	capture := logassert.NewCapture()
	origSession, origMCP, origCat := sessionLog, mcpLog, mcpCatLog
	logger := slog.New(capture)
	sessionLog, mcpLog, mcpCatLog = logger, logger, logger
	t.Cleanup(func() { sessionLog, mcpLog, mcpCatLog = origSession, origMCP, origCat })
	return capture
}

func fx11UseConfig(t *testing.T, cfg *UserConfig) {
	t.Helper()
	userConfigCacheMu.Lock()
	orig := userConfigCache
	userConfigCache = cfg
	userConfigCacheMu.Unlock()
	t.Cleanup(func() {
		userConfigCacheMu.Lock()
		userConfigCache = orig
		userConfigCacheMu.Unlock()
	})
}

// fx11AssertNoSentinelInLogs fails when any captured record carries the
// sentinel (or its distinctive core) in its message or any attribute.
func fx11AssertNoSentinelInLogs(t *testing.T, capture *logassert.Capture) {
	t.Helper()
	for _, rec := range capture.Records() {
		line := rec.Message
		for k, v := range rec.Attrs {
			line += " " + k + "=" + fmt.Sprint(v)
		}
		if strings.Contains(line, fx11Sentinel) || strings.Contains(line, "SENTINEL") {
			t.Errorf("log record leaks the secret: %s", line)
		}
	}
}

func fx11AssertNoSentinel(t *testing.T, what, s string) {
	t.Helper()
	if strings.Contains(s, fx11Sentinel) || strings.Contains(s, "SENTINEL") {
		t.Errorf("%s carries the secret:\n%s", what, s)
	}
}

func TestFX11Sentinel_SecretNeverInArgvLogsOrErrors(t *testing.T) {
	t.Run("tools_env_and_invalid_keys", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		fx11UseConfig(t, &UserConfig{
			Tools: map[string]ToolDef{"fx11tool": {Command: "true", Env: map[string]string{
				"AGENTDECK_TEST_SECRET": fx11Sentinel,
				// Mis-parsed entries whose NAME carries the value: they are
				// rejected and warned about, and the warning must not echo it.
				"AGENTDECK_TEST_SECRET " + fx11Sentinel: "x",
				"export=" + fx11Sentinel:                "x",
				fx11Sentinel:                            "x",
			}}},
			MCPs: make(map[string]MCPDef),
		})
		capture := fx11CaptureLogs(t)

		inst := &Instance{ID: "fx11-tool-inst", Title: "fx11", Tool: "fx11tool", Command: "true", ProjectPath: t.TempDir()}
		launch := inst.buildGenericCommand(inst.Command)
		prepared, _, err := inst.prepareCommand(launch)
		if err != nil {
			t.Fatalf("prepareCommand: %v", err)
		}
		fx11AssertNoSentinel(t, "built launch command", launch)
		fx11AssertNoSentinel(t, "prepared launch command (pane argv)", prepared)

		capture.AssertContains(t, "invalid_env_key_skipped: env names must match ^[A-Za-z_][A-Za-z0-9_]*$")
		fx11AssertNoSentinelInLogs(t, capture)

		if got := loc4RunPrefix(t, inst.buildEnvSourceCommand(), "AGENTDECK_TEST_SECRET"); got != fx11Sentinel {
			t.Fatalf("positive control: the value did not reach the process: got %q", got)
		}
	})

	t.Run("tools_env_file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		envFile := filepath.Join(t.TempDir(), "fx11.env")
		if err := os.WriteFile(envFile, []byte("export AGENTDECK_TEST_SECRET='"+fx11Sentinel+"'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fx11UseConfig(t, &UserConfig{
			Tools: map[string]ToolDef{"fx11tool": {Command: "true", EnvFile: envFile}},
			MCPs:  make(map[string]MCPDef),
		})
		capture := fx11CaptureLogs(t)

		inst := &Instance{ID: "fx11-envfile-inst", Title: "fx11", Tool: "fx11tool", Command: "true", ProjectPath: t.TempDir()}
		prepared, _, err := inst.prepareCommand(inst.buildGenericCommand(inst.Command))
		if err != nil {
			t.Fatalf("prepareCommand: %v", err)
		}
		fx11AssertNoSentinel(t, "prepared launch command (pane argv)", prepared)
		fx11AssertNoSentinelInLogs(t, capture)
		if got := loc4RunPrefix(t, inst.buildEnvSourceCommand(), "AGENTDECK_TEST_SECRET"); got != fx11Sentinel {
			t.Fatalf("positive control: env_file value did not reach the process: got %q", got)
		}
	})

	t.Run("groups_claude_env", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		fx11UseConfig(t, &UserConfig{
			Groups: map[string]GroupSettings{"fx11grp": {Claude: GroupClaudeSettings{Env: map[string]string{
				"AGENTDECK_TEST_SECRET":                 fx11Sentinel,
				"BAD-NAME=" + fx11Sentinel:              "x",
				"AGENTDECK_TEST_SECRET " + fx11Sentinel: "x",
			}}}},
			MCPs: make(map[string]MCPDef),
		})
		capture := fx11CaptureLogs(t)

		inst := &Instance{ID: "fx11-claude-inst", Title: "fx11", Tool: "claude", GroupPath: "fx11grp", ProjectPath: t.TempDir()}
		prefix := inst.buildEnvSourceCommand()
		fx11AssertNoSentinel(t, "claude env prelude (pane argv)", prefix)
		capture.AssertContains(t, "invalid_env_key_skipped: env names must match ^[A-Za-z_][A-Za-z0-9_]*$")
		fx11AssertNoSentinelInLogs(t, capture)
		if got := loc4RunPrefix(t, prefix, "AGENTDECK_TEST_SECRET"); got != fx11Sentinel {
			t.Fatalf("positive control: group claude env did not reach the process: got %q", got)
		}
	})

	t.Run("restart_env", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		capture := fx11CaptureLogs(t)

		inst := &Instance{ID: "fx11-restart-inst", restartEnv: map[string]string{
			"AGENTDECK_TEST_SECRET":                 fx11Sentinel,
			"AGENTDECK_TEST_SECRET " + fx11Sentinel: "x",
		}}
		prefix := inst.buildRestartEnvPrefix()
		fx11AssertNoSentinel(t, "restart env prefix (pane argv)", prefix)
		fx11AssertNoSentinelInLogs(t, capture)
		if got := loc4RunPrefix(t, prefix, "AGENTDECK_TEST_SECRET"); got != fx11Sentinel {
			t.Fatalf("positive control: restart env did not reach the process: got %q", got)
		}

		// A rejected restart name is reported back to the user, without the
		// value it carries.
		for _, bad := range []string{"AGENTDECK_TEST_SECRET " + fx11Sentinel, "X=" + fx11Sentinel, fx11Sentinel} {
			err := (&Instance{ID: "fx11-restart-bad"}).RestartWithEnv(map[string]string{bad: "v"})
			if err == nil {
				t.Fatalf("RestartWithEnv accepted invalid name %q", bad)
			}
			fx11AssertNoSentinel(t, "RestartWithEnv error", err.Error())
		}
	})
}

func TestEnvKeyForDisplay(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"GOOD_KEY", "GOOD_KEY"},
		{"API_KEY sk-live-abc", "API_KEY…"},
		{"export=secret", "export…"},
		{"TOKEN='x y'", "TOKEN…"},
		{"sk-proj-abcdef", "sk…"},
		{"=value", "(invalid)"},
		{"", "(invalid)"},
		{strings.Repeat("A", 40), strings.Repeat("A", 32) + "…"},
	} {
		if got := EnvKeyForDisplay(tc.in); got != tc.want {
			t.Errorf("EnvKeyForDisplay(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
