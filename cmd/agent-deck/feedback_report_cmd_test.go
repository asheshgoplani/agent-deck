package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
)

// Fake gh for `agent-deck feedback bug|idea` tests: shell builtins only,
// records every call in $FAKE_GH_DIR/calls and the issue-create stdin in
// $FAKE_GH_DIR/body. FAKE_GH_AUTH=ok|fail, FAKE_GH_CREATE=ok|fail.
const reportFakeGh = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_DIR/calls"
case "$1 $2" in
"auth status")
  [ "$FAKE_GH_AUTH" = ok ] && exit 0
  exit 1 ;;
"issue create")
  while IFS= read -r line || [ -n "$line" ]; do printf '%s\n' "$line"; done > "$FAKE_GH_DIR/body"
  if [ "$FAKE_GH_CREATE" = fail ]; then echo "HTTP 403: forbidden" >&2; exit 1; fi
  echo "https://github.com/asheshgoplani/agent-deck/issues/4242"
  exit 0 ;;
esac
exit 2
`

type reportEnv struct {
	dir      string
	fileOpts []issuereport.FileOptions
}

// setupReportTest isolates HOME, puts a fake gh (or none) alone on PATH,
// pins the diagnostics and records the options File is called with.
func setupReportTest(t *testing.T, ghPresent bool, auth, create string) *reportEnv {
	t.Helper()
	isolateFeedbackHome(t)
	env := &reportEnv{dir: t.TempDir()}
	bin := filepath.Join(env.dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if ghPresent {
		if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(reportFakeGh), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("FAKE_GH_DIR", env.dir)
	t.Setenv("FAKE_GH_AUTH", auth)
	t.Setenv("FAKE_GH_CREATE", create)

	prevDiag, prevFile, prevBrowser := feedbackCollectDiagnostics, feedbackFileIssue, feedbackBrowserAvailable
	feedbackBrowserAvailable = func() bool { return true }
	feedbackCollectDiagnostics = func(version string, _ int) issuereport.Diagnostics {
		return issuereport.Diagnostics{Version: version, OS: "Linux (Test OS)", Arch: "amd64",
			Terminal: "xterm-256color", LogExcerpt: "log tail line"}
	}
	feedbackFileIssue = func(ctx context.Context, is issuereport.Issue, opts issuereport.FileOptions) (issuereport.FileResult, error) {
		env.fileOpts = append(env.fileOpts, opts)
		opts.Opener = func(string) error { t.Error("browser opener called from a test"); return nil }
		return issuereport.File(ctx, is, opts)
	}
	t.Cleanup(func() {
		feedbackCollectDiagnostics, feedbackFileIssue, feedbackBrowserAvailable = prevDiag, prevFile, prevBrowser
	})
	return env
}

func (e *reportEnv) calls() string {
	b, _ := os.ReadFile(filepath.Join(e.dir, "calls"))
	return string(b)
}

func (e *reportEnv) sentBody() (string, bool) {
	b, err := os.ReadFile(filepath.Join(e.dir, "body"))
	return string(b), err == nil
}

func runReport(t *testing.T, kind issuereport.Kind, stdin string, args ...string) (string, int) {
	t.Helper()
	var out strings.Builder
	code := runFeedbackReport(context.Background(), kind, args, "1.16.33", strings.NewReader(stdin), &out)
	return out.String(), code
}

func decodeReportJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, s)
	}
	return m
}

var bugArgs = []string{"--title", "Typed letters doubled in a session",
	"--what", "each key shows twice", "--expected", "each key once", "--steps", "1. open a session\n2. type abc"}

func TestFeedbackReport_DryRunPrintsExactIssue(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	out, code := runReport(t, issuereport.KindBug, "", append([]string{"--dry-run"}, bugArgs...)...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	is := issuereport.Build(issuereport.Report{Kind: issuereport.KindBug, Title: bugArgs[1], WhatHappened: bugArgs[3],
		Expected: bugArgs[5], Steps: bugArgs[7], Surface: "cli"}, feedbackCollectDiagnostics("1.16.33", 40))
	url, _ := issuereport.NewIssueURL(is)
	for _, want := range []string{"Dry run: nothing is sent.", is.Text(), "Would file with: gh issue create",
		"Prefilled GitHub URL (browser fallback):\n" + url + "\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output lacks %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(env.calls(), "issue create") || len(env.fileOpts) != 0 {
		t.Fatal("dry run tried to file")
	}
}

func TestFeedbackReport_JSONDryRunShape(t *testing.T) {
	env := setupReportTest(t, false, "", "")
	out, code := runReport(t, issuereport.KindBug, "", append([]string{"--json", "--dry-run"}, bugArgs...)...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := decodeReportJSON(t, out)
	if m["ok"] != true || m["dry_run"] != true || m["gh_ready"] != false {
		t.Fatalf("bad flags in %v", m)
	}
	if m["title"] != "[bug] Typed letters doubled in a session" {
		t.Fatalf("title = %v", m["title"])
	}
	labels, _ := m["labels"].([]any)
	if len(labels) != 2 || labels[0] != "bug" || labels[1] != "triage" {
		t.Fatalf("labels = %v", m["labels"])
	}
	body, _ := m["body"].(string)
	if !strings.Contains(body, "### Steps to reproduce\n\n1. open a session\n2. type abc") {
		t.Fatalf("body = %q", body)
	}
	if u, _ := m["url"].(string); !strings.HasPrefix(u, "https://github.com/asheshgoplani/agent-deck/issues/new?") || len(u) > issuereport.MaxURLLen {
		t.Fatalf("url = %q", u)
	}
	for _, k := range []string{"method", "error_code"} {
		if _, ok := m[k]; ok {
			t.Fatalf("dry-run JSON has %q: %v", k, m)
		}
	}
	if len(env.fileOpts) != 0 {
		t.Fatal("dry run tried to file")
	}

	// gh_ready follows gh auth status.
	setupReportTest(t, true, "ok", "ok")
	out, _ = runReport(t, issuereport.KindBug, "", append([]string{"--json", "--dry-run"}, bugArgs...)...)
	if decodeReportJSON(t, out)["gh_ready"] != true {
		t.Fatalf("gh_ready false with a logged-in gh: %s", out)
	}
}

func TestFeedbackReport_JSONWithoutYesIsConfirmRequired(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	out, code := runReport(t, issuereport.KindBug, "", append([]string{"--json"}, bugArgs...)...)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	m := decodeReportJSON(t, out)
	if m["ok"] != false || m["error_code"] != "confirm_required" {
		t.Fatalf("got %v", m)
	}
	if env.calls() != "" || len(env.fileOpts) != 0 {
		t.Fatalf("gh was called without --yes: %q", env.calls())
	}
}

func TestFeedbackReport_JSONYesFilesWithGh(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	dry, _ := runReport(t, issuereport.KindBug, "", append([]string{"--json", "--dry-run"}, bugArgs...)...)
	out, code := runReport(t, issuereport.KindBug, "", append([]string{"--json", "--yes"}, bugArgs...)...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := decodeReportJSON(t, out)
	if m["ok"] != true || m["method"] != "gh" || m["url"] != "https://github.com/asheshgoplani/agent-deck/issues/4242" {
		t.Fatalf("got %v", m)
	}
	sent, ok := env.sentBody()
	if !ok || sent != decodeReportJSON(t, dry)["body"] {
		t.Fatalf("body sent to gh differs from the dry-run body the user saw\nsent: %q", sent)
	}
	if len(env.fileOpts) != 1 || env.fileOpts[0].OpenBrowser {
		t.Fatalf("--json must not open a browser: %+v", env.fileOpts)
	}
}

func TestFeedbackReport_JSONYesWithoutGhReturnsBrowserURL(t *testing.T) {
	env := setupReportTest(t, false, "", "")
	out, code := runReport(t, issuereport.KindIdea, "", "--json", "--yes", "--title", "Report from the app", "--what", "no quick way")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	m := decodeReportJSON(t, out)
	u, _ := m["url"].(string)
	if m["method"] != "browser" || !strings.HasPrefix(u, "https://github.com/asheshgoplani/agent-deck/issues/new?") ||
		!strings.Contains(u, "labels=enhancement%2Ctriage") {
		t.Fatalf("got %v", m)
	}
	if len(env.fileOpts) != 1 || env.fileOpts[0].OpenBrowser {
		t.Fatalf("--json must not open a browser: %+v", env.fileOpts)
	}
}

func TestFeedbackReport_JSONErrors(t *testing.T) {
	setupReportTest(t, true, "ok", "fail")
	out, code := runReport(t, issuereport.KindBug, "", append([]string{"--json", "--yes"}, bugArgs...)...)
	m := decodeReportJSON(t, out)
	if code != 1 || m["ok"] != false || m["error_code"] != "gh_failed" || m["message"] == "" {
		t.Fatalf("gh failure: exit %d %v", code, m)
	}
	if u, _ := m["url"].(string); !strings.HasPrefix(u, "https://github.com/") {
		t.Fatalf("gh_failed lacks the fallback url: %v", m)
	}

	env := setupReportTest(t, true, "ok", "ok")
	out, code = runReport(t, issuereport.KindBug, "", "--json", "--yes")
	if m := decodeReportJSON(t, out); code != 1 || m["error_code"] != "invalid_input" {
		t.Fatalf("empty report: exit %d %v", code, m)
	}
	out, code = runReport(t, issuereport.KindBug, "", "--json", "--bogus")
	if m := decodeReportJSON(t, out); code != 1 || m["error_code"] != "invalid_input" {
		t.Fatalf("bad flag: exit %d %v", code, m)
	}
	if strings.Contains(env.calls(), "issue create") {
		t.Fatal("invalid input reached gh")
	}
}

// Interactive: prompts for what is missing, prints the full issue and
// sends nothing unless the answer is y.
func TestFeedbackReport_InteractiveDeclineSendsNothing(t *testing.T) {
	for _, answer := range []string{"n", "", "yes please"} {
		env := setupReportTest(t, true, "ok", "ok")
		out, code := runReport(t, issuereport.KindBug, "each key shows twice\neach key once\n\nTyped letters doubled\n"+answer+"\n")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		for _, want := range []string{"What happened? ", "What did you expect? ", "Steps to reproduce (optional",
			"Title:  [bug] Typed letters doubled", "### Expected vs actual\n\nExpected: each key once\nActual: each key shows twice",
			"File this on GitHub? [y/N]: ", "Not sent. Nothing left this machine."} {
			if !strings.Contains(out, want) {
				t.Errorf("answer %q: output lacks %q\n---\n%s", answer, want, out)
			}
		}
		if strings.Contains(env.calls(), "issue create") || len(env.fileOpts) != 0 {
			t.Fatalf("answer %q filed the issue: %q", answer, env.calls())
		}
		if _, sent := env.sentBody(); sent {
			t.Fatalf("answer %q: a body reached gh", answer)
		}
	}
}

func TestFeedbackReport_InteractiveYesFiles(t *testing.T) {
	env := setupReportTest(t, true, "ok", "ok")
	out, code := runReport(t, issuereport.KindBug, "y\n", bugArgs...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "Filed: https://github.com/asheshgoplani/agent-deck/issues/4242") {
		t.Fatalf("output:\n%s", out)
	}
	if strings.Contains(out, "What happened? ") {
		t.Fatal("prompted for a field given as a flag")
	}
	if _, sent := env.sentBody(); !sent {
		t.Fatal("no body reached gh after y")
	}
}

func TestFeedbackReport_InteractiveNoOpenPrintsURL(t *testing.T) {
	env := setupReportTest(t, false, "", "")
	out, code := runReport(t, issuereport.KindIdea, "y\n", "--no-open", "--what", "a problem", "--expected", "a fix", "--steps", "none", "--title", "t")
	if code != 0 || !strings.Contains(out, "Open this URL and press Submit to file it:\nhttps://github.com/asheshgoplani/agent-deck/issues/new?") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if len(env.fileOpts) != 1 || env.fileOpts[0].OpenBrowser {
		t.Fatalf("--no-open still opens: %+v", env.fileOpts)
	}
}

func TestFeedbackReport_Help(t *testing.T) {
	setupReportTest(t, false, "", "")
	out, code := runReport(t, issuereport.KindBug, "", "--help")
	if code != 0 || !strings.Contains(out, "Usage: agent-deck feedback bug [flags]") || !strings.Contains(out, "--dry-run") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// Review MEDIUM (CLI parity with the TUI): when File could not open a
// browser (headless/SSH host), the CLI must not claim it did; it prints the
// prefilled URL to open by hand.
func TestFeedbackReport_HeadlessPrintsURLWithoutClaimingBrowser(t *testing.T) {
	setupReportTest(t, false, "", "")
	const u = "https://github.com/asheshgoplani/agent-deck/issues/new?title=t"
	for _, opened := range []bool{false, true} {
		feedbackFileIssue = func(context.Context, issuereport.Issue, issuereport.FileOptions) (issuereport.FileResult, error) {
			return issuereport.FileResult{Method: "browser", URL: u, Opened: opened}, nil
		}
		out, code := runReport(t, issuereport.KindBug, "y\n", bugArgs...)
		if code != 0 || !strings.Contains(out, u) {
			t.Fatalf("opened=%v: exit %d\n%s", opened, code, out)
		}
		claims := strings.Contains(out, "Opened your browser")
		if claims != opened {
			t.Fatalf("opened=%v but output claims a browser opened=%v:\n%s", opened, claims, out)
		}
		if !opened && !strings.Contains(out, "No browser can open here") {
			t.Fatalf("headless output does not say why the URL is printed:\n%s", out)
		}
	}
}

// Review LOW (mutation gap): the preview's "How" line must say the user gets
// a prefilled URL when gh is not ready and no browser can open here, and
// must only promise a browser when one is available.
func TestFeedbackReport_HowLineFollowsBrowserAvailability(t *testing.T) {
	const headless = "How: gh is not installed or not logged in; you get a prefilled GitHub URL and press Submit there."
	const browser = "How: gh is not installed or not logged in; your browser opens a prefilled GitHub page and you press Submit there."
	for _, avail := range []bool{false, true} {
		env := setupReportTest(t, false, "", "")
		feedbackBrowserAvailable = func() bool { return avail }
		out, code := runReport(t, issuereport.KindBug, "n\n", bugArgs...)
		if code != 0 {
			t.Fatalf("browser=%v: exit %d\n%s", avail, code, out)
		}
		want, notWant := headless, browser
		if avail {
			want, notWant = browser, headless
		}
		if !strings.Contains(out, want) || strings.Contains(out, notWant) {
			t.Errorf("browser=%v: want %q, not %q\n---\n%s", avail, want, notWant, out)
		}
		if len(env.fileOpts) != 0 {
			t.Fatalf("browser=%v: declined preview still filed", avail)
		}
	}
}
