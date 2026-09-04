package session

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestDeadLetterInspectionBudgetsAndEscapedErrors(t *testing.T) {
	inboxTestHome(t)
	if err := os.MkdirAll(DeadLetterDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := DeadLetterPathFor("limit")
	raw := bytes.Repeat([]byte("x\n"), maxDeadLetterInspectionRecords+1)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := InspectDeadLetters("all"); err == nil || got != nil {
		t.Fatal("record limit returned success or partial data")
	}
	// Two individually bounded files must still respect a shared byte budget.
	half := bytes.Repeat([]byte(" "), maxDeadLetterInspectionBytes/2+1)
	if err := os.WriteFile(path, half, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(InboxPathFor(UnownedInboxID), half, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := InspectDeadLetters("all"); err == nil || got != nil {
		t.Fatal("aggregate byte budget not enforced")
	}
	if _, err := InspectDeadLetters("bad\x1b[2Jstore"); err == nil || strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatal("error emitted raw terminal escape")
	}
}

func TestDeadLetterInspectionErrorQuotesPathAndPreservesCause(t *testing.T) {
	cause := &os.PathError{Op: "open", Path: "ledger\x1b[2J\n.jsonl", Err: os.ErrPermission}
	err := &deadLetterInspectionError{cause: cause}
	if strings.ContainsAny(err.Error(), "\x1b\n") {
		t.Fatal("error contains raw terminal controls")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || !errors.Is(err, os.ErrPermission) {
		t.Fatal("wrapped storage cause lost")
	}
}
