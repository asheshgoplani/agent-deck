package session

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

func TestCompletionLedgerWriteReadLastWins(t *testing.T) {
	const childID = "ledgertest-child-1"
	// Isolate from any state a prior run left behind: the ledger path is a fixed,
	// process-shared file, so without cleanup "expected no entry before write" is flaky.
	if p, err := completionLedgerPath(childID); err == nil {
		_ = os.Remove(p)
		t.Cleanup(func() { _ = os.Remove(p) })
	}
	if _, ok := ReadLedgerEntry(childID); ok {
		t.Fatalf("expected no entry before write")
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: childID, Profile: "p", Title: "T", Status: "ok", Summary: "first"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: childID, Profile: "p", Title: "T", Status: "fail", Summary: "second"}); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	got, ok := ReadLedgerEntry(childID)
	if !ok {
		t.Fatalf("expected entry after write")
	}
	if got.Status != "fail" || got.Summary != "second" {
		t.Fatalf("last-wins failed: got %+v", got)
	}
}

// TestCompletionLedgerConcurrentWrites exercises the per-write temp file: many
// goroutines writing the same child must never clobber each other's temp file
// before rename, and the surviving entry must be a complete, parseable record
// (not a torn half-write). Runs under -race in CI.
func TestCompletionLedgerConcurrentWrites(t *testing.T) {
	const childID = "ledgertest-concurrent"
	if p, err := completionLedgerPath(childID); err == nil {
		_ = os.Remove(p)
		t.Cleanup(func() { _ = os.Remove(p) })
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := WriteLedgerEntry(CompletionLedgerEntry{
				ChildID: childID, Profile: "p", Status: "ok",
				Summary: fmt.Sprintf("write-%d", n),
			}); err != nil {
				t.Errorf("concurrent write %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()
	got, ok := ReadLedgerEntry(childID)
	if !ok {
		t.Fatalf("expected an entry after concurrent writes")
	}
	if got.Status != "ok" || got.Summary == "" {
		t.Fatalf("got torn/incomplete entry: %+v", got)
	}
}

func TestCompletionLedgerWriteRejectsEmptyID(t *testing.T) {
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "  "}); err == nil {
		t.Fatalf("expected error on empty child id")
	}
}
