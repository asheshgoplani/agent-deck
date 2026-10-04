package events

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Verifier defect 3: after a rotation the active file holds only malformed
// lines. The ledger bus must open with each of them counted as a spent
// cursor on top of the sealed history (absolute, not relative to zero), in
// Open and in the refresh that runs before every commit.
func TestLedgerBusOpensAnAllCorruptActiveFileAfterRotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	opts := Options{Private: true, KeepCorrupt: true, MaxSegmentBytes: 1}
	b, err := OpenAt(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	commitN(t, b, 3) // every frame rotates: seg 1-1, 2-2, 3-3, checkpoint 3
	_ = b.Close()
	if err := os.WriteFile(filepath.Join(dir, activeSegmentName), []byte("{garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b2, err := OpenAt(dir, Options{Private: true, KeepCorrupt: true})
	if err != nil {
		t.Fatalf("ledger failed to open an all-corrupt active file: %v", err)
	}
	defer b2.Close()
	if b2.Cursor() != 4 {
		t.Fatalf("cursor %d, want 4 (the malformed line spent cursor 4)", b2.Cursor())
	}
	if st := b2.Stats(); st.Cursor != 4 {
		t.Fatalf("refreshed cursor %d, want 4", st.Cursor)
	}
	f, err := b2.Commit("k", "s", nil)
	if err != nil || f.Cursor != 5 {
		t.Fatalf("commit after recovery: %+v %v, want cursor 5", f, err)
	}
	r, err := OpenAt(dir, Options{ReadOnly: true, KeepCorrupt: true})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if st := r.Stats(); st.Cursor != 5 {
		t.Fatalf("follower stats cursor %d, want 5", st.Cursor)
	}
	end, lastFrame, err := r.Ends()
	if err != nil || end != 5 || lastFrame != 5 {
		t.Fatalf("Ends() = %d, %d, %v; want 5, 5", end, lastFrame, err)
	}
}

// Ends reports the readable end separately from the cursor: a trailing
// malformed line spends a cursor that no frame will ever carry, so a reader
// waits for the last parseable frame, not for the cursor.
func TestEndsSeparatesTheLastFrameFromSpentCursors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	b, err := OpenAt(dir, Options{Private: true, KeepCorrupt: true})
	if err != nil {
		t.Fatal(err)
	}
	commitN(t, b, 3)
	_ = b.Close()
	corruptLog(t, dir, 2, false)
	r, err := OpenAt(dir, Options{ReadOnly: true, KeepCorrupt: true})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	end, lastFrame, err := r.Ends()
	if err != nil || end != 3 || lastFrame != 2 {
		t.Fatalf("Ends() = %d, %d, %v; want 3, 2", end, lastFrame, err)
	}
}

// Verifier defect 4: on the ledger bus a commit whose fsync failed is never
// visible to a follower (followers read the active file under the writer
// lock, so they see a frame only once its Commit finished), and its cursor
// number is spent: the reopened bus never hands it to a different frame.
func TestLedgerRolledBackCommitIsNeverVisibleAndItsCursorIsSpent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	opts := Options{Private: true, KeepCorrupt: true}
	b, err := OpenAt(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	commitN(t, b, 1)
	r, err := OpenAt(dir, Options{ReadOnly: true, KeepCorrupt: true})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Hold the follower between its segment listing and its read of the
	// active file until the writer has appended (but not synced) the frame:
	// the window in which an unlocked read would expose it.
	entered, written := make(chan struct{}), make(chan struct{})
	var once bool
	hook := func() {
		if once {
			return
		}
		once = true
		close(entered)
		select {
		case <-written:
		case <-time.After(5 * time.Second):
		}
	}
	subscribeActiveHook.Store(&hook)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	sub, _ := r.Subscribe(ctx, 1)
	defer func() {
		// Stop the follower before clearing the seam it reads.
		cancel()
		for range sub.Frames() {
		}
		subscribeActiveHook.Store(nil)
	}()
	<-entered
	commitFault = func(at string, _ *Bus) error {
		if at != "before-sync" {
			return nil
		}
		// The frame is written but not synced; let the follower read now
		// and give it time to (wrongly) emit what it read.
		close(written)
		time.Sleep(300 * time.Millisecond)
		return errors.New("injected fsync failure")
	}
	_, err = b.Commit("k", "s", map[string]string{"text": "rolled back"})
	commitFault = nil
	if err == nil {
		t.Fatal("a failed fsync reported success")
	}
	select {
	case f := <-sub.Frames():
		t.Fatalf("follower saw a frame that was never committed: %d %s", f.Cursor, f.Data)
	case <-time.After(200 * time.Millisecond):
	}
	_ = b.Close()
	b2, err := OpenAt(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	f, err := b2.Commit("k", "s", map[string]string{"text": "real"})
	if err != nil || f.Cursor != 3 {
		t.Fatalf("commit after the rollback: %+v %v; want cursor 3 (2 is spent)", f, err)
	}
	select {
	case got := <-sub.Frames():
		if got.Cursor != 3 || string(got.Data) != `{"text":"real"}` {
			t.Fatalf("follower got %d %s, want the real frame 3", got.Cursor, got.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower never saw the real frame")
	}
}

// Verifier round 1: a rotation between a follower's segment listing and its
// read of the active file must not make it read the NEW active file as if
// it were the listed one (skipping the frames just sealed). On the ledger
// bus the read re-checks for a rotation under the lock and re-lists.
func TestLedgerFollowerNeverSkipsFramesSealedBetweenListAndRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	b, err := OpenAt(dir, Options{Private: true, KeepCorrupt: true})
	if err != nil {
		t.Fatal(err)
	}
	commitN(t, b, 2)
	_ = b.Close()
	info, err := os.Stat(filepath.Join(dir, activeSegmentName))
	if err != nil {
		t.Fatal(err)
	}
	// The next commit pushes the active file past the bound and rotates.
	w, err := OpenAt(dir, Options{Private: true, KeepCorrupt: true, MaxSegmentBytes: info.Size() + 1})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r, err := OpenAt(dir, Options{ReadOnly: true, KeepCorrupt: true})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var once bool
	hook := func() {
		if once {
			return
		}
		once = true
		commitN(t, w, 2) // 3 seals 1-3, 4 starts the new active file
	}
	subscribeActiveHook.Store(&hook)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	sub, _ := r.Subscribe(ctx, 0)
	defer func() {
		cancel()
		for range sub.Frames() {
		}
		subscribeActiveHook.Store(nil)
	}()
	var seen []Cursor
	for f := range sub.Frames() {
		seen = append(seen, f.Cursor)
		if f.Cursor >= 4 {
			break
		}
	}
	if len(seen) != 4 || seen[0] != 1 || seen[1] != 2 || seen[2] != 3 || seen[3] != 4 {
		t.Fatalf("follower saw %v, want [1 2 3 4]", seen)
	}
}

// Verifier round 1: a clean ledger is never JSON-scanned line by line on
// the hot paths (Commit's refresh, a follower's listing, Ends); the full
// scan runs only when the first or last line does not parse.
func TestCleanLedgerNeverTakesTheLenientScan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	b, err := OpenAt(dir, Options{Private: true, KeepCorrupt: true})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	commitN(t, b, 1000)
	r, err := OpenAt(dir, Options{ReadOnly: true, KeepCorrupt: true})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	before := lenientScans.Load()
	commitN(t, b, 1)
	if end, last, err := r.Ends(); err != nil || end != 1001 || last != 1001 {
		t.Fatalf("Ends = %d, %d, %v", end, last, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	sub, _ := r.Subscribe(ctx, 999)
	n := 0
	for range sub.Frames() {
		if n++; n == 2 {
			cancel()
		}
	}
	cancel()
	if got := lenientScans.Load() - before; got != 0 {
		t.Fatalf("clean ledger took the lenient scan %d times", got)
	}
	_ = b.Close()
	corruptLog(t, dir, 1000, false)
	if _, last, err := r.Ends(); err != nil || last != 1000 || lenientScans.Load() == before {
		t.Fatalf("a corrupt last line must fall back to the scan: last %d err %v", last, err)
	}
}
