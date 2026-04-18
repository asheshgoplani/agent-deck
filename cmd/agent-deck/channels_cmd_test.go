package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// channelsCLIBuildOnce builds the agent-deck binary exactly once per test
// run. Per-test rebuilds add ~5s × N; the lazy-once pattern keeps the
// failing-test suite snappy while still catching real CLI regressions.
var (
	channelsCLIBinPath string
	channelsCLIBuildMu sync.Mutex
	channelsCLIBuildOK bool
)

func channelsCLIBinary(t *testing.T) string {
	t.Helper()
	channelsCLIBuildMu.Lock()
	defer channelsCLIBuildMu.Unlock()

	if channelsCLIBuildOK {
		return channelsCLIBinPath
	}

	binDir, err := os.MkdirTemp("", "agent-deck-channels-bin-*")
	if err != nil {
		t.Fatalf("mkdir bin tmp: %v", err)
	}
	bin := filepath.Join(binDir, "agent-deck-test")

	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\noutput: %s", err, out)
	}
	channelsCLIBinPath = bin
	channelsCLIBuildOK = true
	return bin
}

// runAgentDeck invokes the built binary with isolated HOME so each test
// owns its own ~/.agent-deck/profiles/<profile>/ tree.
func runAgentDeck(
	t *testing.T,
	home string,
	args ...string,
) (stdout, stderr string, exitCode int) {
	t.Helper()

	bin := channelsCLIBinary(t)
	cmd := exec.Command(bin, args...)

	// Strip TMUX*/AGENTDECK_*/HOME from parent so the test isolation is
	// total — same pattern used by TestLogCgroupIsolationDecision_*
	// in cgroup_isolation_wiring_test.go:60-78.
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX") {
			continue
		}
		if strings.HasPrefix(kv, "AGENTDECK_") {
			continue
		}
		if strings.HasPrefix(kv, "HOME=") {
			continue
		}
		// Strip CLAUDE_CONFIG_DIR so the test's isolated HOME/.claude is the
		// effective Claude config dir — otherwise session search leaks into
		// the developer's real ~/.claude/projects tree. Added for #483.
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+home,
		"AGENTDECK_PROFILE=ch_support_test",
		"TERM=dumb",
	)
	cmd.Env = env

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run binary: %v\nstdout: %s\nstderr: %s", err, outBuf.String(), errBuf.String())
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// readSessionsJSON reads the persisted sessions for the test profile.
// The CLI writes to ~/.agent-deck/profiles/ch_support_test/state.db AND
// (when present) sessions.json; we look at sessions.json which is the
// human-readable mirror. Falls back to scanning state.db via `agent-deck list`
// if needed.
func readSessionsJSON(t *testing.T, home string) string {
	t.Helper()
	// list --json prints all sessions for the active profile. This avoids
	// poking SQLite from the test.
	stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf(
			"agent-deck list --json failed (exit %d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr,
		)
	}
	return stdout
}

// TestAddChannelFlag asserts that `agent-deck add --channel <plugin-id>`
// is parsed and the channel persists on the new session.
//
// Failure mode on main:
//
//	flag provided but not defined: -channel
//	(exit 2 from flag.NewFlagSet ExitOnError)
func TestAddChannelFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", "ch-add-test",
		"-c", "claude",
		"--channel", "plugin:telegram@user/repo",
		"--channel", "plugin:discord@user/repo",
		"--no-parent",
		"--json",
		projectDir,
	)
	if code != 0 {
		t.Fatalf(
			"agent-deck add --channel failed (exit %d) — feature missing on main\n"+
				"stdout: %s\nstderr: %s",
			code, stdout, stderr,
		)
	}

	listJSON := readSessionsJSON(t, home)

	// Expect at least one of the channels to appear in the persisted JSON.
	// We don't assume a specific JSON shape — just that the channel id is
	// present somewhere on the session record.
	if !strings.Contains(listJSON, "plugin:telegram@user/repo") {
		t.Errorf(
			"persisted sessions missing channel id; got:\n%s",
			listJSON,
		)
	}
	if !strings.Contains(listJSON, "plugin:discord@user/repo") {
		t.Errorf(
			"persisted sessions missing second channel id; got:\n%s",
			listJSON,
		)
	}
}

