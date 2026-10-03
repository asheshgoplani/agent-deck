package events

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestCommitIsDurableOrderedAndReturnsItsCursor(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 1; i <= 3; i++ {
		f, err := b.Commit("turn", "child", map[string]any{"n": i})
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		if f.Cursor != Cursor(i) {
			t.Fatalf("commit %d got cursor %d", i, f.Cursor)
		}
		if f.EventID == "" || f.Kind != "turn" || f.SessionID != "child" {
			t.Fatalf("commit %d frame %+v", i, f)
		}
		// Durable before Commit returns: the line is on disk now, not after a flush tick.
		lines := readLines(t, filepath.Join(dir, activeSegmentName))
		if len(lines) != i {
			t.Fatalf("after commit %d: %d lines on disk", i, len(lines))
		}
	}
	if b.synced.Load() != 3 || b.written.Load() != 3 {
		t.Fatalf("synced=%d written=%d", b.synced.Load(), b.written.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, _ := b.Subscribe(ctx, 0)
	var got []Cursor
	for f := range sub.Frames() {
		got = append(got, f.Cursor)
		if len(got) == 3 {
			cancel()
		}
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("subscribe saw %v", got)
	}
}

func TestCommitAndPublishShareOneCursorSequence(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.Publish("a", "s", nil)
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush")
	}
	f, err := b.Commit("b", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	b.Publish("c", "s", nil)
	if !b.Flush(2 * time.Second) {
		t.Fatal("flush")
	}
	if f.Cursor != 2 || b.Cursor() != 3 {
		t.Fatalf("commit cursor %d, bus cursor %d", f.Cursor, b.Cursor())
	}
	seen := map[Cursor]bool{}
	for _, line := range readLines(t, filepath.Join(dir, activeSegmentName)) {
		fr, err := ParseFrameLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if seen[fr.Cursor] {
			t.Fatalf("duplicate cursor %d", fr.Cursor)
		}
		seen[fr.Cursor] = true
	}
	if len(seen) != 3 {
		t.Fatalf("%d frames on disk", len(seen))
	}
}

func TestTwoWritersOnOneLogNeverShareACursor(t *testing.T) {
	dir := t.TempDir()
	w1, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Close()
	w2, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	var cursors []Cursor
	for i := 0; i < 10; i++ {
		w := w1
		if i%2 == 1 {
			w = w2
		}
		f, err := w.Commit("k", "s", nil)
		if err != nil {
			t.Fatal(err)
		}
		cursors = append(cursors, f.Cursor)
	}
	for i, c := range cursors {
		if c != Cursor(i+1) {
			t.Fatalf("cursor sequence %v", cursors)
		}
	}
}

func TestCommitRotatesAndRetentionDaysDropsOldSegments(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenAt(dir, Options{MaxSegmentBytes: 1, RetentionDays: 1, RetainSegments: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 0; i < 3; i++ {
		if _, err := b.Commit("k", "s", nil); err != nil {
			t.Fatal(err)
		}
	}
	sealed, err := listSealedSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != 3 {
		t.Fatalf("expected one sealed segment per commit at a 1-byte cap, got %d", len(sealed))
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(sealed[0].path, old, old); err != nil {
		t.Fatal(err)
	}
	// The next rotation compacts: the aged segment goes, the fresh ones stay.
	if _, err := b.Commit("k", "s", nil); err != nil {
		t.Fatal(err)
	}
	after, err := listSealedSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range after {
		if s.path == sealed[0].path {
			t.Fatalf("aged segment %s survived compaction", s.path)
		}
	}
	if len(after) != 3 {
		t.Fatalf("expected 3 fresh sealed segments, got %d", len(after))
	}
	if b.Cursor() != 4 {
		t.Fatalf("cursor %d", b.Cursor())
	}
}

func TestReadOnlyBusFollowsButNeverWrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenAt(dir+"/missing", Options{ReadOnly: true}); !errors.Is(err, ErrNoBus) {
		t.Fatalf("missing dir: %v", err)
	}
	w, err := OpenAt(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Commit("turn", "c", map[string]string{"text": "hi"}); err != nil {
		t.Fatal(err)
	}

	r, err := OpenAt(dir, Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.ReadOnly() {
		t.Fatal("ReadOnly() false")
	}
	if _, err := r.Commit("turn", "c", nil); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only commit: %v", err)
	}
	r.Publish("turn", "c", nil)
	if r.dropped.Load() != 1 {
		t.Fatalf("publish on a read-only bus must drop, dropped=%d", r.dropped.Load())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, _ := r.Subscribe(ctx, 0)
	var got []Frame
	go func() {
		// A second commit after the follower started must stream live.
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Commit("turn", "c", map[string]string{"text": "again"})
	}()
	for f := range sub.Frames() {
		got = append(got, f)
		if len(got) == 2 {
			cancel()
		}
	}
	if len(got) != 2 || got[0].Cursor != 1 || got[1].Cursor != 2 {
		t.Fatalf("follower saw %+v", got)
	}
	if st := r.Stats(); st.Cursor != 2 || !st.Enabled {
		t.Fatalf("read-only stats %+v", st)
	}
	if lines := readLines(t, filepath.Join(dir, activeSegmentName)); len(lines) != 2 {
		t.Fatalf("follower changed the log: %d lines", len(lines))
	}
}

func TestCommitOnClosedOrDisabledBusErrors(t *testing.T) {
	if _, err := disabledBus().Commit("k", "s", nil); err == nil {
		t.Fatal("disabled bus accepted a commit")
	}
	b, err := OpenAt(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	if _, err := b.Commit("k", "s", nil); err == nil {
		t.Fatal("closed bus accepted a commit")
	}
}

func corruptLog(t *testing.T, dir string, lineIndex int, tornTail bool) {
	t.Helper()
	path := filepath.Join(dir, activeSegmentName)
	data, _ := os.ReadFile(path)
	lines := strings.SplitAfter(string(data), "\n")
	lines[lineIndex] = "{garbage\n"
	out := strings.Join(lines, "")
	if tornTail {
		out += `{"cursor":9,"event_id":"x"`
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitN(t *testing.T, b *Bus, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := b.Commit("k", "s", map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
}

// The status bus keeps origin/main's recovery: a malformed line inside the
// active file truncates from that line (first, middle or last), the bus
// opens, and appends continue from the last good frame.
func TestStatusBusTruncatesFromACorruptLine(t *testing.T) {
	for _, idx := range []int{0, 1, 2} {
		dir := t.TempDir()
		b, err := OpenAt(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		commitN(t, b, 3)
		_ = b.Close()
		corruptLog(t, dir, idx, idx == 2)
		b2, err := OpenAt(dir, Options{})
		if err != nil {
			t.Fatalf("corrupt line %d: status bus failed to open: %v", idx, err)
		}
		if b2.Cursor() != Cursor(idx) {
			t.Fatalf("corrupt line %d: cursor %d, want %d (truncated from the bad line)", idx, b2.Cursor(), idx)
		}
		f, err := b2.Commit("k", "s", nil)
		if err != nil || f.Cursor != Cursor(idx+1) {
			t.Fatalf("corrupt line %d: commit after recovery %+v %v", idx, f, err)
		}
		if lines := readLines(t, filepath.Join(dir, activeSegmentName)); len(lines) != idx+1 {
			t.Fatalf("corrupt line %d: %d lines on disk", idx, len(lines))
		}
		_ = b2.Close()
	}
}

// The ledger bus keeps a malformed line in place (first, middle or last),
// opens, skips it on read, keeps every later frame and continues the
// cursor; a torn tail is still truncated.
func TestLedgerBusKeepsACorruptLineAndEveryLaterFrame(t *testing.T) {
	for _, idx := range []int{0, 1, 2} {
		dir := filepath.Join(t.TempDir(), "ledger")
		b, err := OpenAt(dir, Options{Private: true, KeepCorrupt: true})
		if err != nil {
			t.Fatal(err)
		}
		commitN(t, b, 3)
		_ = b.Close()
		corruptLog(t, dir, idx, true)
		b2, err := OpenAt(dir, Options{Private: true, KeepCorrupt: true})
		if err != nil {
			t.Fatalf("corrupt line %d: ledger failed to open: %v", idx, err)
		}
		if b2.Cursor() != 3 {
			t.Fatalf("corrupt line %d: cursor %d, want 3", idx, b2.Cursor())
		}
		after, _ := os.ReadFile(filepath.Join(dir, activeSegmentName))
		if strings.HasSuffix(string(after), `"event_id":"x"`) || !strings.Contains(string(after), "{garbage\n") {
			t.Fatalf("corrupt line %d: torn tail kept or corruption erased:\n%s", idx, after)
		}
		f, err := b2.Commit("k", "s", nil)
		if err != nil || f.Cursor != 4 {
			t.Fatalf("corrupt line %d: commit after recovery %+v %v", idx, f, err)
		}
		r, err := OpenAt(dir, Options{ReadOnly: true, KeepCorrupt: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		sub, _ := r.Subscribe(ctx, 0)
		var seen []Cursor
		for fr := range sub.Frames() {
			seen = append(seen, fr.Cursor)
			if fr.Cursor == 4 {
				cancel()
			}
		}
		cancel()
		// The corrupt line's cursor number is spent: the next commit is 4
		// whichever line was corrupt, so a consumer's acknowledgement of 3
		// can never apply to a frame it did not see.
		want := []Cursor{1, 2, 3, 4}
		want = append(want[:idx], want[idx+1:]...)
		if len(seen) != 3 || seen[0] != want[0] || seen[1] != want[1] || seen[2] != want[2] {
			t.Fatalf("corrupt line %d: follower saw %v, want %v", idx, seen, want)
		}
		if st := r.Stats(); st.Cursor != 4 {
			t.Fatalf("corrupt line %d: follower stats cursor %d", idx, st.Cursor)
		}
		_ = r.Close()
		_ = b2.Close()
	}
}

func TestPrivateBusCreatesOwnerOnlyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	b, err := OpenAt(dir, Options{Private: true, MaxSegmentBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 0; i < 2; i++ {
		if _, err := b.Commit("k", "s", nil); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v err %v", info.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) < 4 {
		t.Fatalf("expected active, sealed, lock and checkpoint files, got %d", len(entries))
	}
	for _, e := range entries {
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", e.Name(), fi.Mode().Perm())
		}
	}
}

// G1: a failed append (short write) and a failed fsync are both rolled
// back: no bytes left behind, cursor unchanged, error reported, the bus
// disabled until reopened, and the reopen continues from the last durable
// frame. Faults are injected through the commitFault seam AFTER the
// refresh, so the rollback code itself is what runs.
func TestCommitShortWriteAndSyncFailureAreRolledBack(t *testing.T) {
	for _, stage := range []string{"before-append", "before-sync"} {
		dir := t.TempDir()
		b, err := OpenAt(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Commit("k", "s", nil); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(dir, activeSegmentName))
		fired := false
		commitFault = func(at string, bus *Bus) error {
			if at != stage {
				return nil
			}
			fired = true
			if stage == "before-append" {
				// Swap the handle for a read-only one: the write fails
				// short and the rollback truncate runs on that handle.
				_ = bus.activeFile.Close()
				ro, err := os.Open(filepath.Join(dir, activeSegmentName))
				if err != nil {
					t.Fatal(err)
				}
				bus.activeFile = ro
				return nil
			}
			return errors.New("injected fsync failure")
		}
		_, err = b.Commit("k", "s", map[string]string{"text": "lost"})
		commitFault = nil
		if err == nil || !fired {
			t.Fatalf("%s: commit reported success (fired=%v)", stage, fired)
		}
		if b.Cursor() != 1 {
			t.Fatalf("%s: cursor advanced past a failed commit: %d", stage, b.Cursor())
		}
		if _, err := b.Commit("k", "s", nil); err == nil {
			t.Fatalf("%s: bus must stay disabled after a write failure until reopened", stage)
		}
		_ = b.Close()
		after, _ := os.ReadFile(filepath.Join(dir, activeSegmentName))
		if string(after) != string(before) {
			t.Fatalf("%s: a failed commit left bytes behind:\n%s", stage, after)
		}
		b2, err := OpenAt(dir, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if f, err := b2.Commit("k", "s", nil); err != nil || f.Cursor != 2 {
			t.Fatalf("%s: reopen must continue at cursor 2: %+v %v", stage, f, err)
		}
		_ = b2.Close()
	}
}
