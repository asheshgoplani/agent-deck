package main

import (
	"encoding/json"
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
