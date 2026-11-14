package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// TestSetupOutputForLog: setup-script output reaches the log as its last
// setupLogMaxLines lines and at most setupLogMaxBytes bytes, with a note of
// how much was cut; short output is logged as is.
func TestSetupOutputForLog(t *testing.T) {
	if got := setupOutputForLog("one\ntwo\n"); got != "one\ntwo\n" {
		t.Fatalf("short output = %q, want it unchanged", got)
	}
	if got := setupOutputForLog(""); got != "" {
		t.Fatalf("empty output = %q", got)
	}

	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "line %03d\n", i)
	}
	got := setupOutputForLog(b.String())
	if strings.Contains(got, "line 159\n") || !strings.Contains(got, "line 160\n") || !strings.HasSuffix(got, "line 199\n") {
		t.Fatalf("200 lines logged as:\n%s\nwant only the last %d", got, setupLogMaxLines)
	}
	if !strings.Contains(got, "160 lines") {
		t.Fatalf("no note of the 160 lines cut:\n%s", got)
	}

	// One huge line (no newlines): the byte cap still holds, on a rune
	// boundary.
	huge := strings.Repeat("é", 1<<19) + "END"
	got = setupOutputForLog(huge)
	if len(got) > setupLogMaxBytes+200 || !strings.HasSuffix(got, "END") || !utf8.ValidString(got) {
		t.Fatalf("1 MiB line logged as %d bytes (valid utf8 %v), want about %d ending in END", len(got), utf8.ValidString(got), setupLogMaxBytes)
	}
	if !strings.Contains(got, "bytes") {
		t.Fatalf("no note of the bytes cut: %q", got[:120])
	}
}

// TestForkWithStateWorktree_SetupFailureLogsOnlyTheOutputTail: a failing
// setup script that printed a lot is logged at Warn with its tail only, not
// the whole output.
func TestForkWithStateWorktree_SetupFailureLogsOnlyTheOutputTail(t *testing.T) {
	var logBuf bytes.Buffer
	prev := uiLog
	uiLog = slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { uiLog = prev })

	deps := forkWithStateSetupDeps()
	deps.runSetup = func(_, _ string, stdout, _ io.Writer, _ time.Duration) error {
		fmt.Fprintln(stdout, "FIRST-LINE-SECRET-ISH")
		for i := 0; i < 5000; i++ {
			fmt.Fprintf(stdout, "npm noise %05d %s\n", i, strings.Repeat("x", 60))
		}
		fmt.Fprintln(stdout, "LAST-LINE error: boom")
		return errors.New("setup boom")
	}
	if _, err := forkWithStateWorktree("parent", "repo", "fork-path", "fork/state", git.WorktreeStateOptions{WithState: true}, deps); err != nil {
		t.Fatalf("fatal err = %v", err)
	}

	var rec map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logBuf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "fork_with_state_setup_failed" {
			rec = m
		}
	}
	if rec == nil {
		t.Fatalf("no fork_with_state_setup_failed record in:\n%.2000s", logBuf.String())
	}
	out, _ := rec["output"].(string)
	if len(out) > setupLogMaxBytes+200 {
		t.Fatalf("logged %d bytes of setup output, want at most about %d", len(out), setupLogMaxBytes)
	}
	if strings.Contains(out, "FIRST-LINE-SECRET-ISH") || !strings.Contains(out, "LAST-LINE error: boom") {
		t.Fatalf("logged output does not keep just the tail:\n%s", out)
	}
}
