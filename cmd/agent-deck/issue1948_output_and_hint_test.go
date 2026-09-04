package main

// Issue #1948, review round 2 — findings 1 and 3.
//
//	1. the version diagnosis belongs where the version is known (the caller,
//	   which can probe `--version`), not in a guess at the shape of stdout;
//	3. `printInboxEventLines` changed user-visible output for EVERY `inbox drain`
//	   and `inbox <id>` caller with nothing pinning either spelling.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// --- finding 1 ------------------------------------------------------------

func TestIssue1948R2_StaleRemoteBinaryHint(t *testing.T) {
	cases := []struct {
		name       string
		version    string
		found      bool
		wantSubstr string
	}{
		{"no binary answered", "", false, "No agent-deck answered"},
		{"older remote", "1.0.0", true, "older than this build"},
		{"current remote stays silent", Version, true, ""},
		{"newer remote stays silent", "999.0.0", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := staleRemoteBinaryHint("boxb", tc.version, tc.found)
			if tc.wantSubstr == "" {
				if got != "" {
					t.Fatalf("a healthy remote must not be blamed for the failure, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Fatalf("want a hint containing %q, got %q", tc.wantSubstr, got)
			}
			if !strings.Contains(got, "remote update boxb") {
				t.Fatalf("the hint must name the fix for THIS remote, got %q", got)
			}
		})
	}
}

// --- finding 3 ------------------------------------------------------------

// Both spellings of an inbox line are pinned: a transition renders its flip, a
// completion renders its outcome and never a bare arrow (which read like a lost
// status). This covers `inbox drain` and `inbox <id>` alike — they share the
// renderer.
func TestIssue1948R2_InboxLineSpellings(t *testing.T) {
	drainTestHome(t)
	parent := "conductor-line-format"

	if err := session.WriteInboxEvent(parent, session.TransitionNotificationEvent{
		ChildSessionID: "child-transition", ChildTitle: "migrate", Profile: "default",
		FromStatus: "running", ToStatus: "waiting", LastOutputHash: "h1",
		Timestamp: time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
	if err := session.WriteInboxEvent(parent, session.TransitionNotificationEvent{
		ChildSessionID: "child-finished", ChildTitle: "ship", Profile: "default",
		Kind: "finished", DoneStatus: "ok", DoneSummary: "shipped",
		Timestamp: time.Date(2026, 8, 16, 10, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed completion: %v", err)
	}

	var buf bytes.Buffer
	if err := runInbox(&buf, []string{parent}); err != nil {
		t.Fatalf("inbox: %v", err)
	}
	out := buf.String()

	wantTransition := `2026-08-16T10:00:00Z  child=child-transition title="migrate" profile=default running→waiting`
	if !strings.Contains(out, wantTransition) {
		t.Fatalf("transition line changed spelling:\nwant substring: %s\ngot:\n%s", wantTransition, out)
	}
	wantCompletion := `2026-08-16T10:01:00Z  child=child-finished title="ship" profile=default finished=ok`
	if !strings.Contains(out, wantCompletion) {
		t.Fatalf("completion line changed spelling:\nwant substring: %s\ngot:\n%s", wantCompletion, out)
	}
	if strings.Contains(out, "profile=default →") {
		t.Fatalf("a completion must not render a bare status arrow:\n%s", out)
	}
}

// A pulled record names the remote it came from, so a conductor draining two
// hosts can tell them apart on sight.
func TestIssue1948R2_InboxLineNamesSourceRemote(t *testing.T) {
	drainTestHome(t)
	parent := "conductor-line-remote"

	if err := session.WriteInboxEvent(parent, session.TransitionNotificationEvent{
		ChildSessionID: "boxb:child-remote", ChildTitle: "migrate", Profile: "default",
		FromStatus: "running", ToStatus: "waiting", SourceRemote: "boxb",
		Timestamp: time.Date(2026, 8, 16, 10, 2, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var buf bytes.Buffer
	if err := runInbox(&buf, []string{parent}); err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if !strings.Contains(buf.String(), "remote=boxb") {
		t.Fatalf("a pulled record must name its host:\n%s", buf.String())
	}
}
