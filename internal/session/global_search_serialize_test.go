//go:build !windows

package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newSerializeTestIndex(t *testing.T, claudeDir string) *GlobalSearchIndex {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	idx := &GlobalSearchIndex{
		claudeDir:        claudeDir,
		fileTrackers:     map[string]*FileTracker{},
		tier:             TierInstant,
		memoryLimitBytes: 1 << 30,
		ctx:              ctx,
		cancel:           cancel,
	}
	empty := []SearchEntry{}
	idx.entries.Store(&empty)
	return idx
}

func serializeTestLine(id string) string {
	return `{"sessionId":"` + id + `","type":"user","message":{"role":"user","content":"hello"},"cwd":"/x"}` + "\n"
}

func serializeTestHasPath(idx *GlobalSearchIndex, path string) bool {
	for _, e := range *idx.entries.Load() {
		if e.FilePath == path {
			return true
		}
	}
	return false
}

// Two concurrent updateFile calls (watcherLoop runs one debounce goroutine
// per file) must not drop each other's entry. File A is a FIFO so the first
// updateFile is parked between its entries Load and Store while B runs.
func TestGlobalSearchUpdateFileConcurrentNoLostUpdate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projects", "-p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	idA := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	idB := "b2c3d4e5-f6a7-8901-bcde-f23456789012"
	a := filepath.Join(dir, idA+".jsonl")
	b := filepath.Join(dir, idB+".jsonl")
	if err := syscall.Mkfifo(a, 0o644); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	if err := os.WriteFile(b, []byte(serializeTestLine(idB)), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := newSerializeTestIndex(t, filepath.Dir(filepath.Dir(dir)))

	doneA := make(chan struct{})
	go func() { idx.updateFile(a); close(doneA) }()

	// Opening the FIFO for writing returns once updateFile(A) has opened it
	// for reading, i.e. after it loaded the (empty) entries slice.
	type openRes struct {
		f   *os.File
		err error
	}
	opened := make(chan openRes, 1)
	go func() {
		f, err := os.OpenFile(a, os.O_WRONLY, 0)
		opened <- openRes{f, err}
	}()
	var w *os.File
	select {
	case r := <-opened:
		if r.err != nil {
			t.Fatal(r.err)
		}
		w = r.f
	case <-time.After(5 * time.Second):
		t.Fatal("updateFile(A) never opened the FIFO")
	}

	doneB := make(chan struct{})
	go func() { idx.updateFile(b); close(doneB) }()
	// Unserialized code finishes B now; serialized code blocks B on A.
	select {
	case <-doneB:
	case <-time.After(time.Second):
	}

	if _, err := w.WriteString(serializeTestLine(idA)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	for _, ch := range []chan struct{}{doneA, doneB} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("updateFile hung")
		}
	}

	if got := idx.EntryCount(); got != 2 || !serializeTestHasPath(idx, a) || !serializeTestHasPath(idx, b) {
		t.Fatalf("EntryCount=%d hasA=%v hasB=%v, want both entries (concurrent updateFile lost an update)",
			got, serializeTestHasPath(idx, a), serializeTestHasPath(idx, b))
	}
}

