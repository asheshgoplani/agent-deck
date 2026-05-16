package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// initTestLogging redirects the global logger to a temp dir's debug.log
// so a test can assert on the JSON log output produced inside the
// hook-handler write paths.
func initTestLogging(t *testing.T) string {
	t.Helper()
	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug:  true,
		LogDir: logDir,
		Level:  "debug",
		Format: "json",
	})
	t.Cleanup(func() { logging.Shutdown() })
	return logDir
}

// TestWriteHookStatus_WarnsOnMkdirError verifies that when the hooks
// directory cannot be created (path occupied by a regular file), the
// failure produces a WARN log instead of being silently swallowed.
//
// Regression: cmd/agent-deck/hook_handler.go writeHookStatus previously
// swallowed os.MkdirAll errors → producer-side failures invisible to
// operators while the consumer (web/session_data_service) showed stale
// status.
func TestWriteHookStatus_WarnsOnMkdirError(t *testing.T) {
	logDir := initTestLogging(t)

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Place a regular file where the hooks directory should be, so MkdirAll
	// fails with "not a directory".
	adDir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(adDir, 0o755); err != nil {
		t.Fatalf("setup .agent-deck: %v", err)
	}
	hooksPath := filepath.Join(adDir, "hooks")
	if err := os.WriteFile(hooksPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}

	writeHookStatus("inst-mkdirfail", "running", "", "UserPromptSubmit")

	logging.Shutdown()
	body := readLog(t, logDir)
	if !strings.Contains(body, `"hook_status_mkdir_failed"`) {
		t.Errorf("expected hook_status_mkdir_failed WARN; got:\n%s", body)
	}
	if !strings.Contains(body, `"level":"WARN"`) {
		t.Errorf("expected WARN level; got:\n%s", body)
	}
}

// TestWriteHookStatus_WarnsOnWriteFileError verifies that when WriteFile
// fails (read-only hooks directory), a WARN is logged.
func TestWriteHookStatus_WarnsOnWriteFileError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission test requires non-root")
	}

	logDir := initTestLogging(t)

	home := t.TempDir()
	t.Setenv("HOME", home)

	hooksDir := filepath.Join(home, ".agent-deck", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.Chmod(hooksDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(hooksDir, 0o755) }()

	writeHookStatus("inst-writefail", "running", "", "UserPromptSubmit")

	logging.Shutdown()
	body := readLog(t, logDir)
	if !strings.Contains(body, `"hook_status_write_failed"`) {
		t.Errorf("expected hook_status_write_failed WARN; got:\n%s", body)
	}
}

// TestWriteCostEvent_WarnsOnMkdirError exercises the second function
// (writeCostEvent) with a Stop hook payload pointing at a fake transcript;
// makes the cost-events directory unwritable to trigger MkdirAll failure.
func TestWriteCostEvent_WarnsOnMkdirError(t *testing.T) {
	logDir := initTestLogging(t)

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Stage a valid Claude transcript so writeCostEvent gets past the
	// transcript-path validation and reaches the MkdirAll(costDir) call.
	claudeDir := filepath.Join(home, ".claude", "projects", "abc")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("setup transcript dir: %v", err)
	}
	transcript := filepath.Join(claudeDir, "session.jsonl")
	body := `{"type":"assistant","message":{"model":"claude-test","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// Block cost-events dir creation: place a regular file at the path.
	adDir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(adDir, 0o755); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	if err := os.WriteFile(filepath.Join(adDir, "cost-events"), []byte("blocker"), 0o644); err != nil {
		t.Fatalf("create blocker file: %v", err)
	}

	stopPayload := []byte(`{"hook_event_name":"Stop","transcript_path":"` + transcript + `"}`)
	writeCostEvent("inst-cost", stopPayload)

	logging.Shutdown()
	logBody := readLog(t, logDir)
	if !strings.Contains(logBody, `"cost_event_mkdir_failed"`) {
		t.Errorf("expected cost_event_mkdir_failed WARN; got:\n%s", logBody)
	}
}

func readLog(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "debug.log"))
	if err != nil {
		t.Fatalf("read debug.log: %v", err)
	}
	return string(data)
}
