package session

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// channelsTestEnv isolates the test from the host's CLAUDE_CONFIG_DIR / HOME
// so buildClaudeCommand resolves deterministically. Mirrors the env-isolation
// pattern in TestBuildClaudeCommand_SubagentAddDir at instance_test.go:509.
func channelsTestEnv(t *testing.T) {
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

// setChannelsField uses reflection so this test file compiles cleanly on
// main (where Instance.Channels does not yet exist). When the field lands,
// the probe transparently starts succeeding — no test rewrite required.
//
// Returns the *reflect.Value or fails the test with a message that names
// the missing field. This is the SPECIFIC failure mode the conductor's
// PLAN.md predicts for ch-support.
func setChannelsField(t *testing.T, inst *Instance, channels []string) {
	t.Helper()
	val := reflect.ValueOf(inst).Elem()
	field := val.FieldByName("Channels")
	if !field.IsValid() {
		t.Fatalf(
			"Instance.Channels field does not exist; required for first-class " +
				"--channel/--channels support (see fix/ch-support PLAN.md). " +
				"Add `Channels []string \\`json:\"channels,omitempty\"\\`` to the Instance struct " +
				"in internal/session/instance.go.",
		)
	}
	if field.Kind() != reflect.Slice || field.Type().Elem().Kind() != reflect.String {
		t.Fatalf(
			"Instance.Channels has wrong type %s; want []string",
			field.Type().String(),
		)
	}
	field.Set(reflect.ValueOf(channels))
}

// TestStartCommandAppendsChannels asserts that when a Claude session has
// non-empty Channels, the built start command contains a "--channels <entry>"
// flag (one --channels value per channel). This is the contract that fixes the lost-Telegram-message bug:
// without --channels on the claude binary, channel plugins run as plain
// MCPs (tools only, no inbound delivery).
//
// Failure mode on main:
//
//	channels_test.go: Instance.Channels field does not exist; required for
//	first-class --channel/--channels support (see fix/ch-support PLAN.md).
func TestStartCommandAppendsChannels(t *testing.T) {
	channelsTestEnv(t)

	inst := NewInstanceWithTool("ch-test", t.TempDir(), "claude")
	setChannelsField(t, inst, []string{
		"plugin:telegram@user/repo",
		"plugin:discord@user/repo",
	})

	cmd := inst.buildClaudeCommand("claude")

	if !strings.Contains(cmd, "--channels") {
		t.Fatalf("built claude command missing --channels flag, got:\n%s", cmd)
	}
	expected := "--channels plugin:telegram@user/repo --channels plugin:discord@user/repo"
	if !strings.Contains(cmd, expected) {
		t.Errorf("expected built claude command to contain %q, got:\n%s", expected, cmd)
	}
}

// TestStartCommandOmitsChannelsWhenEmpty asserts the negative: a session
// with no channels must NOT emit --channels (would error out claude).
func TestStartCommandOmitsChannelsWhenEmpty(t *testing.T) {
	channelsTestEnv(t)

	inst := NewInstanceWithTool("ch-empty", t.TempDir(), "claude")
	// Probe field existence the same way the positive test does.
	val := reflect.ValueOf(inst).Elem()
	if !val.FieldByName("Channels").IsValid() {
		t.Fatalf(
			"Instance.Channels field does not exist (see fix/ch-support PLAN.md)",
		)
	}
	cmd := inst.buildClaudeCommand("claude")

	if strings.Contains(cmd, "--channels") {
		t.Errorf("expected NO --channels flag for empty channels, got:\n%s", cmd)
	}
}

// TestChannelsRestartPersist asserts that channels survive a JSON
// round-trip through Storage. This stands in for "agent-deck session
// restart preserves channels" because Restart() reloads from the same
// JSON the storage layer wrote. If the field marshals + unmarshals
// correctly AND buildClaudeCommand picks it up, restart wiring is wired.
//
// Failure mode on main: same compile-time-clean reflection failure as
// TestStartCommandAppendsChannels — Channels field missing.
func TestChannelsRestartPersist(t *testing.T) {
	channelsTestEnv(t)

	inst := NewInstanceWithTool("ch-restart", t.TempDir(), "claude")
	channels := []string{"plugin:telegram@acme/bot"}
	setChannelsField(t, inst, channels)

	// Round-trip through JSON (same path Storage.Save → Storage.Load uses).
	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("json.Marshal(inst): %v", err)
	}

	// Assert the JSON tag is "channels" (lowercase), matching the project's
	// convention for other slice fields like loaded_mcp_names.
	if !strings.Contains(string(data), `"channels":`) {
		t.Errorf(
			"marshalled instance missing \"channels\" json tag; got:\n%s",
			string(data),
		)
	}

	revived := &Instance{}
	if err := json.Unmarshal(data, revived); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	revivedField := reflect.ValueOf(revived).Elem().FieldByName("Channels")
	if !revivedField.IsValid() {
		t.Fatalf("revived Instance has no Channels field after unmarshal")
	}
	got := revivedField.Interface().([]string)
	if len(got) != len(channels) || got[0] != channels[0] {
		t.Fatalf("revived Channels = %v, want %v", got, channels)
	}

	// Final check: rebuild the command from the revived instance and
	// confirm --channels still flows through. This is what restart does.
	cmd := revived.buildClaudeCommand("claude")
	if !strings.Contains(cmd, "--channels plugin:telegram@acme/bot") {
		t.Errorf(
			"command from revived (post-restart) instance missing --channels, got:\n%s",
			cmd,
		)
	}
}

// TestResumeCommandAppendsChannels is the phase 5 LOOPBACK regression guard.
//
// Bug (conductor E2E, v1.5.x): `agent-deck session restart <id>` regenerated
// the pane_start_command WITHOUT --channels, even though the Instance had
// Channels set and `agent-deck session start` correctly emitted --channels.
//
// Root cause: Instance.Restart() dispatches to buildClaudeResumeCommand,
// which hand-rolled its own dangerous-mode flag assembly and never called
// buildClaudeExtraFlags — so every flag emitted by that helper (--channels,
// --add-dir, etc.) was silently dropped on restart. TestChannelsRestartPersist
// only covers JSON round-trip + buildClaudeCommand (the START path), so it
// missed this.
//
// This test asserts the restart-path command builder preserves --channels
// whenever Instance.Channels is non-empty. Failing this test MUST be fixed
// by routing buildClaudeResumeCommand through buildClaudeExtraFlags; any
// other fix is a symptom patch.
func TestResumeCommandAppendsChannels(t *testing.T) {
	channelsTestEnv(t)

	inst := NewInstanceWithTool("ch-resume", t.TempDir(), "claude")
	// Force the --session-id branch (no on-disk JSONL for this fresh UUID).
	inst.ClaudeSessionID = "00000000-0000-0000-0000-000000000000"
	setChannelsField(t, inst, []string{
		"plugin:telegram@acme/bot",
		"plugin:discord@acme/bot",
	})

	cmd := inst.buildClaudeResumeCommand()

	if !strings.Contains(cmd, "--channels") {
		t.Fatalf(
			"buildClaudeResumeCommand dropped --channels on restart path; "+
				"this is the phase-5 loopback bug. got:\n%s",
			cmd,
		)
	}
	expected := "--channels plugin:telegram@acme/bot --channels plugin:discord@acme/bot"
	if !strings.Contains(cmd, expected) {
		t.Errorf(
			"expected resume command to contain %q, got:\n%s",
			expected, cmd,
		)
	}
}
