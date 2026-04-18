package session

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #662 regression suite — sessionHasConversationData returns false
// despite rich jsonl on disk, causing buildClaudeResumeCommand to emit
// --session-id instead of --resume (Claude then rejects or starts fresh).
//
// The issue body lists three hypotheses to audit:
//   (1) path-encoding mismatch on hidden-dir paths like `.agent-deck`
//   (2) race with SessionEnd flush at the resume-command call site
//   (3) findSessionFileInAllProjects fallback not firing / wrong configDir
//
// These tests pin each hypothesis. Tests 1 and 2 guard against regressions in
// the existing encoder + fallback. Test 3 is the RED test that fails on
// v1.7.25 — it exercises the SessionEnd flush race at the call site and
// requires a bounded retry on first-miss.
//
// Test 4 pins the diagnostic-log contract: when sessionHasConversationData
// returns false, the structured debug line MUST carry enough fields to
// reconstruct the decision from logs alone (config_dir, resolved path,
// encoded path, primary path tested, fallback tried, final result).

// captureSessionLogDebug swaps sessionLog for a JSON handler at Debug level
// so tests that assert on Debug lines (like the #662 diagnostic-log test)
// actually see them. The package-wide captureSessionLog helper defaults to
// slog's Info level and therefore drops the very lines this test must pin.
func captureSessionLogDebug(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	original := sessionLog
	sessionLog = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { sessionLog = original })
	return buf
}

// TestIssue662_HiddenDirInPath_EncodesToDoubleDash is a sanity pin for
// hypothesis (1): paths with dot-prefix segments must encode with a
// double-dash (the dot → dash plus the path-separator → dash), which is
// exactly how Claude writes to disk. If this ever regresses, every
// `.agent-deck/conductor/*` session silently stops finding its jsonl.
func TestIssue662_HiddenDirInPath_EncodesToDoubleDash(t *testing.T) {
	got := ConvertToClaudeDirName("/home/u/.agent-deck/conductor/travel")
	want := "-home-u--agent-deck-conductor-travel"
	if got != want {
		t.Fatalf("ConvertToClaudeDirName(hidden-dir path) = %q, want %q (double-dash indicates the dot was preserved as a separate char)", got, want)
	}
}

