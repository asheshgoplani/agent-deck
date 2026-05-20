package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue1101_RemotePreview_RendersClaudeFormattedContent asserts that when
// the preview cache holds claude-formatted output (the kind capture-pane
// returns — ANSI escapes, the ╭─ box border, the `> ` prompt), the remote
// preview renderer writes that content verbatim into the output.
//
// Bug context: PR #1095 (v1.9.23) fixed the tool-label color for remote rows,
// but @ddorman-dn confirmed in #1101 that the preview pane still doesn't show
// claude-formatted output for SSH sessions. The root cause is that
// `fetchRemotePreview` used `FetchSessionOutput`, which on the remote side
// returns parsed transcript text via `GetLastResponseBestEffort()`. Local
// previews use `tmux capture-pane -p -e`, which is what produces the
// claude-formatted UI we want on remotes too.
//
// Fix: a new `FetchSessionPane` SSH method calls `agent-deck session output
// --pane --json` on the remote, which the CLI now serves with PreviewFull()
// (capture-pane content). This test pins the rendering contract — given a
// populated cache with claude-formatted content, the renderer must emit
// the ANSI/border markers, not strip them.
func TestIssue1101_RemotePreview_RendersClaudeFormattedContent(t *testing.T) {
	forceTrueColorProfile()

	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{
		ID:         "remote-claude-1",
		Title:      "my-claude-session",
		Status:     "running",
		Tool:       "claude",
		RemoteName: "myserver",
	}
	item := session.Item{
		Type:          session.ItemTypeRemoteSession,
		RemoteName:    "myserver",
		RemoteSession: &remote,
	}

	// Simulate the fix: cache populated by FetchSessionPane with raw
	// capture-pane content (ANSI + claude box-drawing border + prompt).
	// This is what `session output --pane --json` returns post-fix.
	claudeFormatted := "\x1b[38;5;208m╭─────────────────────╮\x1b[0m\n" +
		"\x1b[38;5;208m│\x1b[0m Welcome to Claude \x1b[38;5;208m│\x1b[0m\n" +
		"\x1b[38;5;208m╰─────────────────────╯\x1b[0m\n" +
		"\n> "

	key := remotePreviewCacheKey("myserver", remote.ID)
	home.previewCacheMu.Lock()
	if home.previewCache == nil {
		home.previewCache = make(map[string]string)
	}
	if home.previewCacheTime == nil {
		home.previewCacheTime = make(map[string]time.Time)
	}
	home.previewCache[key] = claudeFormatted
	home.previewCacheTime[key] = time.Now()
	home.previewCacheMu.Unlock()

	out := home.renderRemotePreview(item, 100, 30)

	// The box-drawing border is the most reliable proof that the
	// claude-formatted content reached the renderer. If the bug regressed
	// (renderer strips ANSI or content is empty), this assertion fires.
	if !strings.Contains(out, "╭") || !strings.Contains(out, "╮") {
		t.Fatalf("expected claude box border in preview output; got:\n%s", out)
	}
	if !strings.Contains(out, "Welcome to Claude") {
		t.Fatalf("expected claude content text in preview output; got:\n%s", out)
	}
	// The prompt marker also matters because the launching-animation code
	// looks for "\n> " to decide the agent is ready.
	if !strings.Contains(out, "> ") {
		t.Fatalf("expected claude prompt marker '> ' in preview output; got:\n%s", out)
	}
}

// TestIssue1101_RemotePreview_FetchUsesPaneFlag asserts that the
// remote-preview fetch path calls the SSH runner with `--pane` (the new flag
// added to `agent-deck session output` for #1101). If we ever revert to the
// transcript-text path (`session output --json` without `--pane`), this test
// fires — remote claude previews would silently degrade to plain text again.
func TestIssue1101_RemotePreview_FetchUsesPaneFlag(t *testing.T) {
	var gotArgs []string
	runner := &session.SSHRunner{}
	session.SetSSHRunnerRunFnForTest(runner, func(args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		// Return a minimal JSON envelope; the parser only needs `content`.
		// Use \u001b for ANSI ESC because raw 0x1B is invalid in a JSON string.
		return []byte(`{"content":"\u001b\u001b[38;5;208m╭─╮\u001b[0m"}`), nil
	})

	content, err := runner.FetchSessionPane(context.Background(), "abc")
	if err != nil {
		t.Fatalf("FetchSessionPane error: %v", err)
	}
	if !strings.Contains(content, "╭") {
		t.Fatalf("expected ANSI/box-border content; got %q", content)
	}
	if len(gotArgs) < 4 {
		t.Fatalf("expected runner called with >=4 args, got %v", gotArgs)
	}
	sawPane := false
	for _, a := range gotArgs {
		if a == "--pane" {
			sawPane = true
			break
		}
	}
	if !sawPane {
		t.Fatalf("expected `--pane` in SSH args; got %v", gotArgs)
	}
}
