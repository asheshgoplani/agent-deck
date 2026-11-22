package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// runFeedbackCapture runs handleFeedback and returns stdout, stderr and the
// exit code it asked for (-1: returned normally).
func runFeedbackCapture(t *testing.T, args []string) (string, string, int) {
	t.Helper()
	cliInboxTestHome(t)
	old := cliExitHook
	cliExitHook = func(code int) { panic(sendExit{code}) }
	t.Cleanup(func() { cliExitHook = old })
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = ow, ew
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
		handleFeedback(args)
	}()
	os.Stdout, os.Stderr = origOut, origErr
	ow.Close()
	ew.Close()
	o, _ := io.ReadAll(or)
	e, _ := io.ReadAll(er)
	return string(o), string(e), code
}

func TestFeedback_ErrorsGoToStderr(t *testing.T) {
	out, errOut, code := runFeedbackCapture(t, []string{"bug", "--from-agent", "--dry-run"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if strings.Contains(out, "Error") {
		t.Fatalf("error text on stdout: %q", out)
	}
	if !strings.Contains(errOut, "--from-agent needs --title and --what") {
		t.Fatalf("error missing from stderr: %q", errOut)
	}
}

func TestFeedback_UnknownSubcommandIsAnErrorNotThePrompt(t *testing.T) {
	out, errOut, code := runFeedbackCapture(t, []string{"bogus"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if strings.Contains(out, "Rating") {
		t.Fatalf("unknown subcommand started the rating prompt: %q", out)
	}
	if !strings.Contains(errOut, "unknown feedback subcommand") {
		t.Fatalf("stderr = %q", errOut)
	}
}
