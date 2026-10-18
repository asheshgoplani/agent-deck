package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// formatSetupWarning must produce a single bounded, sanitized footer line so a
// failed worktree setup script can't flood or corrupt the height-constrained,
// auto-dismissing footer. The full output stays in the log, not here.
func TestFormatSetupWarning(t *testing.T) {
	const prefix = "worktree setup script failed: "

	t.Run("plain error keeps its message", func(t *testing.T) {
		got := formatSetupWarning(errors.New("exit status 1"))
		if got != prefix+"exit status 1" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("ANSI escapes are stripped", func(t *testing.T) {
		got := formatSetupWarning(errors.New("\x1b[31mboom\x1b[0m"))
		if got != prefix+"boom" {
			t.Fatalf("got %q, want ANSI stripped", got)
		}
	})

	t.Run("newlines and runs of whitespace collapse to single spaces", func(t *testing.T) {
		got := formatSetupWarning(errors.New("line one\n\n  line two\t line three\n"))
		if got != prefix+"line one line two line three" {
			t.Fatalf("got %q, want collapsed whitespace", got)
		}
	})

	t.Run("long output is truncated with an ellipsis", func(t *testing.T) {
		got := formatSetupWarning(errors.New(strings.Repeat("x", setupWarningMaxLen+50)))
		body := strings.TrimPrefix(got, prefix)
		if !strings.HasSuffix(body, "…") {
			t.Fatalf("want ellipsis suffix, got %q", body)
		}
		// setupWarningMaxLen runes of content + the ellipsis rune.
		if runes := []rune(body); len(runes) != setupWarningMaxLen+1 {
			t.Fatalf("body rune length = %d, want %d", len(runes), setupWarningMaxLen+1)
		}
	})

	t.Run("multi-byte output truncates on rune boundaries, stays valid UTF-8", func(t *testing.T) {
		// Each 'é' is 2 bytes; byte-slicing would split one and corrupt the string.
		got := formatSetupWarning(errors.New(strings.Repeat("é", setupWarningMaxLen+50)))
		body := strings.TrimPrefix(got, prefix)
		if !utf8.ValidString(body) {
			t.Fatalf("truncated body is not valid UTF-8: %q", body)
		}
		if runes := []rune(body); len(runes) != setupWarningMaxLen+1 {
			t.Fatalf("body rune length = %d, want %d", len(runes), setupWarningMaxLen+1)
		}
	})
}

// forkWithStateSetupDeps stubs every dependency up to (and including) runSetup so
// a with-state fork reaches the setup step deterministically without touching a
// real git repo. runSetup is left for the caller to set.
func forkWithStateSetupDeps() forkWithStateWorktreeDeps {
	deps := defaultForkWithStateWorktreeDeps("")
	deps.statPath = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	deps.mkdirAll = func(string, os.FileMode) error { return nil }
	deps.validateDestination = func(string, string) error { return nil }
	deps.detectInProgressOperation = func(string) (string, error) { return "", nil }
	deps.hasSubmodules = func(string) bool { return false }
	deps.headCommit = func(string) (string, error) { return "abc123", nil }
	deps.createAtStartPoint = func(string, string, string, string) (bool, error) { return true, nil }
	deps.materialize = func(string, string, bool) error { return nil }
	deps.processInclude = func(string, string, io.Writer) error { return nil }
	return deps
}

// A failing setup script must NOT fail the with-state fork: the worktree and
// parent state are already created. The failure is returned as the non-fatal
// setupErr so the caller can surface it, with the fatal err staying nil.
func TestForkWithStateWorktree_SetupFailureIsNonFatal(t *testing.T) {
	deps := forkWithStateSetupDeps()
	deps.runSetup = func(string, string, io.Writer, io.Writer, time.Duration) error {
		return errors.New("setup boom")
	}

	setupErr, err := forkWithStateWorktree("parent", "repo", "fork-path", "fork/state", git.WorktreeStateOptions{WithState: true}, deps)
	if err != nil {
		t.Fatalf("fatal err = %v, want nil (setup failure is non-fatal)", err)
	}
	if setupErr == nil || !strings.Contains(setupErr.Error(), "setup boom") {
		t.Fatalf("setupErr = %v, want the setup-script failure", setupErr)
	}
}

// When the setup script succeeds (or is absent), neither error is set.
func TestForkWithStateWorktree_SetupSuccessNoWarning(t *testing.T) {
	deps := forkWithStateSetupDeps()
	deps.runSetup = func(string, string, io.Writer, io.Writer, time.Duration) error { return nil }

	setupErr, err := forkWithStateWorktree("parent", "repo", "fork-path", "fork/state", git.WorktreeStateOptions{WithState: true}, deps)
	if err != nil {
		t.Fatalf("fatal err = %v, want nil", err)
	}
	if setupErr != nil {
		t.Fatalf("setupErr = %v, want nil on setup success", setupErr)
	}
}

// A fatal step (here: worktree creation) must return via err, never as a setup
// warning — the two channels must not be conflated.
func TestForkWithStateWorktree_FatalErrorIsNotASetupWarning(t *testing.T) {
	deps := forkWithStateSetupDeps()
	deps.createAtStartPoint = func(string, string, string, string) (bool, error) {
		return false, errors.New("create failed")
	}
	// runSetup must never run once creation fails.
	deps.runSetup = func(string, string, io.Writer, io.Writer, time.Duration) error {
		t.Fatal("runSetup ran after a fatal creation failure")
		return nil
	}

	setupErr, err := forkWithStateWorktree("parent", "repo", "fork-path", "fork/state", git.WorktreeStateOptions{WithState: true}, deps)
	if setupErr != nil {
		t.Fatalf("setupErr = %v, want nil on a fatal failure", setupErr)
	}
	if err == nil || !strings.Contains(err.Error(), "worktree creation failed") {
		t.Fatalf("err = %v, want fatal creation failure", err)
	}
}

// Structural guard: the create-session handler must skip auto-attach while a
// setup warning is pending, otherwise attaching hides the footer before it can
// be read. Asserted on source because the handler is not unit-testable in
// isolation (it drives tmux, storage, and preview fetches).
func TestCreateHandler_SkipsAutoAttachWhenSetupWarningPending(t *testing.T) {
	src := extractUIFuncBodySource(t, "home.go", "updateInner")
	if !strings.Contains(src, `h.attachOnCreate && msg.setupWarning == ""`) {
		t.Fatal("auto-attach must be guarded by msg.setupWarning == \"\" so the warning stays visible")
	}
}

// #2637: a failed setup script's warning must include the tail of its output,
// not just "exit status 1", while staying bounded and sanitized.
func TestFormatSetupWarning_IncludesOutputTail(t *testing.T) {
	const prefix = "worktree setup script failed: "
	exit1 := errors.New("exit status 1")

	t.Run("tail of output is shown after the error", func(t *testing.T) {
		out := "Running worktree setup script...\nstep 1\nERROR: npm install failed\nWorktree setup script failed after 2s: exit status 1\n"
		got := formatSetupWarning(withSetupOutputTail(exit1, out))
		want := prefix + "exit status 1 — step 1 | ERROR: npm install failed"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("ANSI and carriage-return progress lines are cleaned", func(t *testing.T) {
		out := "\x1b[32mfetching\x1b[0m 10%\rfetching 100%\r\n\n   \n\x1b[31mboom\x1b[0m\n"
		got := formatSetupWarning(withSetupOutputTail(exit1, out))
		want := prefix + "exit status 1 — fetching 10% | fetching 100% | boom"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("only the last lines are kept", func(t *testing.T) {
		out := "l1\nl2\nl3\nl4\nl5\nl6\nl7\n"
		got := formatSetupWarning(withSetupOutputTail(exit1, out))
		want := prefix + "exit status 1 — l3 | l4 | l5 | l6 | l7"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("long tail is cut from the front so the last line survives", func(t *testing.T) {
		out := strings.Repeat("y", setupWarningMaxLen*2) + "\nFINAL LINE\n"
		got := formatSetupWarning(withSetupOutputTail(exit1, out))
		if !strings.HasSuffix(got, "FINAL LINE") {
			t.Fatalf("last line lost: %q", got)
		}
		if !strings.Contains(got, "exit status 1 — …") {
			t.Fatalf("want error kept and tail front-truncated, got %q", got)
		}
		if body := []rune(strings.TrimPrefix(got, prefix)); len(body) > setupWarningMaxLen+1 {
			t.Fatalf("body rune length = %d, exceeds cap %d", len(body), setupWarningMaxLen)
		}
	})

	t.Run("not-approved skip keeps its own message and no tail", func(t *testing.T) {
		skipped := fmt.Errorf("%w: run agent-deck worktree approve", git.ErrWorktreeScriptNotApproved)
		got := formatSetupWarning(withSetupOutputTail(skipped, "some output\n"))
		if strings.HasPrefix(got, prefix) || strings.Contains(got, "some output") {
			t.Fatalf("got %q, want the plain not-approved message", got)
		}
		if !errors.Is(withSetupOutputTail(skipped, "x\n"), git.ErrWorktreeScriptNotApproved) {
			t.Fatal("wrapping must keep errors.Is working")
		}
	})

	t.Run("no output leaves the error unchanged", func(t *testing.T) {
		if got := withSetupOutputTail(exit1, "Running worktree setup script...\n\n"); got != exit1 {
			t.Fatalf("got %v, want the original error", got)
		}
	})
}
