package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sessionRefJSON struct {
	OK        bool   `json:"ok"`
	ErrorCode string `json:"error_code"`
	Error     string `json:"error"`
	Source    struct {
		ID, Title, Path, Status string
	} `json:"source"`
	Target struct {
		ID, Title string
	} `json:"target"`
	Text     string `json:"text"`
	Line     string `json:"line"`
	Inserted bool   `json:"inserted"`
}

func runSessionRefJSON(t *testing.T, home string, args ...string) (sessionRefJSON, int, string) {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, args...)
	var res sessionRefJSON
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("%v: not JSON (exit %d): %q %q", args, code, stdout, stderr)
	}
	return res, code, stdout
}

// writeSessionRefSocket points the CLI's tmux at a private server named
// session-ref-<purpose>-<pid>, never the user's.
func writeSessionRefSocket(t *testing.T, home, purpose string) string {
	t.Helper()
	socket := fmt.Sprintf("session-ref-%s-%d", purpose, os.Getpid())
	dir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[tmux]\nsocket_name = %q\n\n[feedback]\ndisabled = true\n\n[telemetry]\ndisabled = true\n", socket)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return socket
}

// --print builds the reference without typing anything, so B need not run.
func TestSessionRefCLI_PrintJSON(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	writeSessionRefSocket(t, home, "print")
	a := addProfileTestSession(t, home, "_test", "ref-alpha")
	b := addProfileTestSession(t, home, "_test", "ref-beta")

	res, code, raw := runSessionRefJSON(t, home, "-p", "_test", "session", "ref", "ref-alpha", b, "--print", "--json")
	if code != 0 || !res.OK || res.Inserted {
		t.Fatalf("exit %d: %s", code, raw)
	}
	if res.Source.ID != a || res.Source.Title != "ref-alpha" || res.Target.ID != b || res.Target.Title != "ref-beta" {
		t.Fatalf("endpoints wrong: %s", raw)
	}
	wantHead := fmt.Sprintf("[ref] session \"ref-alpha\" (%s) · ", a)
	wantRead := fmt.Sprintf("read it with: agent-deck -p _test session output %s -q", a)
	if !strings.HasPrefix(res.Text, wantHead) || !strings.Contains(res.Text, "\n"+wantRead) {
		t.Fatalf("text = %q, want head %q and %q", res.Text, wantHead, wantRead)
	}
	if strings.ContainsAny(res.Line, "\r\n") || !strings.Contains(res.Line, wantRead) {
		t.Fatalf("line = %q", res.Line)
	}

	// Human --print is the text alone, no "typed into" claim.
	stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "session", "ref", a, b, "--print")
	if code != 0 || strings.TrimSpace(stdout) != res.Text || strings.Contains(stdout, "typed into") {
		t.Fatalf("human --print: exit %d %q %q", code, stdout, stderr)
	}
}

func TestSessionRefCLI_Refusals(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()
	writeSessionRefSocket(t, home, "refuse")
	a := addProfileTestSession(t, home, "_test", "ref-one")
	b := addProfileTestSession(t, home, "_test", "ref-two")
	other := addProfileTestSession(t, home, "_test_other", "ref-elsewhere")

	cases := []struct {
		name string
		args []string
		code string
		exit int
	}{
		{"same session", []string{"session", "ref", a, "ref-one", "--json"}, "same_session", 1},
		{"target not running", []string{"session", "ref", a, b, "--json"}, "target_not_running", 1},
		{"cross profile", []string{"session", "ref", a, other, "--json"}, "cross_profile", 1},
		{"unknown", []string{"session", "ref", a, "no-such-session", "--json"}, "missing_session", 2},
		{"one arg", []string{"session", "ref", a, "--json"}, "missing_session", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, code, raw := runSessionRefJSON(t, home, append([]string{"-p", "_test"}, c.args...)...)
			if code != c.exit || res.OK || res.ErrorCode != c.code || res.Error == "" {
				t.Fatalf("exit %d, want %d with %s: %s", code, c.exit, c.code, raw)
			}
		})
	}
	// Human refusal: clear message on stderr, nothing on stdout.
	stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "session", "ref", a, a)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "cannot take a reference to itself") {
		t.Fatalf("human refusal: exit %d %q %q", code, stdout, stderr)
	}
}

