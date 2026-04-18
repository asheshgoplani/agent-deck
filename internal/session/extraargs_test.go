package session

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// extraArgsTestEnv isolates the test from the host's CLAUDE_CONFIG_DIR / HOME
// so buildClaudeCommand resolves deterministically. Mirrors the pattern in
// channelsTestEnv at channels_test.go:14.
func extraArgsTestEnv(t *testing.T) {
	t.Helper()
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	origHome := os.Getenv("HOME")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	os.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})
}

// setExtraArgsField uses reflection so this test file compiles cleanly on
// main (where Instance.ExtraArgs does not yet exist). Identical probe pattern
// to setChannelsField in channels_test.go:39.
func setExtraArgsField(t *testing.T, inst *Instance, args []string) {
	t.Helper()
	val := reflect.ValueOf(inst).Elem()
	field := val.FieldByName("ExtraArgs")
	if !field.IsValid() {
		t.Fatalf(
			"Instance.ExtraArgs field does not exist; required for first-class " +
				"--extra-arg CLI support. Add `ExtraArgs []string " +
				"`json:\"extra_args,omitempty\"`` to the Instance struct " +
				"in internal/session/instance.go next to Channels.",
		)
	}
	if field.Kind() != reflect.Slice || field.Type().Elem().Kind() != reflect.String {
		t.Fatalf(
			"Instance.ExtraArgs has wrong type %s; want []string",
			field.Type().String(),
		)
	}
	field.Set(reflect.ValueOf(args))
}

// TestStartCommandAppendsExtraArgs asserts that Instance.ExtraArgs tokens
// appear in the built start command. This is the core contract for passing
// arbitrary claude CLI flags (e.g. --agent, --model) to claude sessions.
func TestStartCommandAppendsExtraArgs(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("ea-start", t.TempDir(), "claude")
	setExtraArgsField(t, inst, []string{"--agent", "my-agent"})

	cmd := inst.buildClaudeCommand("claude")

	if !strings.Contains(cmd, "--agent") {
		t.Fatalf("built claude command missing --agent token, got:\n%s", cmd)
	}
	if !strings.Contains(cmd, "my-agent") {
		t.Errorf("built claude command missing my-agent value, got:\n%s", cmd)
	}
}

// TestStartCommandOmitsExtraArgsWhenEmpty asserts no extra flags are
// emitted when ExtraArgs is empty (avoids leading/trailing garbage).
func TestStartCommandOmitsExtraArgsWhenEmpty(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("ea-empty", t.TempDir(), "claude")
	val := reflect.ValueOf(inst).Elem()
	if !val.FieldByName("ExtraArgs").IsValid() {
		t.Fatalf("Instance.ExtraArgs field does not exist")
	}
	cmd := inst.buildClaudeCommand("claude")

	// The builder should produce a clean command with no double spaces or
	// trailing residue that would indicate a stray empty-flag emission.
	if strings.Contains(cmd, "  ") {
		t.Errorf("empty ExtraArgs produced double-space in command, got:\n%s", cmd)
	}
}

// TestExtraArgsRestartPersist asserts extra args survive a JSON round-trip
// through Storage. Mirrors TestChannelsRestartPersist at channels_test.go:118.
func TestExtraArgsRestartPersist(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("ea-restart", t.TempDir(), "claude")
	args := []string{"--model", "opus", "--thinking-level", "high"}
	setExtraArgsField(t, inst, args)

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("json.Marshal(inst): %v", err)
	}

	if !strings.Contains(string(data), `"extra_args":`) {
		t.Errorf(
			"marshalled instance missing \"extra_args\" json tag; got:\n%s",
			string(data),
		)
	}

	revived := &Instance{}
	if err := json.Unmarshal(data, revived); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	revivedField := reflect.ValueOf(revived).Elem().FieldByName("ExtraArgs")
	if !revivedField.IsValid() {
		t.Fatalf("revived Instance has no ExtraArgs field after unmarshal")
	}
	got := revivedField.Interface().([]string)
	if len(got) != len(args) {
		t.Fatalf("revived ExtraArgs = %v, want %v", got, args)
	}
	for i, want := range args {
		if got[i] != want {
			t.Fatalf("revived ExtraArgs[%d] = %q, want %q", i, got[i], want)
		}
	}

	cmd := revived.buildClaudeCommand("claude")
	if !strings.Contains(cmd, "--model") || !strings.Contains(cmd, "opus") {
		t.Errorf("command from revived instance missing extra args, got:\n%s", cmd)
	}
}

