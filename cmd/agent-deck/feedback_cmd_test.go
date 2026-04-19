package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
)

// feedbackMocks wraps a *feedback.Sender plus counters so the test
// matrix for issue #679 can assert (a) whether GhCmd was called,
// (b) that clipboard/browser fallbacks are NEVER called from the CLI,
// and (c) the final error / stdout.
type feedbackMocks struct {
	sender      *feedback.Sender
	ghCalled    bool
	ghFailErr   error
	clipCalled  bool
	browserCall bool
	lastGhArgs  []string
}

func newFeedbackMocks() *feedbackMocks {
	m := &feedbackMocks{}
	m.sender = &feedback.Sender{
		GhCmd: func(args ...string) error {
			m.ghCalled = true
			m.lastGhArgs = append([]string(nil), args...)
			return m.ghFailErr
		},
		BrowserCmd: func(url string) error {
			m.browserCall = true
			return nil
		},
		ClipboardCmd: func(text string) error {
			m.clipCalled = true
			return nil
		},
		IsHeadlessFunc: func() bool { return true },
	}
	return m
}

// pipeInput writes the given lines (joined with \n) to an os.Pipe and
// returns the read end. The caller installs it as os.Stdin.
func pipeInput(t *testing.T, lines ...string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() failed: %v", err)
	}
	go func() {
		defer w.Close()
		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
	}()
	return r
}

// withStdin installs r as os.Stdin and returns a restore func.
func withStdin(t *testing.T, r *os.File) func() {
	t.Helper()
	orig := os.Stdin
	os.Stdin = r
	return func() { os.Stdin = orig }
}

// withFeedbackLogin overrides ghUserLogin for the test duration.
func withFeedbackLogin(t *testing.T, login string) func() {
	t.Helper()
	prev := ghUserLogin
	ghUserLogin = func() string { return login }
	return func() { ghUserLogin = prev }
}

