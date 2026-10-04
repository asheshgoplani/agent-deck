package events

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWantLeaseLifecycle (#2481 item 6): a demand kind is wanted only while
// a follower holds a fresh lease; release and staleness both end it.
func TestWantLeaseLifecycle(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Wants(KindTmuxOutput) {
		t.Fatal("no follower yet, but tmux.output is wanted")
	}
	release := b.Want(KindTmuxOutput, "session.status")
	if !b.Wants(KindTmuxOutput) {
		t.Fatal("a live follower's lease is not seen")
	}
	if b.Wants("session.status") {
		t.Fatal("a non-demand kind got a lease")
	}
	release()
	release() // idempotent
	if b.Wants(KindTmuxOutput) {
		t.Fatal("released lease still counts")
	}
	entries, _ := os.ReadDir(filepath.Join(b.dir, demandDirName))
	if len(entries) != 0 {
		t.Fatalf("release left %d lease files", len(entries))
	}

	// A follower that died without releasing stops counting after demandTTL
	// and the next Want sweeps its file.
	dead := filepath.Join(b.dir, demandDirName, KindTmuxOutput+".1.dead")
	if err := os.WriteFile(dead, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * demandTTL)
	if err := os.Chtimes(dead, old, old); err != nil {
		t.Fatal(err)
	}
	if b.Wants(KindTmuxOutput) {
		t.Fatal("stale lease still counts")
	}
	b.Want(KindTmuxOutput)()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("stale lease not swept: %v", err)
	}
}

// TestWantNoopOnReadOnlyAndDisabled: a follower never writes into a read-only
// bus, and a disabled bus wants nothing.
func TestWantNoopOnReadOnlyAndDisabled(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ro, err := OpenAt(dir, Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	ro.Want(KindTmuxOutput)()
	if _, err := os.Stat(filepath.Join(dir, demandDirName)); !os.IsNotExist(err) {
		t.Fatalf("read-only follower created the lease dir: %v", err)
	}
	d := disabledBus()
	d.Want(KindTmuxOutput)()
	if d.Wants(KindTmuxOutput) {
		t.Fatal("disabled bus wants tmux.output")
	}
}

// TestDefaultWantsFollowsLeases: the producer-side check sees a follower on
// the default bus within the cache window.
func TestDefaultWantsFollowsLeases(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)
	SetProfile("demand")
	t.Cleanup(func() { SetProfile("default") })
	if DefaultWants(KindTmuxOutput) {
		t.Fatal("wanted with no follower")
	}
	release := Default().Want(KindTmuxOutput)
	defer release()
	deadline := time.Now().Add(demandRecheck + time.Second)
	for !DefaultWants(KindTmuxOutput) {
		if time.Now().After(deadline) {
			t.Fatal("DefaultWants never saw the follower")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// BenchmarkDefaultWants is the per-%output cost the tmux producer now pays
// before deciding whether to publish.
func BenchmarkDefaultWants(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	b.Setenv("XDG_DATA_HOME", filepath.Join(b.TempDir(), "data"))
	SetProfile("demand-bench")
	b.Cleanup(func() { SetProfile("default") })
	for i := 0; i < b.N; i++ {
		DefaultWants(KindTmuxOutput)
	}
}

// TestSweptLiveLeaseRecovers: a live follower whose lease was removed (another
// follower swept it after this one's refresh came late, e.g. across a laptop
// sleep or SIGSTOP) gets it back on its next refresh instead of losing
// tmux.output for good.
func TestSweptLiveLeaseRecovers(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	release := b.Want(KindTmuxOutput)
	defer release()
	dir := filepath.Join(b.dir, demandDirName)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one lease, got %d (%v)", len(entries), err)
	}
	old := time.Now().Add(-2 * demandTTL)
	lease := filepath.Join(dir, entries[0].Name())
	if err := os.Chtimes(lease, old, old); err != nil {
		t.Fatal(err)
	}
	b.Want(KindTmuxOutput)() // a second follower sweeps the late lease
	if b.Wants(KindTmuxOutput) {
		t.Fatal("setup: the late lease was not swept")
	}
	deadline := time.Now().Add(demandRefresh + 2*time.Second)
	for !b.Wants(KindTmuxOutput) {
		if time.Now().After(deadline) {
			t.Fatal("live follower lost its lease permanently after a sweep")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