// TestResumeCommandAppendsExtraArgs is the phase-5 loopback guard, mirror
// of TestResumeCommandAppendsChannels at channels_test.go:182. Any flag that
// flows through buildClaudeExtraFlags must also survive a restart.
func TestResumeCommandAppendsExtraArgs(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("ea-resume", t.TempDir(), "claude")
	inst.ClaudeSessionID = "00000000-0000-0000-0000-000000000000"
	setExtraArgsField(t, inst, []string{"--agent", "reviewer"})

	cmd := inst.buildClaudeResumeCommand()

	if !strings.Contains(cmd, "--agent") || !strings.Contains(cmd, "reviewer") {
		t.Fatalf(
			"buildClaudeResumeCommand dropped --agent/reviewer on restart path; "+
				"this is the phase-5 loopback bug. got:\n%s",
			cmd,
		)
	}
}

// TestExtraArgsAndChannelsCoexist asserts that both flag families are
// emitted together and do not clobber each other. Channels come first
// (so --channels appears before user tokens), and user tokens are last
// so they can override claude's own defaults (claude uses last-wins).
func TestExtraArgsAndChannelsCoexist(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("ea-and-ch", t.TempDir(), "claude")
	inst.Channels = []string{"plugin:telegram@acme/bot"}
	setExtraArgsField(t, inst, []string{"--agent", "reviewer"})

	cmd := inst.buildClaudeCommand("claude")

	chanIdx := strings.Index(cmd, "--channels")
	agentIdx := strings.Index(cmd, "--agent")
	if chanIdx < 0 || agentIdx < 0 {
		t.Fatalf("missing --channels or --agent in command:\n%s", cmd)
	}
	if chanIdx > agentIdx {
		t.Errorf("expected --channels to appear BEFORE --agent (last-wins), got:\n%s", cmd)
	}
}

// TestExtraArgsShellInjectionSafe asserts that shell metacharacters in
// tokens are quoted so they do not execute. This is the injection-surface
// guard the Define→Develop debate gate flagged as HIGH severity.
func TestExtraArgsShellInjectionSafe(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("ea-inject", t.TempDir(), "claude")
	// Tokens that would be dangerous if not quoted: $(...) and backticks
	// would execute in bash -c context.
	setExtraArgsField(t, inst, []string{"--name", "$(rm -rf /)", "--other", "`touch /tmp/pwn`"})

	cmd := inst.buildClaudeCommand("claude")

	// Raw $() or `` must NOT appear unquoted. shellescape wraps in single
	// quotes, which prevent expansion.
	if strings.Contains(cmd, " $(rm -rf /)") || strings.Contains(cmd, " `touch /tmp/pwn`") {
		t.Fatalf(
			"shell metacharacters were not quoted — command injection surface; got:\n%s",
			cmd,
		)
	}
}

// TestExtraArgsShellQuotesTokensWithSpaces asserts that tokens containing
// shell metacharacters (spaces, quotes) are re-quoted on emission so
// `bash -c` does NOT re-split them. Without this, `--claude-args "foo bar"`
// would be re-tokenised into `foo` and `bar` by the shell wrapper.
func TestExtraArgsShellQuotesTokensWithSpaces(t *testing.T) {
	extraArgsTestEnv(t)

	inst := NewInstanceWithTool("ea-quote", t.TempDir(), "claude")
	setExtraArgsField(t, inst, []string{"--name", "agent with spaces"})

	cmd := inst.buildClaudeCommand("claude")

	// The multi-word value must be emitted as a single shell token, not
	// "agent with spaces" raw. Accept either single-quoted ('agent with spaces')
	// or double-quoted ("agent with spaces") forms; both survive bash -c.
	hasQuoted := strings.Contains(cmd, `'agent with spaces'`) ||
		strings.Contains(cmd, `"agent with spaces"`)
	if !hasQuoted {
		t.Fatalf(
			"token with spaces was not shell-quoted on emission; "+
				"bash -c will re-split it. Use shellescape.Quote on each token. got:\n%s",
			cmd,
		)
	}
}
