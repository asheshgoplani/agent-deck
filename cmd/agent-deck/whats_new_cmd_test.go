package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/reportloop"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// setupWhatsNewTest is setupLoopTest plus a pinned changelog.
func setupWhatsNewTest(t *testing.T) *loopEnv {
	t.Helper()
	e := setupLoopTest(t)
	prev := whatsNewFetchChangelog
	whatsNewFetchChangelog = func(context.Context) (string, error) { return loopChangelog, nil }
	t.Cleanup(func() { whatsNewFetchChangelog = prev })
	return e
}

// ── whats-new ─────────────────────────────────────────────────────────

func runWN(t *testing.T, version string, args ...string) (string, int) {
	t.Helper()
	var out strings.Builder
	code := runWhatsNew(context.Background(), args, version, &out)
	return out.String(), code
}

func TestWhatsNew_FirstRunMarksSeenSilently(t *testing.T) {
	le := setupWhatsNewTest(t)
	out, code := runWN(t, "1.16.38")
	if code != 0 || strings.TrimSpace(out) != "Nothing new since v1.16.38." {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := le.state(t).LastSeenVersion; got != "1.16.38" {
		t.Fatalf("first run must mark the current version seen, got %q", got)
	}
}

func TestWhatsNew_ShowsYourFixedReportsFirst(t *testing.T) {
	le := setupWhatsNewTest(t)
	if err := le.store.MarkSeen("1.16.36"); err != nil {
		t.Fatal(err)
	}
	if err := le.store.Record(reportloop.Report{Number: 9002, Title: "[bug] fork is slow", FiledVersion: "1.16.36"}); err != nil {
		t.Fatal(err)
	}
	out, code := runWN(t, "1.16.38")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != "What's new in v1.16.38" || !strings.HasPrefix(lines[1], "Your report #9002: fixed in v1.16.38") {
		t.Fatalf("title then your report must come first:\n%s", out)
	}
	for _, want := range []string{"Forks finish in seconds again.", "Queued messages are delivered after a restart.", "agent-deck feedback fixed 9002"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if le.state(t).LastSeenVersion != "1.16.36" {
		t.Fatal("without --mark-seen the version stays unseen")
	}

	out, code = runWN(t, "1.16.38", "--json", "--mark-seen")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := decodeReportJSON(t, out)
	fixed := m["fixed"].([]any)
	if m["ok"] != true || m["empty"] != false || m["to"] != "1.16.38" || m["from"] != "1.16.36" || len(fixed) != 1 ||
		!strings.HasPrefix(m["text"].(string), "What's new in v1.16.38\nYour report #9002") || len(m["versions"].([]any)) != 2 {
		t.Fatalf("unexpected JSON: %v", m)
	}
	if le.state(t).LastSeenVersion != "1.16.38" {
		t.Fatal("--mark-seen must store the current version")
	}
	out, _ = runWN(t, "1.16.38")
	if strings.TrimSpace(out) != "Nothing new since v1.16.38." {
		t.Fatalf("after mark-seen nothing is new:\n%s", out)
	}
}

func TestWhatsNew_SinceAndEmptyJSON(t *testing.T) {
	le := setupWhatsNewTest(t)
	if err := le.store.MarkSeen("1.16.38"); err != nil {
		t.Fatal(err)
	}
	out, code := runWN(t, "1.16.38", "--json")
	m := decodeReportJSON(t, out)
	if code != 0 || m["ok"] != true || m["empty"] != true || m["text"] != "Nothing new since v1.16.38.\n" {
		t.Fatalf("exit %d: %v", code, m)
	}
	out, code = runWN(t, "1.16.38", "--since", "v1.16.37")
	if code != 0 || !strings.Contains(out, "Forks finish in seconds again.") || strings.Contains(out, "Queued messages") {
		t.Fatalf("--since 1.16.37 shows only 1.16.38, exit %d\n%s", code, out)
	}
}

func TestWhatsNew_ChangelogErrorIsReported(t *testing.T) {
	le := setupWhatsNewTest(t)
	if err := le.store.MarkSeen("1.16.36"); err != nil {
		t.Fatal(err)
	}
	whatsNewFetchChangelog = func(context.Context) (string, error) { return "", errors.New("fetching changelog: status 404") }
	out, code := runWN(t, "1.16.38", "--json")
	m := decodeReportJSON(t, out)
	if code == 0 || m["ok"] == true || m["error_code"] != "changelog_failed" {
		t.Fatalf("exit %d: %v", code, m)
	}
	if le.state(t).LastSeenVersion != "1.16.36" {
		t.Fatal("a failed fetch must not mark anything seen")
	}
}

func TestWhatsNew_Help(t *testing.T) {
	setupWhatsNewTest(t)
	out, code := runWN(t, "1.16.38", "--help")
	if code != 0 || !strings.Contains(out, "Usage: agent-deck whats-new") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// ── update prints what's new ──────────────────────────────────────────

func TestUpdatePrintsWhatsNew_AfterManualInstallAndMarksSeen(t *testing.T) {
	le := setupWhatsNewTest(t)
	if err := le.store.Record(reportloop.Report{Number: 9001, Title: "[bug] queued message stuck", FiledVersion: "1.16.36"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printWhatsNewAfterUpdate(context.Background(), &buf, "1.16.36", "1.16.38", true)
	out := buf.String()
	for _, want := range []string{"What's new in v1.16.38", "Your report #9001: fixed in v1.16.37", "Forks finish in seconds again."} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if le.state(t).LastSeenVersion != "1.16.38" {
		t.Fatalf("manual update must mark the new version seen, got %q", le.state(t).LastSeenVersion)
	}
}

func TestUpdatePrintsWhatsNew_FetchFailureIsOneLine(t *testing.T) {
	setupWhatsNewTest(t)
	whatsNewFetchChangelog = func(context.Context) (string, error) { return "", errors.New("fetching changelog: offline") }
	var buf bytes.Buffer
	printWhatsNewAfterUpdate(context.Background(), &buf, "1.16.36", "1.16.38", true)
	if out := strings.TrimSpace(buf.String()); strings.Count(out, "\n") != 0 || !strings.Contains(out, "What's new") || !strings.Contains(out, "offline") {
		t.Fatalf("want one note line, got:\n%s", buf.String())
	}
}

func TestUpdatePrintsWhatsNew_UnattendedAfterSuccessOnly(t *testing.T) {
	le := setupWhatsNewTest(t)
	if err := le.store.MarkSeen("1.16.36"); err != nil {
		t.Fatal(err)
	}
	info := &update.UpdateInfo{CurrentVersion: "1.16.36", LatestVersion: "1.16.38", Available: true}
	h := newUnattendedHarness(t, info)
	h.deps.version = "1.16.36"
	var got []string
	h.deps.whatsNew = func(from, to string) {
		got = append(got, from+"->"+to)
		printWhatsNewAfterUpdate(context.Background(), h.deps.out, from, to, false)
	}
	if code := runUnattendedUpdate(h.deps); code != exitUpdateOK {
		t.Fatalf("exit %d\n%s", code, h.out.String())
	}
	if len(got) != 1 || got[0] != "1.16.36->1.16.38" {
		t.Fatalf("whatsNew hook calls: %v", got)
	}
	out := h.out.String()
	if i, j := strings.Index(out, "✓ Updated to v1.16.38"), strings.Index(out, "What's new in v1.16.38"); i < 0 || j < i {
		t.Fatalf("what's new must follow the success line:\n%s", out)
	}
	// The TUI shows the dialog on its next start, so an unattended run
	// leaves the version unseen.
	if le.state(t).LastSeenVersion != "1.16.36" {
		t.Fatalf("unattended install must leave the version for the TUI, got %q", le.state(t).LastSeenVersion)
	}

	// A failed install prints no what's new.
	h = newUnattendedHarness(t, info)
	h.deps.install = func(string) error { return errors.New("boom") }
	got = nil
	h.deps.whatsNew = func(from, to string) { got = append(got, from+"->"+to) }
	if code := runUnattendedUpdate(h.deps); code == exitUpdateOK || len(got) != 0 {
		t.Fatalf("failed install: exit %d, whatsNew calls %v", code, got)
	}
}

// An unattended install runs from the update timer: it must not reach the
// network for the changelog (release gate TestReleaseReadiness_UpdatePath).
// It keeps (from, to] pending for the next start and says so in one line.
func TestUpdatePrintsWhatsNew_UnattendedNeverFetches(t *testing.T) {
	le := setupWhatsNewTest(t)
	prev := whatsNewFetchChangelog
	whatsNewFetchChangelog = func(context.Context) (string, error) {
		t.Fatalf("an unattended install must not fetch the changelog")
		return "", nil
	}
	t.Cleanup(func() { whatsNewFetchChangelog = prev })
	var buf bytes.Buffer
	printWhatsNewAfterUpdate(context.Background(), &buf, "1.16.36", "1.16.38", false)
	if got := le.state(t).LastSeenVersion; got != "1.16.36" {
		t.Fatalf("the range must stay pending, last seen %q", got)
	}
	if !strings.Contains(buf.String(), "What's new in v1.16.38") || !strings.Contains(buf.String(), "agent-deck whats-new") {
		t.Fatalf("want a one-line pointer to What's new:\n%s", buf.String())
	}
}
