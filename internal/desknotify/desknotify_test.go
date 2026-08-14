package desknotify

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeBackend records what it was asked to deliver.
type fakeBackend struct {
	name        string
	available   bool
	err         error
	calls       int
	gotTitle    string
	gotBody     string
	ctxDeadline bool
}

func (f *fakeBackend) Name() string    { return f.name }
func (f *fakeBackend) Available() bool { return f.available }
func (f *fakeBackend) Notify(ctx context.Context, title, body string) error {
	f.calls++
	f.gotTitle, f.gotBody = title, body
	_, f.ctxDeadline = ctx.Deadline()
	return f.err
}

func TestNotify_UsesFirstAvailableBackend(t *testing.T) {
	unavailable := &fakeBackend{name: "cmux", available: false}
	available := &fakeBackend{name: "osascript", available: true}

	got := NewWithBackends(unavailable, available).Notify(Notification{
		SessionTitle: "flow", ToStatus: "waiting",
	})

	if got != "osascript" {
		t.Errorf("backend = %q, want %q", got, "osascript")
	}
	if unavailable.calls != 0 {
		t.Errorf("unavailable backend was called %d times, want 0", unavailable.calls)
	}
	if available.calls != 1 {
		t.Errorf("available backend called %d times, want 1", available.calls)
	}
}

// A backend that reports available but fails must not swallow the alert: cmux
// installed while the app is closed is exactly this case, and the OS notifier
// should still fire.
func TestNotify_FallsThroughOnBackendError(t *testing.T) {
	failing := &fakeBackend{name: "cmux", available: true, err: errors.New("socket closed")}
	fallback := &fakeBackend{name: "osascript", available: true}

	got := NewWithBackends(failing, fallback).Notify(Notification{
		SessionTitle: "flow", ToStatus: "waiting",
	})

	if got != "osascript" {
		t.Errorf("backend = %q, want fallback %q after the first errored", got, "osascript")
	}
	if fallback.calls != 1 {
		t.Errorf("fallback called %d times, want 1", fallback.calls)
	}
}

func TestNotify_NoBackendAvailableIsSilent(t *testing.T) {
	got := NewWithBackends(
		&fakeBackend{name: "cmux", available: false},
		&fakeBackend{name: "osascript", available: false},
	).Notify(Notification{SessionTitle: "flow", ToStatus: "waiting"})

	if got != "" {
		t.Errorf("backend = %q, want empty when nothing is available", got)
	}
}

// The status poll calls into Notify, so a wedged notifier must be bounded.
func TestNotify_PassesBoundedContext(t *testing.T) {
	b := &fakeBackend{name: "cmux", available: true}
	NewWithBackends(b).Notify(Notification{SessionTitle: "flow", ToStatus: "waiting"})
	if !b.ctxDeadline {
		t.Error("backend received a context with no deadline; a hung notifier would stall the status poll")
	}
}

func TestNotify_NilNotifierIsSafe(t *testing.T) {
	var nt *Notifier
	if got := nt.Notify(Notification{SessionTitle: "flow"}); got != "" {
		t.Errorf("nil Notifier returned %q, want empty", got)
	}
}

func TestBody_LeadsWithTheRequiredAction(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"waiting", "needs input"},
		{"error", "hit an error"},
		{"idle", "finished"},
	}
	for _, tt := range tests {
		got := Notification{SessionTitle: "flow", ToStatus: tt.status}.Body()
		if got != tt.want {
			t.Errorf("Body() for %q = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// A non-default profile must appear: the same title can exist in two profiles,
// so without it the operator cannot tell which session is asking.
func TestBody_NamesNonDefaultProfile(t *testing.T) {
	got := Notification{SessionTitle: "flow", Profile: "work", ToStatus: "waiting"}.Body()
	if !strings.Contains(got, "work") {
		t.Errorf("Body() = %q, want it to name profile %q", got, "work")
	}

	// "default" is noise: every session is in it unless stated otherwise.
	got = Notification{SessionTitle: "flow", Profile: "default", ToStatus: "waiting"}.Body()
	if strings.Contains(got, "default") {
		t.Errorf("Body() = %q, must not mention the default profile", got)
	}
}

func TestTitle_FallsBackWhenSessionTitleIsEmpty(t *testing.T) {
	if got := (Notification{ToStatus: "waiting"}).Title(); got != "agent-deck" {
		t.Errorf("Title() = %q, want %q for an untitled session", got, "agent-deck")
	}
}

// ShouldNotify is deliberately narrower than the parent-routing status set:
// idle is a resting state for a long-lived agent, and notifying on it would
// train the operator to ignore the banners.
func TestShouldNotify_ExcludesIdle(t *testing.T) {
	for _, s := range []string{"waiting", "error", "WAITING", "  error  "} {
		if !ShouldNotify(s) {
			t.Errorf("ShouldNotify(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"idle", "running", "starting", "", "stopped"} {
		if ShouldNotify(s) {
			t.Errorf("ShouldNotify(%q) = true, want false", s)
		}
	}
}

// A session title is user-controlled and reaches osascript as SCRIPT SOURCE,
// not argv, so a quote in a title would otherwise close the string literal and
// let the rest execute as AppleScript.
func TestEscapeAppleScript_NeutralizesInjection(t *testing.T) {
	const payload = `foo" & (do shell script "id") & "`
	got := escapeAppleScript(payload)

	// Every quote must be preceded by a backslash. Scanning for a bare `"` is
	// the actual invariant: a substring check like `" &` would also match the
	// tail of a correctly-escaped `\"`, and so would pass on broken output.
	for i, r := range got {
		if r != '"' {
			continue
		}
		if i == 0 || got[i-1] != '\\' {
			t.Fatalf("escaped = %q has a bare quote at %d; it would close the "+
				"AppleScript literal and run the rest as code", got, i)
		}
	}
	if !strings.Contains(got, `\"`) {
		t.Errorf("escaped = %q, want embedded quotes backslash-escaped", got)
	}
}

// Backslashes must be escaped BEFORE quotes. Reversed, the backslash pass
// would re-escape the backslashes the quote pass just added, turning \" back
// into \\" and re-opening the literal.
func TestEscapeAppleScript_EscapesBackslashBeforeQuote(t *testing.T) {
	if got, want := escapeAppleScript(`a\"b`), `a\\\"b`; got != want {
		t.Errorf("escapeAppleScript(%q) = %q, want %q", `a\"b`, got, want)
	}
}

// A session titled exactly "-h" makes the cmux CLI print usage and deliver
// nothing, so the alert vanishes silently. Verified against the real binary:
// `cmux notify --title -h` prints usage, while other flag-shaped titles such
// as "--surface" are safely bound as the value of --title.
func TestDeflagged_NeutralizesHelpFlags(t *testing.T) {
	for _, s := range []string{"-h", "--help", "  -h  "} {
		got := deflagged(s)
		if got == s {
			t.Errorf("deflagged(%q) returned it unchanged; the notifier would print "+
				"usage and drop the alert", s)
		}
		if !strings.HasPrefix(got, s) {
			t.Errorf("deflagged(%q) = %q, want the original text preserved as a prefix "+
				"so the banner still reads correctly", s, got)
		}
	}
}

// Flag-shaped values that are NOT help flags must pass through untouched: the
// argv binding already handles them, so altering them would corrupt a title
// for no benefit.
func TestDeflagged_LeavesOtherValuesAlone(t *testing.T) {
	for _, s := range []string{"--surface", "-x", "flow", "", "needs input"} {
		if got := deflagged(s); got != s {
			t.Errorf("deflagged(%q) = %q, want it unchanged", s, got)
		}
	}
}
