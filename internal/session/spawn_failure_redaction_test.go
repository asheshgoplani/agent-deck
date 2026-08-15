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
	dir := t.TempDir()
	rec := SpawnFailureRecord{
		InstanceID: "leaktest",
		Tool:       "generic",
		Command:    `export API_KEY='sk-live-SECRET' && export NOTE='it'\''s quoted' && mytool run`,
		Reason:     "prepare_failed",
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
}
