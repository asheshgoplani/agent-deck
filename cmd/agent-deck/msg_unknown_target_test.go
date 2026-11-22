package main

import (
	"os"
	"strings"
	"testing"
)

// `msg read --for <unknown>` printed "Error: Error: ..." and exited 1, while
// `inbox drain` exits 2 for the same unresolved target.
func TestMsgRead_UnknownTargetErrorIsSingleAndExitsTwo(t *testing.T) {
	cliInboxTestHome(t)
	old := cliExitHook
	cliExitHook = func(code int) { panic(sendExit{code}) }
	t.Cleanup(func() { cliExitHook = old })

	r, w, _ := os.Pipe()
	origErr := os.Stderr
	os.Stderr = w
	code := -1
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				e, ok := rec.(sendExit)
				if !ok {
					panic(rec)
				}
				code = e.code
			}
		}()
		handleMsg("", []string{"read", "--for", "nosuch-msg"})
	}()
	w.Close()
	os.Stderr = origErr
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	got := string(buf[:n])
	if strings.Contains(got, "Error: Error:") {
		t.Fatalf("doubled prefix: %q", got)
	}
	if !strings.HasPrefix(got, "Error: ") || !strings.Contains(got, "nosuch-msg") {
		t.Fatalf("unexpected message: %q", got)
	}
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
