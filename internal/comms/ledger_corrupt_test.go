package comms

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// corruptLedgerLine replaces one line of the active ledger file.
func corruptLedgerLine(t *testing.T, dir string, idx int) {
	t.Helper()
	path := filepath.Join(dir, "active.ndjson")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	lines[idx] = "{garbage\n"
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitTexts(t *testing.T, l *Ledger, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if _, _, err := l.Commit(Record{Kind: KindTurn, From: "c", Key: Key(KindTurn, "c", s), Text: s}); err != nil {
			t.Fatal(err)
		}
	}
}

// Verifier defect 1: a reader opened through OpenReaderDir sees every
// parseable record when the first or the last line is corrupt, never an
// empty ledger with no error.
func TestOpenReaderDirSeesRecordsAroundACorruptFirstOrLastLine(t *testing.T) {
	for _, idx := range []int{0, 2} {
		dir := filepath.Join(t.TempDir(), "ledger")
		l, err := OpenDir("p", dir)
		if err != nil {
			t.Fatal(err)
		}
		commitTexts(t, l, "a", "b", "c")
		_ = l.Close()
		corruptLedgerLine(t, dir, idx)
		bus, err := OpenReaderDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		recs, last, err := ReadAfter(bus, 0, 0)
		_ = bus.Close()
		if err != nil || len(recs) != 2 || last != 3 {
			t.Fatalf("corrupt line %d: ReadAfter = %d records, last %d, err %v; want 2 records, last 3", idx, len(recs), last, err)
		}
	}
}

// Verifier defect 2: a malformed last line never blocks the ledger. Open
// succeeds promptly (the dedup rebuild stops at the last parseable frame),
// the window still holds the records before it, the next commit takes the
// cursor after the spent one, and a reader's pass ends without error.
func TestLedgerOpensPromptlyPastACorruptLastLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	commitTexts(t, l, "a", "b", "c")
	_ = l.Close()
	corruptLedgerLine(t, dir, 2)
	start := time.Now()
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatalf("open past a corrupt last line: %v", err)
	}
	defer l2.Close()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("open took %v", took)
	}
	if _, ok := l2.Lookup(Key(KindTurn, "c", "b")); !ok {
		t.Fatal("dedup window lost the records before the corrupt line")
	}
	_, cur, err := l2.Commit(Record{Kind: KindTurn, From: "c", Key: Key(KindTurn, "c", "d"), Text: "d"})
	if err != nil || cur != 4 {
		t.Fatalf("next commit: cursor %d err %v; want 4 (3 is spent)", cur, err)
	}
	bus, err := OpenReaderDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	_ = l2.Close()
	corruptLedgerLine(t, dir, 3) // the new last line too
	start = time.Now()
	recs, last, err := ReadAfter(bus, 0, 0)
	if err != nil || len(recs) != 2 || last != 4 {
		t.Fatalf("ReadAfter = %d records, last %d, err %v; want 2 records, last 4", len(recs), last, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("read took %v", took)
	}
	if recs, last, err := ReadAfter(bus, 2, 0); err != nil || len(recs) != 0 || last != 4 {
		t.Fatalf("ReadAfter past the last frame = %d records, last %d, err %v; want 0, 4", len(recs), last, err)
	}
}