// initialLoad's final Store must not discard an entry that a concurrent
// updateFile added while the walk was running.
func TestGlobalSearchInitialLoadKeepsConcurrentUpdate(t *testing.T) {
	claudeDir := t.TempDir()
	dir := filepath.Join(claudeDir, "projects", "-p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	idY := "c3d4e5f6-a7b8-9012-cdef-345678901234"
	y := filepath.Join(dir, idY+".jsonl")
	if err := os.WriteFile(y, []byte(serializeTestLine(idY)), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := newSerializeTestIndex(t, claudeDir)
	x := filepath.Join(dir, "d4e5f6a7-b8c9-0123-def0-456789012345.jsonl")
	// x: added by updateFile, not walked. y: an older updateFile copy that
	// the walk re-reads in full, so the walk's copy must win.
	pre := []SearchEntry{{FilePath: x, SessionID: "x", CWD: "/x", Summary: "x"}, {FilePath: y, SessionID: idY}}
	idx.entries.Store(&pre)
	idx.loading.Store(true)

	idx.wg.Add(1)
	idx.initialLoad()

	if !serializeTestHasPath(idx, x) || !serializeTestHasPath(idx, y) {
		t.Fatalf("hasX=%v hasY=%v, want both (initialLoad overwrote a concurrent update)",
			serializeTestHasPath(idx, x), serializeTestHasPath(idx, y))
	}
	if e := serializeTestEntry(idx, y); e == nil || e.CWD == "" || !strings.Contains(e.ContentString(), "hello") {
		t.Fatalf("y entry %+v lost the walk's full read (stale copy won)", e)
	}
}

func serializeTestMsg(id, text string) string {
	return `{"sessionId":"` + id + `","type":"user","message":{"role":"user","content":"` + text + `"},"cwd":"/x"}` + "\n"
}

func serializeTestEntry(idx *GlobalSearchIndex, path string) *SearchEntry {
	for _, e := range *idx.entries.Load() {
		if e.FilePath == path {
			return &e
		}
	}
	return nil
}

// A file the walk already read, appended and re-indexed by updateFile while
// the walk is still running, must end up with both its old and new content.
// The walk blocks on a FIFO that sorts after the walked file.
func TestGlobalSearchInitialLoadConcurrentAppendKeepsFullContent(t *testing.T) {
	claudeDir := t.TempDir()
	dir := filepath.Join(claudeDir, "projects", "-p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	idX := "11111111-e5f6-7890-abcd-ef1234567890"
	idF := "ffffffff-f6a7-8901-bcde-f23456789012"
	x := filepath.Join(dir, idX+".jsonl")
	fifo := filepath.Join(dir, idF+".jsonl")
	if err := os.WriteFile(x, []byte(serializeTestMsg(idX, "firstpart")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	idx := newSerializeTestIndex(t, claudeDir)
	idx.loading.Store(true)
	idx.wg.Add(1)
	loaded := make(chan struct{})
	go func() { idx.initialLoad(); close(loaded) }()

	// The walk opens the FIFO only after it has read and tracked x.
	opened := make(chan *os.File, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
		}
		opened <- f
	}()
	var w *os.File
	select {
	case w = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("walk never opened the FIFO")
	}
	if w == nil {
		t.FailNow()
	}

	fx, err := os.OpenFile(x, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fx.WriteString(serializeTestMsg(idX, "secondpart"))
	_ = fx.Close()
	updated := make(chan struct{})
	go func() { idx.updateFile(x); close(updated) }()
	select { // unserialized code finishes now; serialized code waits for the walk
	case <-updated:
	case <-time.After(500 * time.Millisecond):
	}

	_, _ = w.WriteString(serializeTestMsg(idF, "fifo"))
	_ = w.Close()
	for _, ch := range []chan struct{}{loaded, updated} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("initialLoad or updateFile hung")
		}
	}

	e := serializeTestEntry(idx, x)
	if e == nil {
		t.Fatal("entry for x missing")
	}
	got := e.ContentString()
	if !strings.Contains(got, "firstpart") || !strings.Contains(got, "secondpart") || e.CWD == "" {
		t.Fatalf("content=%q cwd=%q, want both parts and cwd (concurrent append lost data)", got, e.CWD)
	}
}

// After Close, an in-flight debounce callback must not repopulate entries.
func TestGlobalSearchUpdateFileAfterCloseStoresNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projects", "-p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "c3d4e5f6-a7b8-9012-cdef-345678901234"
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(serializeTestLine(id)), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := newSerializeTestIndex(t, filepath.Dir(filepath.Dir(dir)))
	idx.Close()
	idx.updateFile(p)
	if got := idx.EntryCount(); got != 0 {
		t.Fatalf("EntryCount=%d after Close, want 0", got)
	}
}
