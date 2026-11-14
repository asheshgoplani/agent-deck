package issuereport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGhScript is a stand-in gh. It uses only shell builtins (PATH holds
// nothing but its own directory) and records every call's argv, plus the
// stdin body of `issue create`, under $FAKE_GH_DIR.
const fakeGhScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_DIR/calls"
case "$1 $2" in
"auth status")
  [ "$FAKE_GH_AUTH" = ok ] && exit 0
  echo "You are not logged into any GitHub hosts." >&2
  exit 1 ;;
"issue create")
  i=0
  for a in "$@"; do i=$((i+1)); printf '%s\n' "$a" >> "$FAKE_GH_DIR/argv"; done
  while IFS= read -r line || [ -n "$line" ]; do printf '%s\n' "$line"; done > "$FAKE_GH_DIR/body"
  if [ "$FAKE_GH_CREATE" = fail ]; then
    echo "could not add label: 'triage' not found" >&2
    exit 1
  fi
  echo "Creating issue in asheshgoplani/agent-deck"
  echo
  echo "https://github.com/asheshgoplani/agent-deck/issues/4242"
  exit 0 ;;
esac
exit 2
`

// installFakeGh puts a fake gh on an otherwise empty PATH. auth: "ok" or
// "fail"; create: "ok" or "fail". present=false leaves PATH empty (no gh).
func installFakeGh(t *testing.T, present bool, auth, create string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if present {
		if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGhScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("FAKE_GH_DIR", dir)
	t.Setenv("FAKE_GH_AUTH", auth)
	t.Setenv("FAKE_GH_CREATE", create)
	return dir
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func sampleIssue() Issue {
	return Build(Report{Kind: KindBug, Title: "Typed letters doubled", WhatHappened: "abc shows aabbcc",
		Expected: "abc", Steps: "1. type abc"}, testDiag)
}

type openRecorder struct{ urls []string }

func (o *openRecorder) open(u string) error { o.urls = append(o.urls, u); return nil }

func TestFile_GhLoggedIn_CreatesIssue(t *testing.T) {
	dir := installFakeGh(t, true, "ok", "ok")
	rec := &openRecorder{}
	is := sampleIssue()
	res, err := File(context.Background(), is, FileOptions{OpenBrowser: true, Opener: rec.open})
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if res.Method != "gh" || res.URL != "https://github.com/asheshgoplani/agent-deck/issues/4242" {
		t.Fatalf("result = %+v", res)
	}
	if len(rec.urls) != 0 {
		t.Fatalf("browser opened on the gh path: %v", rec.urls)
	}
	wantArgv := []string{"issue", "create", "--repo", "asheshgoplani/agent-deck", "--title", is.Title,
		"--body-file", "-", "--label", "bug", "--label", "triage"}
	if got := strings.Split(strings.TrimSuffix(readFile(t, filepath.Join(dir, "argv")), "\n"), "\n"); strings.Join(got, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Fatalf("gh argv\n got: %q\nwant: %q", got, wantArgv)
	}
	if got := readFile(t, filepath.Join(dir, "body")); got != is.Body {
		t.Fatalf("gh stdin body differs from the previewed body\n got: %q\nwant: %q", got, is.Body)
	}
}

func TestFile_GhNotLoggedIn_FallsBackToBrowser(t *testing.T) {
	dir := installFakeGh(t, true, "fail", "ok")
	rec := &openRecorder{}
	is := sampleIssue()
	res, err := File(context.Background(), is, FileOptions{OpenBrowser: true, Opener: rec.open})
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	want, _ := NewIssueURL(is)
	if res.Method != "browser" || res.URL != want {
		t.Fatalf("result = %+v, want browser %s", res, want)
	}
	if len(rec.urls) != 1 || rec.urls[0] != want {
		t.Fatalf("opened %v, want [%s]", rec.urls, want)
	}
	if strings.Contains(readFile(t, filepath.Join(dir, "calls")), "issue create") {
		t.Fatal("gh issue create ran although gh auth status failed")
	}
}

func TestFile_GhMissing_BrowserWithoutOpening(t *testing.T) {
	installFakeGh(t, false, "", "")
	rec := &openRecorder{}
	res, err := File(context.Background(), sampleIssue(), FileOptions{OpenBrowser: false, Opener: rec.open})
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if res.Method != "browser" || !strings.HasPrefix(res.URL, "https://github.com/asheshgoplani/agent-deck/issues/new?") {
		t.Fatalf("result = %+v", res)
	}
	if len(rec.urls) != 0 {
		t.Fatalf("OpenBrowser=false but opened %v", rec.urls)
	}
}

func TestFile_GhCreateFails_GhFailed(t *testing.T) {
	installFakeGh(t, true, "ok", "fail")
	rec := &openRecorder{}
	_, err := File(context.Background(), sampleIssue(), FileOptions{OpenBrowser: true, Opener: rec.open})
	if CodeOf(err) != ErrGhFailed {
		t.Fatalf("err = %v (code %q), want gh_failed", err, CodeOf(err))
	}
	var e *Error
	if !errors.As(err, &e) || !strings.HasPrefix(e.URL, "https://github.com/") {
		t.Fatalf("gh_failed carries no fallback URL: %#v", err)
	}
	if !strings.Contains(e.Message, "not found") {
		t.Fatalf("message lacks gh stderr: %q", e.Message)
	}
	if len(rec.urls) != 0 {
		t.Fatal("browser opened silently after gh failed")
	}
}

func TestFile_BrowserOpenFails(t *testing.T) {
	installFakeGh(t, false, "", "")
	_, err := File(context.Background(), sampleIssue(), FileOptions{OpenBrowser: true,
		Opener: func(string) error { return errors.New("no display") }})
	if CodeOf(err) != ErrBrowserFailed {
		t.Fatalf("code = %q, want browser_failed", CodeOf(err))
	}
}

func TestFile_InvalidAndCancelled(t *testing.T) {
	dir := installFakeGh(t, true, "ok", "ok")
	if _, err := File(context.Background(), Issue{}, FileOptions{}); CodeOf(err) != ErrInvalidInput {
		t.Fatalf("empty issue code = %q", CodeOf(err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := File(ctx, sampleIssue(), FileOptions{}); CodeOf(err) != ErrCancelled {
		t.Fatalf("cancelled code = %q", CodeOf(err))
	}
	if readFile(t, filepath.Join(dir, "calls")) != "" {
		t.Fatal("gh was called for an invalid or cancelled filing")
	}
}

func TestGhReady(t *testing.T) {
	installFakeGh(t, true, "ok", "ok")
	if !GhReady(context.Background()) {
		t.Fatal("GhReady false with a logged-in gh")
	}
	installFakeGh(t, true, "fail", "ok")
	if GhReady(context.Background()) {
		t.Fatal("GhReady true with a logged-out gh")
	}
	installFakeGh(t, false, "", "")
	if GhReady(context.Background()) {
		t.Fatal("GhReady true without gh")
	}
}

func TestCollectDiagnostics_LogTailScrubbed(t *testing.T) {
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(t.TempDir(), "debug.log")
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, `{"msg":"tick","path":"`+home+`/proj","n":`+strings.Repeat("1", i%3+1)+`}`)
	}
	lines = append(lines, "token "+fake("gh"+"p_", 36)+" from 10.1.2.3")
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := debugLogPath
	debugLogPath = func() (string, error) { return logPath, nil }
	t.Cleanup(func() { debugLogPath = prev })
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("TERM_PROGRAM_VERSION", "3.5.0")

	d := CollectDiagnostics("1.16.33", 5)
	got := strings.Split(d.LogExcerpt, "\n")
	if len(got) != 5 {
		t.Fatalf("got %d log lines, want 5:\n%s", len(got), d.LogExcerpt)
	}
	if got[4] != "token <token> from <ip>" {
		t.Fatalf("last line not scrubbed: %q", got[4])
	}
	if strings.Contains(d.LogExcerpt, home) {
		t.Fatalf("home path left in log excerpt:\n%s", d.LogExcerpt)
	}
	if d.Version != "1.16.33" || d.Terminal != "iTerm.app 3.5.0" || d.Arch == "" || d.OS == "" {
		t.Fatalf("diagnostics = %+v", d)
	}
	if CollectDiagnostics("x", 0).LogExcerpt != "" {
		t.Fatal("logLines=0 still read the log")
	}
}

// Review MEDIUM: on a headless/SSH host File must not try to open a browser
// (xdg-open fails or starts a text browser over the caller's terminal); it
// returns the prefilled URL with Opened=false so the caller shows it.
func TestFile_Headless_DoesNotOpenBrowser(t *testing.T) {
	installFakeGh(t, true, "fail", "ok")
	prev := noBrowser
	t.Cleanup(func() { noBrowser = prev })

	noBrowser = func() bool { return true }
	rec := &openRecorder{}
	is := sampleIssue()
	res, err := File(context.Background(), is, FileOptions{OpenBrowser: true, Opener: rec.open})
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	want, _ := NewIssueURL(is)
	if res.Method != "browser" || res.URL != want || res.Opened {
		t.Fatalf("result = %+v, want browser %s not opened", res, want)
	}
	if len(rec.urls) != 0 {
		t.Fatalf("headless but opened %v", rec.urls)
	}

	noBrowser = func() bool { return false }
	res, err = File(context.Background(), is, FileOptions{OpenBrowser: true, Opener: rec.open})
	if err != nil || !res.Opened || len(rec.urls) != 1 {
		t.Fatalf("with a display: res=%+v err=%v opened=%v", res, err, rec.urls)
	}
}
