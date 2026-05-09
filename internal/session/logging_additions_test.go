package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// readLogRecords parses a JSONL debug.log into a slice of records.
// A missing file returns an empty slice rather than fatal, so callers can
// distinguish "no log line emitted" from a parse error.
func readLogRecords(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "debug.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read debug.log: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// findRecord returns the first record whose msg matches.
func findRecord(records []map[string]any, msg string) map[string]any {
	for _, r := range records {
		if r["msg"] == msg {
			return r
		}
	}
	return nil
}

// TestSessionCreatedLogged asserts NewInstance/NewInstanceWithTool emit a
// session_created INFO line with instance_id, title, project_path, tool, group_path.
// Closes logging-review G1: session creation is currently silent.
func TestSessionCreatedLogged(t *testing.T) {
	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: dir, Level: "info"})
	defer logging.Shutdown()

	inst := NewInstance("ut-session-created", "/tmp/proj-a")
	if inst == nil {
		t.Fatal("NewInstance returned nil")
	}

	records := readLogRecords(t, dir)
	rec := findRecord(records, "session_created")
	if rec == nil {
		t.Fatalf("expected session_created log line; got %d records", len(records))
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["title"] != "ut-session-created" {
		t.Errorf("title = %v, want ut-session-created", rec["title"])
	}
	if rec["project_path"] != "/tmp/proj-a" {
		t.Errorf("project_path = %v, want /tmp/proj-a", rec["project_path"])
	}
	if rec["instance_id"] != inst.ID {
		t.Errorf("instance_id = %v, want %s", rec["instance_id"], inst.ID)
	}
	if rec["tool"] != "shell" {
		t.Errorf("tool = %v, want shell", rec["tool"])
	}
	if rec["component"] != logging.CompSession {
		t.Errorf("component = %v, want %s", rec["component"], logging.CompSession)
	}
}

// TestHookStatusUpdatedAtInfoWithPath closes G10/G11.
// hook_status_updated must be at INFO and include the source path.
func TestHookStatusUpdatedAtInfoWithPath(t *testing.T) {
	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: dir, Level: "info"})
	defer logging.Shutdown()

	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	payload := []byte(`{"status":"running","session_id":"abc","event":"UserPromptSubmit","ts":1700000000}`)
	filePath := filepath.Join(hooksDir, "inst-hook-1.json")
	if err := os.WriteFile(filePath, payload, 0644); err != nil {
		t.Fatal(err)
	}
	w.processFile(filePath)

	records := readLogRecords(t, dir)
	rec := findRecord(records, "hook_status_updated")
	if rec == nil {
		t.Fatalf("expected hook_status_updated record; got %d records", len(records))
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["instance"] != "inst-hook-1" {
		t.Errorf("instance = %v, want inst-hook-1", rec["instance"])
	}
	if rec["status"] != "running" {
		t.Errorf("status = %v, want running", rec["status"])
	}
	if rec["path"] != filePath {
		t.Errorf("path = %v, want %s", rec["path"], filePath)
	}
}

// TestHookFileCorruptLoggedOnUnmarshal closes G9.
// Malformed JSON in a hook file must emit hook_file_corrupt at WARN.
func TestHookFileCorruptLoggedOnUnmarshal(t *testing.T) {
	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: dir, Level: "info"})
	defer logging.Shutdown()

	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	corrupt := []byte("not-json-at-all{{{")
	filePath := filepath.Join(hooksDir, "broken.json")
	if err := os.WriteFile(filePath, corrupt, 0644); err != nil {
		t.Fatal(err)
	}
	w.processFile(filePath)

	records := readLogRecords(t, dir)
	rec := findRecord(records, "hook_file_corrupt")
	if rec == nil {
		t.Fatalf("expected hook_file_corrupt WARN; got %d records", len(records))
	}
	if rec["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", rec["level"])
	}
	if rec["path"] != filePath {
		t.Errorf("path = %v, want %s", rec["path"], filePath)
	}
	if rec["reason"] != "unmarshal" {
		t.Errorf("reason = %v, want unmarshal", rec["reason"])
	}
	if _, ok := rec["error"]; !ok {
		t.Error("expected error field on hook_file_corrupt")
	}
	if got, _ := rec["bytes_read"].(float64); int(got) != len(corrupt) {
		t.Errorf("bytes_read = %v, want %d", rec["bytes_read"], len(corrupt))
	}
}

// TestHookFileCorruptLoggedOnReadFail — when ReadFile returns err other
// than not-exist, log it as WARN with reason=read.
func TestHookFileCorruptLoggedOnReadFail(t *testing.T) {
	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: dir, Level: "info"})
	defer logging.Shutdown()

	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	// Make a file we cannot read by setting permission 0000.
	filePath := filepath.Join(hooksDir, "noread.json")
	if err := os.WriteFile(filePath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filePath, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filePath, 0644) }()

	// Skip if running as root (root can read 0000 files).
	if data, err := os.ReadFile(filePath); err == nil {
		t.Skipf("running as root or permissive FS — file readable despite 0000 (data=%q)", data)
	}

	w.processFile(filePath)

	records := readLogRecords(t, dir)
	rec := findRecord(records, "hook_file_corrupt")
	if rec == nil {
		t.Fatalf("expected hook_file_corrupt for unreadable file; got %d records", len(records))
	}
	if rec["reason"] != "read" {
		t.Errorf("reason = %v, want read", rec["reason"])
	}
}

func TestSessionCreatedWithToolLogged(t *testing.T) {
	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: dir, Level: "info"})
	defer logging.Shutdown()

	inst := NewInstanceWithTool("ut-with-tool", "/tmp/proj-b", "claude")
	records := readLogRecords(t, dir)
	rec := findRecord(records, "session_created")
	if rec == nil {
		t.Fatal("expected session_created log line for NewInstanceWithTool")
	}
	if rec["tool"] != "claude" {
		t.Errorf("tool = %v, want claude", rec["tool"])
	}
	if rec["instance_id"] != inst.ID {
		t.Errorf("instance_id = %v, want %s", rec["instance_id"], inst.ID)
	}
}