// TestIssue662_FindsFileViaFallback_WhenPrimaryPathMisses pins hypothesis (3):
// when the primary encoded path has no matching jsonl but ANOTHER project dir
// under the same configDir does have <sessionID>.jsonl, the fallback must
// surface it. This mirrors real-world path-hash drift where Claude was
// originally invoked from a slightly different cwd than the Instance records
// today.
func TestIssue662_FindsFileViaFallback_WhenPrimaryPathMisses(t *testing.T) {
	tmpDir := t.TempDir()

	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	_ = os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	t.Cleanup(func() {
		if origConfigDir == "" {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		} else {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		}
		ClearUserConfigCache()
	})
	ClearUserConfigCache()

	instPath := "/tmp/instance-cwd-A"
	actualEncoded := "-tmp-instance-cwd-B"
	sessionID := "11111111-2222-3333-4444-555555555555"

	actualDir := filepath.Join(tmpDir, "projects", actualEncoded)
	if err := os.MkdirAll(actualDir, 0o755); err != nil {
		t.Fatalf("mkdir actual projects dir: %v", err)
	}
	actualFile := filepath.Join(actualDir, sessionID+".jsonl")
	body := `{"type":"user","sessionId":"` + sessionID + `","text":"hi"}` + "\n"
	if err := os.WriteFile(actualFile, []byte(body), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	inst := NewInstance("test-fallback", instPath)
	inst.Tool = "claude"

	if !sessionHasConversationData(inst, sessionID) {
		t.Fatalf("expected true via fallback (jsonl at %q), got false", actualFile)
	}
}

// TestIssue662_DiagnosticLog_CapturesAllDecisionFields pins hypothesis (1) +
// (3) forensic story: when sessionHasConversationData returns false, the
// structured log line MUST contain enough fields to reconstruct the decision
// from logs alone. Production diagnosis of the travel-conductor case in the
// issue body was blocked because the existing log line only named the file
// tested — not the config_dir source, resolved project path, encoded path,
// stat err detail, or whether the fallback was tried.
//
// Required fields: config_dir, resolved_project_path, encoded_path,
// primary_path_tested, fallback_lookup_tried, final_result.
func TestIssue662_DiagnosticLog_CapturesAllDecisionFields(t *testing.T) {
	tmpDir := t.TempDir()

	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	_ = os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	t.Cleanup(func() {
		if origConfigDir == "" {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		} else {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		}
		ClearUserConfigCache()
	})
	ClearUserConfigCache()

	buf := captureSessionLogDebug(t)

	inst := NewInstance("diag-none", "/tmp/does-not-exist-anywhere")
	inst.Tool = "claude"
	sessionID := "deadbeef-dead-beef-dead-beefdeadbeef"

	if got := sessionHasConversationData(inst, sessionID); got {
		t.Fatalf("precondition: expected false for empty state, got true")
	}

	logText := buf.String()
	required := []string{
		"config_dir",
		"resolved_project_path",
		"encoded_path",
		"primary_path_tested",
		"fallback_lookup_tried",
		"final_result",
	}
	for _, field := range required {
		if !strings.Contains(logText, `"`+field+`"`) {
			t.Errorf("diagnostic log missing required field %q. Log: %s", field, logText)
		}
	}
}

// TestIssue662_BuildClaudeResumeCommand_RetriesOnceOnSessionEndRace pins
// hypothesis (2): if sessionHasConversationData observes "no data" on the
// first call but the jsonl is written shortly after (Claude's SessionEnd
// hook hasn't fully flushed yet), buildClaudeResumeCommand must wait briefly
// and re-check before falling back to --session-id. Per the issue spec:
// "Worth a 1-retry with a 200ms wait at the CALL SITE".
//
// RED on v1.7.26-baseline — there is no retry there; GREEN after the
// resumeCheckRetryDelay at buildClaudeResumeCommand is added.
func TestIssue662_BuildClaudeResumeCommand_RetriesOnceOnSessionEndRace(t *testing.T) {
	tmpHome := t.TempDir()

	origHome := os.Getenv("HOME")
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	_ = os.Setenv("HOME", tmpHome)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origConfigDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		}
		ClearUserConfigCache()
	})
	ClearUserConfigCache()

	configDir := filepath.Join(tmpHome, ".claude")
	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir projectPath: %v", err)
	}
	encoded := ConvertToClaudeDirName(projectPath)
	projectsDir := filepath.Join(configDir, "projects", encoded)
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}

	sessionID := "cafebabe-cafe-babe-cafe-babecafebabe"
	jsonlPath := filepath.Join(projectsDir, sessionID+".jsonl")

	inst := NewInstance("race-test", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = sessionID

	var writeDone atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return
		}
		body := fmt.Sprintf(`{"type":"user","sessionId":%q,"text":"hi"}`+"\n", sessionID)
		if err := os.WriteFile(jsonlPath, []byte(body), 0o600); err == nil {
			writeDone.Store(true)
		}
	}()

	cmd := inst.buildClaudeResumeCommand()

	deadline := time.Now().Add(1 * time.Second)
	for !writeDone.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !writeDone.Load() {
		t.Fatalf("precondition: delayed write did not complete within 1s")
	}

	resumeFlag := "--resume " + sessionID
	sessionIDFlag := "--session-id " + sessionID
	if strings.Contains(cmd, sessionIDFlag) {
		t.Errorf("expected --resume, got --session-id (retry did not surface the late-written jsonl). cmd=%q", cmd)
	}
	if !strings.Contains(cmd, resumeFlag) {
		t.Errorf("expected cmd to contain %q, got %q", resumeFlag, cmd)
	}
}
