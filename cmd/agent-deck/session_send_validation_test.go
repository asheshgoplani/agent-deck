package main

import (
	"strings"
	"testing"
)

type sendExit struct{ code int }

// runSessionSendExpectExit runs handleSessionSend and returns the stdout and
// the exit code it asked for (-1: it returned without exiting).
func runSessionSendExpectExit(t *testing.T, args []string) (string, int) {
	t.Helper()
	old := cliExitHook
	cliExitHook = func(code int) { panic(sendExit{code}) }
	t.Cleanup(func() { cliExitHook = old })
	code := -1
	out := captureStdout(t, func() {
		defer func() {
			if r := recover(); r != nil {
				e, ok := r.(sendExit)
				if !ok {
					panic(r)
				}
				code = e.code
			}
		}()
		handleSessionSend("", args)
	})
	return out, code
}

func TestSessionSend_RejectsEmptyMessage(t *testing.T) {
	cliInboxTestHome(t)
	for _, msg := range []string{"", "   "} {
		out, code := runSessionSendExpectExit(t, []string{"somebody", msg, "--json"})
		if code != 1 {
			t.Fatalf("empty message %q: exit %d, want 1; out: %s", msg, code, out)
		}
		if !strings.Contains(out, "empty") {
			t.Fatalf("error does not say the message is empty: %s", out)
		}
	}
}
