package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reproduction of the credential leak: a failed restart with --env used to
// persist the literal `export API_KEY='secret'` into a 0644 sidecar exposed
// by session show. The record writer must redact values and write 0600.
func TestSpawnFailureRecord_RedactsEnvValuesAndTightensPerms(t *testing.T) {
	// A child the writer has to create itself, so the 0700 directory mode is
	// exercised rather than inherited from t.TempDir().
	dir := filepath.Join(t.TempDir(), "runtime", "spawn-failure")
	rec := SpawnFailureRecord{
		InstanceID:  "leaktest",
		Tool:        "generic",
		Command:     `export API_KEY='sk-live-SECRET' && export NOTE='it'\''s quoted' && mytool run`,
		Reason:      "prepare_failed",
		DyingOutput: `prepare failed running: export API_KEY='sk-live-SECRET' && mytool`,
	}
	if err := writeSpawnFailureRecordTo(rec, dir); err != nil {
		t.Fatalf("write: %v", err)
	}
	path := filepath.Join(dir, "leaktest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "sk-live-SECRET") || strings.Contains(string(data), "quoted") {
		t.Fatalf("credential value leaked into sidecar: %s", data)
	}
	var got SpawnFailureRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(got.Command, "export API_KEY='[redacted]'") {
		t.Fatalf("key name must stay visible with redacted value, got: %s", got.Command)
	}
	if !strings.Contains(got.Command, "mytool run") {
		t.Fatalf("non-secret command tail must survive, got: %s", got.Command)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar perms = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("spawn-failure dir perms = %o, want 700", dirInfo.Mode().Perm())
	}
}

// Upgrade path (#1934 review): the directory and the sidecars in it already
// exist from a version that wrote them 0755/0644 with unredacted credentials.
// os.MkdirAll leaves an existing directory's mode alone, so without an explicit
// sweep the fix above would only protect fresh installs while the credentials
// that actually leaked stayed world-readable on disk.
func TestSpawnFailureRecord_TightensExistingDirAndSweepsOldSidecars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spawn-failure")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // defeat any umask narrowing
		t.Fatalf("chmod dir: %v", err)
	}
	old := filepath.Join(dir, "old-session.json")
	oldBody := `{
  "instance_id": "old-session",
  "tool": "generic",
  "command": "export API_KEY='sk-live-OLDSECRET' && mytool run",
  "reason": "prepare_failed",
  "dying_output": "prepare failed running: export API_KEY='sk-live-OLDSECRET'",
  "elapsed_ms": 0,
  "ts": 1765400000
}
`
	if err := os.WriteFile(old, []byte(oldBody), 0o644); err != nil {
		t.Fatalf("write old sidecar: %v", err)
	}
	if err := os.Chmod(old, 0o644); err != nil {
		t.Fatalf("chmod old sidecar: %v", err)
	}

	if err := writeSpawnFailureRecordTo(SpawnFailureRecord{
		InstanceID: "new-session",
		Tool:       "generic",
		Reason:     "prepare_failed",
	}, dir); err != nil {
		t.Fatalf("write: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("existing dir perms = %o, want 700", dirInfo.Mode().Perm())
	}
	oldInfo, err := os.Stat(old)
	if err != nil {
		t.Fatalf("stat old sidecar: %v", err)
	}
	if oldInfo.Mode().Perm() != 0o600 {
		t.Fatalf("old sidecar perms = %o, want 600", oldInfo.Mode().Perm())
	}
	data, err := os.ReadFile(old)
	if err != nil {
		t.Fatalf("read old sidecar: %v", err)
	}
	// Tight perms alone would only hide the credential; it must be gone.
	if strings.Contains(string(data), "sk-live-OLDSECRET") {
		t.Fatalf("pre-existing credential still on disk: %s", data)
	}
	var got SpawnFailureRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("rewritten sidecar is not valid JSON: %v (%s)", err, data)
	}
	if !strings.Contains(got.Command, "export API_KEY='[redacted]'") || !strings.Contains(got.Command, "mytool run") {
		t.Fatalf("rewrite must redact the value and keep the rest, got: %s", got.Command)
	}
	if got.InstanceID != "old-session" || got.Reason != "prepare_failed" || got.Timestamp != 1765400000 {
		t.Fatalf("rewrite lost non-secret fields: %+v", got)
	}
}