// End to end on a private tmux server: B runs cat, the reference is typed
// into it and not submitted (cat would print a submitted line a second time).
func TestSessionRefCLI_InsertsIntoRunningTargetWithoutEnter(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	parallelSubprocessTest(t)
	home := t.TempDir()
	socket := writeSessionRefSocket(t, home, "insert")
	a := addProfileTestSession(t, home, "_test", "ref-src")
	stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "add", t.TempDir(), "--title", "ref-dst", "-c", "cat", "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add dst: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add dst: %q %v", stdout, err)
	}
	b := added.ID
	if stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "session", "start", b, "--json"); code != 0 {
		t.Fatalf("start: %d %s %s", code, stdout, stderr)
	}
	t.Cleanup(func() { _, _, _ = runAgentDeck(t, home, "-p", "_test", "session", "stop", b) })
	stdout, _, _ = runAgentDeck(t, home, "-p", "_test", "session", "show", b, "--json")
	var shown struct {
		TmuxSession string `json:"tmux_session"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil || shown.TmuxSession == "" {
		t.Fatalf("show: %q %v", stdout, err)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", "="+shown.TmuxSession).Run() })

	capture := func() string {
		out, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-J", "-t", shown.TmuxSession+":^").CombinedOutput()
		return string(out)
	}
	// Wait for the pane to be up before typing (bounded).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("tmux", "-L", socket, "has-session", "-t", "="+shown.TmuxSession).Run() == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	res, code, raw := runSessionRefJSON(t, home, "-p", "_test", "session", "ref", a, b, "--json")
	if code != 0 || !res.OK || !res.Inserted {
		t.Fatalf("ref into running cat: exit %d %s", code, raw)
	}
	marker := "session output " + a + " -q"
	count := func() int { return strings.Count(capture(), marker) }
	deadline = time.Now().Add(3 * time.Second)
	n := count()
	for n < 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		n = count()
	}
	if n != 1 {
		t.Fatalf("pointer shows %d times after insert, want 1:\n%s", n, capture())
	}
	time.Sleep(400 * time.Millisecond)
	if n := count(); n != 1 {
		t.Fatalf("reference was submitted (shows %d times):\n%s", n, capture())
	}
}

// TestSessionRefCLI_RefusesTargetShowingMenu: end to end on a private tmux
// server, B's pane shows a Claude permission menu (then waits in cat, so any
// typed key would echo). The CLI refuses with error_code target_menu_open,
// exit 1, and B's pane is unchanged (ASK-111 HIGH-1).
func TestSessionRefCLI_RefusesTargetShowingMenu(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	parallelSubprocessTest(t)
	home := t.TempDir()
	socket := writeSessionRefSocket(t, home, "menu")
	a := addProfileTestSession(t, home, "_test", "ref-src")

	dir := t.TempDir()
	script := filepath.Join(dir, "menu-pane.sh")
	menu := "⏺ Bash(make test)\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and don't ask again for make commands\n   3. No, and tell Claude what to do differently (esc)\n"
	if err := os.WriteFile(script+".txt", []byte(menu), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat \"$0.txt\"\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "add", dir, "--title", "ref-menu", "-c", script, "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add dst: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add dst: %q %v", stdout, err)
	}
	b := added.ID
	if stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "session", "start", b, "--json"); code != 0 {
		t.Fatalf("start: %d %s %s", code, stdout, stderr)
	}
	t.Cleanup(func() { _, _, _ = runAgentDeck(t, home, "-p", "_test", "session", "stop", b) })
	stdout, _, _ = runAgentDeck(t, home, "-p", "_test", "session", "show", b, "--json")
	var shown struct {
		TmuxSession string `json:"tmux_session"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil || shown.TmuxSession == "" {
		t.Fatalf("show: %q %v", stdout, err)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", "="+shown.TmuxSession).Run() })
	capture := func() string {
		out, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-J", "-t", shown.TmuxSession+":^").CombinedOutput()
		return string(out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(capture(), "Do you want to proceed?") {
		if time.Now().After(deadline) {
			t.Fatalf("menu fixture never drew:\n%s", capture())
		}
		time.Sleep(50 * time.Millisecond)
	}
	before := capture()

	res, code, raw := runSessionRefJSON(t, home, "-p", "_test", "session", "ref", a, b, "--json")
	if code != 1 || res.OK || res.ErrorCode != "target_menu_open" || res.Inserted {
		t.Fatalf("ref into a pane showing a menu: exit %d %s", code, raw)
	}
	if !strings.Contains(res.Error, "showing a menu/prompt; answer it first") {
		t.Errorf("error = %q", res.Error)
	}
	time.Sleep(400 * time.Millisecond)
	if after := capture(); after != before || strings.Contains(after, a) {
		t.Fatalf("B's pane changed after the refusal:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
