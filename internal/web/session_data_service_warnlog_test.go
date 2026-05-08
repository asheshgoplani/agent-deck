package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestDefaultLoadHookStatuses_WarnsOnUnmarshalError verifies that a
// malformed hook JSON file produces a WARN log instead of being silently
// skipped (session_data_service.go ~line 190 used to `continue` with no
// log, masking producer-side bugs in cmd/agent-deck/hook_handler.go).
func TestDefaultLoadHookStatuses_WarnsOnUnmarshalError(t *testing.T) {
	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug:  true,
		LogDir: logDir,
		Level:  "debug",
		Format: "json",
	})
	defer logging.Shutdown()

	home := t.TempDir()
	t.Setenv("HOME", home)

	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	// Malformed JSON — must trigger json.Unmarshal error path.
	if err := os.WriteFile(filepath.Join(hooksDir, "inst-bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write broken hook: %v", err)
	}

	_ = defaultLoadHookStatuses()

	logging.Shutdown()

	data, err := os.ReadFile(filepath.Join(logDir, "debug.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"hook_status_unmarshal_failed"`) {
		t.Errorf("expected hook_status_unmarshal_failed WARN; got:\n%s", body)
	}
	if !strings.Contains(body, `"level":"WARN"`) {
		t.Errorf("expected WARN level; got:\n%s", body)
	}
	if !strings.Contains(body, `"file":"inst-bad.json"`) && !strings.Contains(body, `inst-bad.json`) {
		t.Errorf("expected log to identify the offending file; got:\n%s", body)
	}
}

// TestDefaultLoadHookStatuses_WarnsOnReadFileError verifies that an
// unreadable hook file produces a WARN log instead of being silently
// skipped.
func TestDefaultLoadHookStatuses_WarnsOnReadFileError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission test requires non-root")
	}

	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug:  true,
		LogDir: logDir,
		Level:  "debug",
		Format: "json",
	})
	defer logging.Shutdown()

	home := t.TempDir()
	t.Setenv("HOME", home)

	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bad := filepath.Join(hooksDir, "inst-unreadable.json")
	if err := os.WriteFile(bad, []byte(`{"status":"running"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(bad, 0o644) }()

	_ = defaultLoadHookStatuses()

	logging.Shutdown()

	data, err := os.ReadFile(filepath.Join(logDir, "debug.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"hook_status_read_failed"`) {
		t.Errorf("expected hook_status_read_failed WARN; got:\n%s", body)
	}
}

// TestDefaultLoadHookStatuses_WarnsOnReadDirError verifies that when the
// hooks directory exists but cannot be read (e.g. perms), a WARN is logged
// rather than silent return. ENOENT is treated as benign and not logged.
func TestDefaultLoadHookStatuses_WarnsOnReadDirError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission test requires non-root")
	}

	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug:  true,
		LogDir: logDir,
		Level:  "debug",
		Format: "json",
	})
	defer logging.Shutdown()

	home := t.TempDir()
	t.Setenv("HOME", home)

	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Make the directory unreadable.
	if err := os.Chmod(hooksDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(hooksDir, 0o755) }()

	_ = defaultLoadHookStatuses()

	logging.Shutdown()

	data, err := os.ReadFile(filepath.Join(logDir, "debug.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"hook_status_dir_read_failed"`) {
		t.Errorf("expected hook_status_dir_read_failed WARN; got:\n%s", body)
	}
}
