package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func useTempAgentDeckDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".agent-deck")
	prev := agentDeckDirOverride
	agentDeckDirOverride = dir
	t.Cleanup(func() { agentDeckDirOverride = prev })
	return dir
}

// The residue record holds hashes only, private permissions, and round-trips.
func TestFileSendResidueStore_RoundTripPrivateNoText(t *testing.T) {
	root := useTempAgentDeckDir(t)
	const draft = "step 01: run `go build ./...` and report"
	store := FileSendResidueStore{}
	rec := SendResidue{DraftSHA256: SendResidueHash(draft), DraftLen: len(draft), MessageSHA256: SendResidueHash(draft), Verified: true, Sender: "sender-1", CreatedAt: time.Now()}
	if err := store.Save("inst-1", rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := store.Load("inst-1")
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if got.DraftSHA256 != rec.DraftSHA256 || !got.Verified || got.InstanceID != "inst-1" || got.Version != sendResidueVersion {
		t.Fatalf("Load = %+v", got)
	}
	dir := filepath.Join(root, "locks", "send-residue")
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %v err=%v, want 0700", info, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want one record", entries)
	}
	path := filepath.Join(dir, entries[0].Name())
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("record perm = %o, want 600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "go build") || strings.Contains(entries[0].Name(), "inst-1") {
		t.Fatalf("record leaks text or the raw id: %s / %s", entries[0].Name(), data)
	}
	if _, ok, _ := store.Load("inst-2"); ok {
		t.Fatal("another instance must not see the record")
	}
	if err := store.Clear("inst-1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if err := store.Clear("inst-1"); err != nil {
		t.Fatalf("Clear of a missing record: %v", err)
	}
	if _, ok, _ := store.Load("inst-1"); ok {
		t.Fatal("record survived Clear")
	}
}

func TestFileSendResidueStore_StaleRecordIsIgnoredAndRemoved(t *testing.T) {
	useTempAgentDeckDir(t)
	store := FileSendResidueStore{}
	if err := store.Save("inst-1", SendResidue{DraftSHA256: SendResidueHash("x"), Verified: true, CreatedAt: time.Now().Add(-SendResidueKeepAge - time.Minute)}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok, err := store.Load("inst-1"); ok || err != nil {
		t.Fatalf("stale record: ok=%v err=%v, want ignored", ok, err)
	}
	path, _ := sendResiduePath("inst-1")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale record not removed: %v", err)
	}
}

// Review finding 7: records are kept up to 7 days so a refusal can still
// name the residue's origin and age; only after that are they removed.
func TestFileSendResidueStore_KeepsRecordsSevenDaysForNaming(t *testing.T) {
	useTempAgentDeckDir(t)
	store := FileSendResidueStore{}
	if err := store.Save("inst-1", SendResidue{DraftSHA256: SendResidueHash("x"), Verified: true, CreatedAt: time.Now().Add(-72 * time.Hour)}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok, err := store.Load("inst-1"); !ok || err != nil {
		t.Fatalf("3-day-old record: ok=%v err=%v, want kept for naming", ok, err)
	}
	if err := store.Save("inst-2", SendResidue{DraftSHA256: SendResidueHash("x"), Verified: true, CreatedAt: time.Now().Add(-8 * 24 * time.Hour)}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok, _ := store.Load("inst-2"); ok {
		t.Fatal("8-day-old record must be dropped")
	}
	path, _ := sendResiduePath("inst-2")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("8-day-old record not removed: %v", err)
	}
}