// TestSessionShowJSONIncludesChannels asserts that
// `agent-deck session show --json <id>` includes the `channels` field when
// the session has channels set. Without this, the `show` JSON is
// inconsistent with `list --json` (which does include the field).
//
// Failure mode on main pre-fix: `session show --json` omits the "channels"
// key entirely even when set. Data persists fine (list shows it) — only
// the show serializer is blind to it.
//
// Root cause: two separate JSON emitters. handleList threads Channels;
// handleSessionShow (session_cmd.go:645) builds its own jsonData map and
// never included the field.
//
// See gh#615.
func TestSessionShowJSONIncludesChannels(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Step 1: create claude session with --channel.
	stdout, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", "ch-show-test",
		"-c", "claude",
		"--channel", "plugin:telegram@user/repo",
		"--no-parent",
		"--json",
		projectDir,
	)
	if code != 0 {
		t.Fatalf("agent-deck add --channel failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var addResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &addResp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}

	// Step 2: session show --json MUST include the channels field.
	stdout, stderr, code = runAgentDeck(t, home,
		"session", "show", addResp.ID, "--json",
	)
	if code != 0 {
		t.Fatalf("agent-deck session show --json failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	var showResp map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &showResp); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}

	// The assertion — pre-fix this fails because the key is absent.
	channels, ok := showResp["channels"]
	if !ok {
		t.Fatalf(
			"session show --json must include 'channels' field when set, but key is absent.\n"+
				"list --json includes it; show --json should too (consistency).\n"+
				"See gh#615.\nResponse: %s",
			stdout,
		)
	}

	// Must be a list with our channel id.
	chList, ok := channels.([]interface{})
	if !ok {
		t.Fatalf("channels field should be a list, got %T: %v", channels, channels)
	}
	if len(chList) != 1 || chList[0] != "plugin:telegram@user/repo" {
		t.Errorf("expected channels == [plugin:telegram@user/repo], got %v", chList)
	}
}

// TestSessionSetChannels asserts that
// `agent-deck session set <id> channels <csv>` updates the field.
//
// Failure mode on main:
//
//	invalid field: channels  (exit 1)
//	from handleSessionSet's validFields check at session_cmd.go:871-879.
func TestSessionSetChannels(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Step 1: add a session WITHOUT --channel (works on main).
	stdout, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", "ch-set-test",
		"-c", "claude",
		"--no-parent",
		"--json",
		projectDir,
	)
	if code != 0 {
		t.Fatalf("agent-deck add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var addResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &addResp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if addResp.ID == "" {
		t.Fatalf("add returned empty id; stdout: %s", stdout)
	}

	// Step 2: try to set channels — this should succeed on the fix branch.
	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", addResp.ID, "channels",
		"plugin:telegram@user/repo,plugin:discord@user/repo",
		"--json",
	)
	if code != 0 {
		t.Fatalf(
			"agent-deck session set <id> channels failed (exit %d) — feature missing on main\n"+
				"stdout: %s\nstderr: %s",
			code, stdout, stderr,
		)
	}

	// Step 3: confirm persisted.
	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, "plugin:telegram@user/repo") {
		t.Errorf("session set channels did not persist; list output:\n%s", listJSON)
	}
}

// TestChannelsOnlyForClaude asserts the tool-restriction contract:
//
//	(positive control) setting channels on a Claude session SUCCEEDS, and
//	(negative control) setting channels on a non-Claude session FAILS with
//	a tool-specific error.
//
// Both arms are required because, on main, the field is universally
// rejected ("invalid field: channels"). A loose assertion that just
// checks "did it fail" gives a false-PASS — main's universal rejection
// would trivially satisfy a unilateral negative test. The positive
// control forces the implementation to wire channels for Claude
// specifically, then guard the other tools.
//
// Failure mode on main:
//
//	positive control fails first — agent-deck rejects "channels" as an
//	invalid field BEFORE reaching any tool check. Exit 1, error
//	"invalid field: channels".
func TestChannelsOnlyForClaude(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	claudeProj := filepath.Join(home, "claude-proj")
	shellProj := filepath.Join(home, "shell-proj")
	for _, p := range []string{claudeProj, shellProj} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// --- Positive control: claude session accepts channels. ---
	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", "ch-claude-ok", "-c", "claude", "--no-parent", "--json", claudeProj,
	)
	if code != 0 {
		t.Fatalf("add claude failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var claudeResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &claudeResp); err != nil {
		t.Fatalf("parse claude add response: %v\nstdout: %s", err, stdout)
	}

	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", claudeResp.ID, "channels",
		"plugin:telegram@user/repo", "--json",
	)
	if code != 0 {
		t.Fatalf(
			"positive control failed: setting channels on a CLAUDE session "+
				"should succeed (exit %d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr,
		)
	}

	// --- Negative control: shell session rejects channels with a
	// tool-specific message. ---
	stdout, stderr, code = runAgentDeck(t, home,
		"add", "-t", "ch-shell-reject", "-c", "bash", "--no-parent", "--json", shellProj,
	)
	if code != 0 {
		t.Fatalf("add shell failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var shellResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &shellResp); err != nil {
		t.Fatalf("parse shell add response: %v\nstdout: %s", err, stdout)
	}

	stdout, stderr, code = runAgentDeck(t, home,
		"session", "set", shellResp.ID, "channels",
		"plugin:telegram@user/repo", "--json",
	)
	if code == 0 {
		t.Fatalf(
			"negative control failed: channels on a non-claude session must "+
				"be rejected\nstdout: %s\nstderr: %s",
			stdout, stderr,
		)
	}

	// The error must call out the tool restriction explicitly. Reject
	// generic "invalid field" — that's the main-branch failure mode and
	// would let a half-implementation through.
	combined := strings.ToLower(stdout + stderr)
	if strings.Contains(combined, "invalid field") {
		t.Errorf(
			"shell-session error should be a tool-restriction message, "+
				"NOT a generic 'invalid field'; got:\nstdout: %s\nstderr: %s",
			stdout, stderr,
		)
	}
	mustMentionTool := strings.Contains(combined, "claude") &&
		(strings.Contains(combined, "only") ||
			strings.Contains(combined, "supported") ||
			strings.Contains(combined, "requires"))
	if !mustMentionTool {
		t.Errorf(
			"shell-session error must mention claude AND a restriction word "+
				"(only/supported/requires); got:\nstdout: %s\nstderr: %s",
			stdout, stderr,
		)
	}
}
