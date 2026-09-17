package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// showOrderPin reads `order` and `pin` back through `session show --json`,
// the same read a caller (the memento replace procedure) does.
func showOrderPin(t *testing.T, home, id string) (int, string) {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "session", "show", id, "--json")
	if code != 0 {
		t.Fatalf("session show failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var resp struct {
		Order *int    `json:"order"`
		Pin   *string `json:"pin"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}
	if resp.Order == nil || resp.Pin == nil {
		t.Fatalf("show --json lacks order or pin: %s", stdout)
	}
	return *resp.Order, *resp.Pin
}

func expectOrder(t *testing.T, home, id string, want int) {
	t.Helper()
	if got, _ := showOrderPin(t, home, id); got != want {
		t.Errorf("order of %s = %d, want %d", id, got, want)
	}
}

// TestSessionSetOrder_ReplaceSequenceKeepsPosition is the memento replace
// contract end to end: the clone takes the old row's position, and removing
// the old row leaves the clone where it is.
func TestSessionSetOrder_ReplaceSequenceKeepsPosition(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	old := addTestSession(t, home, workPath, "brain")
	sib := addTestSession(t, home, workPath, "sibling")
	clone := addTestSession(t, home, workPath, "brain (new)")
	expectOrder(t, home, old, 0)
	expectOrder(t, home, sib, 1)
	expectOrder(t, home, clone, 2)

	stdout, stderr, code := runAgentDeck(t, home, "session", "set", clone, "order", "0", "--json")
	if code != 0 {
		t.Fatalf("session set order failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var resp struct {
		Field    string `json:"field"`
		OldValue string `json:"old_value"`
		NewValue string `json:"new_value"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse set response: %v\nstdout: %s", err, stdout)
	}
	if resp.Field != "order" || resp.OldValue != "2" || resp.NewValue != "0" {
		t.Errorf("set response = %+v, want field order 2 -> 0", resp)
	}
	expectOrder(t, home, clone, 0)
	expectOrder(t, home, old, 1)
	expectOrder(t, home, sib, 2)

	forceSetStatus(t, home, old, session.StatusStopped)
	if stdout, stderr, code := runAgentDeck(t, home, "session", "remove", old, "--json"); code != 0 {
		t.Fatalf("session remove failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	expectOrder(t, home, clone, 0)
	expectOrder(t, home, sib, 1)
}

func TestSessionSetOrder_RejectsBadValues(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	a := addTestSession(t, home, workPath, "a")
	b := addTestSession(t, home, workPath, "b")

	// "--" keeps the flag parser from hoisting "-1" as an unknown flag, the
	// same terminator `set ... extra-args -- --flag` relies on.
	for _, bad := range []string{"-1", "x"} {
		stdout, stderr, code := runAgentDeck(t, home, "session", "set", "--", b, "order", bad)
		if code != 1 {
			t.Errorf("order %q: exit %d, want 1\nstdout: %s\nstderr: %s", bad, code, stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "invalid order") {
			t.Errorf("order %q: message lacks 'invalid order'\nstdout: %s\nstderr: %s", bad, stdout, stderr)
		}
	}
	expectOrder(t, home, a, 0)
	expectOrder(t, home, b, 1)
}

func TestSessionShow_JSONHasOrderAndPin(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	a := addTestSession(t, home, workPath, "a")
	b := addTestSession(t, home, workPath, "b")

	if order, pin := showOrderPin(t, home, b); order != 1 || pin != "" {
		t.Errorf("fresh row: order %d pin %q, want 1 and empty", order, pin)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "session", "set", b, "pin", "top"); code != 0 {
		t.Fatalf("session set pin failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	// The storage-layer sort puts pin-top rows first, so the pinned row's
	// position moves to 0 and the unpinned one after it.
	if order, pin := showOrderPin(t, home, b); pin != "top" || order != 0 {
		t.Errorf("after set pin top: order %d pin %q, want 0 and top", order, pin)
	}
	expectOrder(t, home, a, 1)
}

// TestSessionShow_CrossProfileTmuxFallback_ReportsRealOrder is the
// regression case for the CodeRabbit finding on PR #2300
// (cmd/agent-deck/session_cmd.go:1739): when `session show` has no
// explicit id and falls back to findSessionByTmuxAcrossProfiles because the
// tmux session belongs to a DIFFERENT profile than the CLI's own, the
// groupTree used to compute `order` must be built from the resolved
// profile's data, not the original one — otherwise the session isn't in
// the tree and `order` comes back -1.
//
// Does not use the shared runAgentDeck helper: that helper deliberately
// strips TMUX* from the subprocess environment for isolation, which is
// exactly the variable this fallback path is gated on.
func TestSessionShow_CrossProfileTmuxFallback_ReportsRealOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	if err := os.MkdirAll(workPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// The session lives in profile "other", never in the CLI's own "base"
	// profile that the invocation below will start from.
	addOut, addErr, addCode := runAgentDeck(t, home, "-p", "other", "add", "-t", "brain", "-c", "claude", "--no-parent", "--json", workPath)
	if addCode != 0 {
		t.Fatalf("add failed (exit %d)\nstdout: %s\nstderr: %s", addCode, addOut, addErr)
	}

	// Fake tmux: any "display-message" call reports an agentdeck session
	// name matching the "brain" title, the same way findSessionByTmux
	// parses a real tmux session name it manages.
	fakeBin := t.TempDir()
	fakeTmux := filepath.Join(fakeBin, "tmux")
	script := "#!/bin/sh\ncase \"$*\" in\n*display-message*) printf 'agentdeck_brain_ffffffff\\t" + workPath + "\\n'; exit 0 ;;\nesac\nexit 1\n"
	if err := os.WriteFile(fakeTmux, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}

	bin := channelsCLIBinary(t)
	cmd := exec.Command(bin, "session", "show", "--json")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"AGENTDECK_PROFILE=base",
		"TMUX=/tmp/tmux-fake-session,1,0",
		"TERM=dumb",
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
	)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("run binary: %v\nstdout: %s\nstderr: %s", err, outBuf.String(), errBuf.String())
		}
	}

	var resp struct {
		Profile string `json:"profile"`
		Order   *int   `json:"order"`
	}
	if err := json.Unmarshal([]byte(outBuf.String()), &resp); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s\nstderr: %s", err, outBuf.String(), errBuf.String())
	}
	if resp.Profile != "other" {
		t.Fatalf("resolved profile = %q, want %q (cross-profile tmux fallback did not fire)", resp.Profile, "other")
	}
	if resp.Order == nil {
		t.Fatalf("show --json lacks order: %s", outBuf.String())
	}
	if *resp.Order != 0 {
		t.Errorf("order = %d, want 0 - cross-profile session/group data was not reloaded before building groupTree", *resp.Order)
	}
}
