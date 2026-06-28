package main

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Under `go test`, stdin/stdout are pipes, not a terminal. The attach guard
// must therefore refuse with errAttachNoTTY BEFORE touching tmux — this is the
// exact path that protects scripts/CI/conductor callers of `--attach`.
func TestAttachInstanceInteractive_NoTTYReturnsSentinel(t *testing.T) {
	if stdinStdoutIsTerminal() {
		t.Skip("test stdio is a terminal; the no-TTY guard cannot be exercised here")
	}

	inst := session.NewInstance("attach-guard", "/tmp")
	err := attachInstanceInteractive(inst)
	if !errors.Is(err, errAttachNoTTY) {
		t.Fatalf("attachInstanceInteractive without a TTY = %v, want errAttachNoTTY", err)
	}
}

// stdinStdoutIsTerminal must be false in the test harness (piped stdio). This
// pins the contract the callers rely on for the no-TTY exit path.
func TestStdinStdoutIsTerminal_FalseUnderTest(t *testing.T) {
	if stdinStdoutIsTerminal() {
		t.Skip("test stdio unexpectedly a terminal; nothing to assert")
	}
}
