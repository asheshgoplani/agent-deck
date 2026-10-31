package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKimiOwnedSessionColdResumeRotationAndFreshClear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := withTempGlobalStateDB(t)
	inst := NewInstanceWithTool("kimi-owned-resume", t.TempDir(), "kimi")
	saveKimiRow(t, db, inst)

	if err := WriteKimiOwnedSessionID(inst.ID, " native-first "); err != nil {
		t.Fatalf("WriteKimiOwnedSessionID: %v", err)
	}
	inst.prepareKimiResume()
	if inst.KimiSessionID != "native-first" || inst.kimiBoundAt.IsZero() {
		t.Fatalf("cold resume binding = %q at %v", inst.KimiSessionID, inst.kimiBoundAt)
	}
	if got, _ := persistedKimiBinding(t, db, inst.ID); got != "native-first" {
		t.Fatalf("persisted cold binding = %q, want native-first", got)
	}
	if command := inst.buildKimiCommand("kimi"); !strings.HasSuffix(command, "kimi --session native-first") {
		t.Fatalf("cold resume command = %q", command)
	}

	if err := WriteKimiOwnedSessionID(inst.ID, "native-rotated"); err != nil {
		t.Fatalf("rotate owned session: %v", err)
	}
	inst.prepareKimiResume()
	if inst.KimiSessionID != "native-rotated" {
		t.Fatalf("rotated binding = %q, want native-rotated", inst.KimiSessionID)
	}

	sandboxDir := hermesHookScope(inst.ID, true)
	if err := os.MkdirAll(sandboxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sandboxDir, inst.ID+kimiOwnedSessionSuffix), []byte(`{"id":"native-sandbox"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inst.clearSessionBindingForFreshStart()
	for _, dir := range []string{hermesHookScope(inst.ID, false), sandboxDir} {
		if _, err := os.Stat(filepath.Join(dir, inst.ID+kimiOwnedSessionSuffix)); !os.IsNotExist(err) {
			t.Fatalf("fresh restart left owned session record in %s; err=%v", dir, err)
		}
	}
	inst.prepareKimiResume()
	if inst.KimiSessionID != "" {
		t.Fatalf("fresh restart restored session %q", inst.KimiSessionID)
	}
}