// ──────────────────────────────────────────────────────────────────
// #679 test matrix (a) — rating + comment + 'n' on confirm prompt.
// GhCmd NOT called, state IS saved, stdout contains 'Not posted.'
// ──────────────────────────────────────────────────────────────────
func TestIssue679_ConfirmN_DoesNotPost(t *testing.T) {
	defer withStdin(t, pipeInput(t, "4", "great tool", "n"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder

	if err := handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}
	if m.ghCalled {
		t.Error("GhCmd must NOT be called when user answers 'n' on confirm")
	}
	if !strings.Contains(stdout.String(), "Not posted.") {
		t.Errorf("stdout should contain 'Not posted.'\n---\n%s", stdout.String())
	}

	// State was saved before the disclosure prompt — rating persisted.
	st, _ := feedback.LoadState()
	if st.LastRatedVersion != "1.7.35" {
		t.Errorf("LastRatedVersion should be saved as '1.7.35' even on decline, got %q", st.LastRatedVersion)
	}
}

// ──────────────────────────────────────────────────────────────────
// (b) rating + comment + 'y' + gh success → GhCmd called with graphql
//
//	mutation args; stdout 'Posted to Discussion #600.'
//
// ──────────────────────────────────────────────────────────────────
func TestIssue679_ConfirmY_GhSuccess_Posts(t *testing.T) {
	defer withStdin(t, pipeInput(t, "5", "bug report", "y"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder

	if err := handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}
	if !m.ghCalled {
		t.Fatal("GhCmd must be called when user confirms with 'y'")
	}
	joined := strings.Join(m.lastGhArgs, " ")
	if !strings.Contains(joined, "graphql") {
		t.Errorf("gh args must include 'graphql': %v", m.lastGhArgs)
	}
	if !strings.Contains(joined, feedback.DiscussionNodeID) {
		t.Errorf("gh args must include DiscussionNodeID (%s): %v", feedback.DiscussionNodeID, m.lastGhArgs)
	}
	expectedBody := feedback.FormatComment("1.7.35", 5, runtime.GOOS, runtime.GOARCH, "bug report")
	if !strings.Contains(joined, expectedBody) {
		t.Errorf("gh args must include FormatComment body verbatim\nwant substring: %q\ngot: %q", expectedBody, joined)
	}
	if m.clipCalled {
		t.Error("ClipboardCmd must NOT be called from the CLI flow")
	}
	if m.browserCall {
		t.Error("BrowserCmd must NOT be called from the CLI flow")
	}
	if !strings.Contains(stdout.String(), "Posted to Discussion #600.") {
		t.Errorf("stdout should contain 'Posted to Discussion #600.'\n---\n%s", stdout.String())
	}
}

// ──────────────────────────────────────────────────────────────────
// (c) 'y' + gh failure → GhCmd called, clipboard/browser NOT called,
//
//	error surfaced (non-zero exit path), stdout has recovery hint.
//
// ──────────────────────────────────────────────────────────────────
func TestIssue679_ConfirmY_GhFailure_NoFallback(t *testing.T) {
	defer withStdin(t, pipeInput(t, "3", "meh", "y"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	m.ghFailErr = errors.New("gh: authentication required")
	var stdout strings.Builder

	err := handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout)
	if err == nil {
		t.Fatal("handleFeedbackWithSender must return an error when gh fails (non-zero exit)")
	}
	if !m.ghCalled {
		t.Error("GhCmd must still be called before failing")
	}
	if m.clipCalled {
		t.Error("ClipboardCmd must NOT be called on gh failure — no silent fallback")
	}
	if m.browserCall {
		t.Error("BrowserCmd must NOT be called on gh failure — no silent fallback")
	}
	out := stdout.String()
	if !strings.Contains(out, "could not post via gh") {
		t.Errorf("stdout should state gh post failure\n---\n%s", out)
	}
	if !strings.Contains(out, "gh auth status") {
		t.Errorf("stdout should mention 'gh auth status' recovery hint\n---\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────
// (d) empty line on confirm → default-N → GhCmd NOT called.
// ──────────────────────────────────────────────────────────────────
func TestIssue679_EmptyConfirm_DefaultNo(t *testing.T) {
	defer withStdin(t, pipeInput(t, "4", "", ""))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder

	if err := handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}
	if m.ghCalled {
		t.Error("GhCmd must NOT be called on empty confirm (default is N)")
	}
	if !strings.Contains(stdout.String(), "Not posted.") {
		t.Errorf("stdout should contain 'Not posted.'\n---\n%s", stdout.String())
	}
}

// ──────────────────────────────────────────────────────────────────
// (e) 'Y' uppercase → treated as yes.
// ──────────────────────────────────────────────────────────────────
func TestIssue679_Confirm_UppercaseY(t *testing.T) {
	defer withStdin(t, pipeInput(t, "4", "comment", "Y"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder

	if err := handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}
	if !m.ghCalled {
		t.Error("uppercase 'Y' must be accepted as yes")
	}
}

// ──────────────────────────────────────────────────────────────────
// (f) ' y ' with whitespace → accepted after trim.
// ──────────────────────────────────────────────────────────────────
func TestIssue679_Confirm_WhitespaceY(t *testing.T) {
	defer withStdin(t, pipeInput(t, "4", "comment", " y "))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder

	if err := handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}
	if !m.ghCalled {
		t.Error("' y ' with whitespace must be accepted after trim")
	}
}

// ──────────────────────────────────────────────────────────────────
// Disclosure preview must show the exact body that will be posted —
// this is the core trust guarantee of #679. We render a comment with
// a distinctive marker and assert every line appears in stdout.
// ──────────────────────────────────────────────────────────────────
func TestIssue679_Disclosure_PreviewMatchesFormatComment(t *testing.T) {
	comment := "ZEBRA-MARKER-ROSETTA-PINE"
	defer withStdin(t, pipeInput(t, "2", comment, "n"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder

	if err := handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}
	body := feedback.FormatComment("1.7.35", 2, runtime.GOOS, runtime.GOARCH, comment)
	out := stdout.String()
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(out, line) {
			t.Errorf("preview must contain FormatComment line verbatim\nmissing line: %q\n---\n%s", line, out)
		}
	}
}

// ──────────────────────────────────────────────────────────────────
// Disclosure must show the fetched login as '@<login>', mention PUBLIC,
// the Discussion URL, and the gh CLI.
// ──────────────────────────────────────────────────────────────────
func TestIssue679_Disclosure_ShowsLogin(t *testing.T) {
	defer withStdin(t, pipeInput(t, "4", "x", "n"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder
	_ = handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout)

	out := stdout.String()
	if !strings.Contains(out, "@octocat") {
		t.Errorf("disclosure should include '@octocat'\n---\n%s", out)
	}
	if !strings.Contains(out, "PUBLIC") {
		t.Errorf("disclosure should call out that the post is PUBLIC\n---\n%s", out)
	}
	if !strings.Contains(out, "discussions/600") {
		t.Errorf("disclosure should link to discussions/600\n---\n%s", out)
	}
	if !strings.Contains(out, "gh") {
		t.Errorf("disclosure should mention the gh CLI\n---\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────
// Login lookup fallback: empty login → 'your GitHub account', no '@' prefix.
// ──────────────────────────────────────────────────────────────────
func TestIssue679_Disclosure_LoginFallback(t *testing.T) {
	defer withStdin(t, pipeInput(t, "4", "x", "n"))()
	defer withFeedbackLogin(t, "")()

	m := newFeedbackMocks()
	var stdout strings.Builder
	_ = handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout)

	out := stdout.String()
	if !strings.Contains(out, "your GitHub account") {
		t.Errorf("disclosure should fall back to 'your GitHub account' when login lookup fails\n---\n%s", out)
	}
	if strings.Contains(out, "As:     @") {
		t.Errorf("disclosure should omit the @ prefix when login lookup fails\n---\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────
// Legacy opt-out behavior ('n' at rating prompt) stays intact.
// ──────────────────────────────────────────────────────────────────
func TestIssue679_OptOut_Unchanged(t *testing.T) {
	defer withStdin(t, pipeInput(t, "n"))()
	defer withFeedbackLogin(t, "octocat")()

	m := newFeedbackMocks()
	var stdout strings.Builder
	if err := handleFeedbackWithSender([]string{}, "1.7.35", m.sender, &stdout); err != nil {
		t.Fatalf("handleFeedbackWithSender returned error: %v", err)
	}
	if m.ghCalled {
		t.Error("GhCmd must NOT be called on opt-out")
	}
	st, _ := feedback.LoadState()
	if st.FeedbackEnabled {
		t.Error("opt-out must set FeedbackEnabled=false")
	}

	// Restore state so later tests don't inherit the opt-out.
	st.FeedbackEnabled = true
	_ = feedback.SaveState(st)
}
