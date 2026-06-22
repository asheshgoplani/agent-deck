package session

import "testing"

func TestCompletionLedgerWriteReadLastWins(t *testing.T) {
	if _, ok := ReadLedgerEntry("ledgertest-child-1"); ok {
		t.Fatalf("expected no entry before write")
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "ledgertest-child-1", Profile: "p", Title: "T", Status: "ok", Summary: "first"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "ledgertest-child-1", Profile: "p", Title: "T", Status: "fail", Summary: "second"}); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	got, ok := ReadLedgerEntry("ledgertest-child-1")
	if !ok {
		t.Fatalf("expected entry after write")
	}
	if got.Status != "fail" || got.Summary != "second" {
		t.Fatalf("last-wins failed: got %+v", got)
	}
}

func TestCompletionLedgerWriteRejectsEmptyID(t *testing.T) {
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "  "}); err == nil {
		t.Fatalf("expected error on empty child id")
	}
}
