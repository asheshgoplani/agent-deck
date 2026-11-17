package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// Top-level `remove` (alias `rm`) and `session remove` are both documented as
// "Remove a session". `remove` used to kill a running session at once while
// `session remove` refused without --force; `remove` also ignored extra
// positional arguments. Both now share one guard and one refusal text.

var removeCommands = []struct {
	name string
	args func(id string, extra ...string) []string
}{
	{"remove", func(id string, extra ...string) []string { return append([]string{"remove", id}, extra...) }},
	{"rm", func(id string, extra ...string) []string { return append([]string{"rm", id}, extra...) }},
	{"session remove", func(id string, extra ...string) []string {
		return append([]string{"session", "remove", id}, extra...)
	}},
}

func removeGuardListed(t *testing.T, home, id string) bool {
	t.Helper()
	return strings.Contains(readSessionsJSON(t, home), id)
}

func TestRemove_RunningSessionNeedsForce(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	testutil.RequireTmux(t) // starts a real session pane
	for _, rc := range removeCommands {
		t.Run(rc.name, func(t *testing.T) {
			home := t.TempDir()
			id := addOpenTestSession(t, home, "live-rm")
			if stdout, stderr, code := runAgentDeck(t, home, "session", "start", id); code != 0 {
				t.Fatalf("session start: exit %d\n%s\n%s", code, stdout, stderr)
			}
			t.Cleanup(func() { _, _, _ = runAgentDeck(t, home, "session", "stop", id) })

			stdout, stderr, code := runAgentDeck(t, home, rc.args(id, "--json")...)
			if code != 1 {
				t.Fatalf("%s of a running session without --force: exit %d, want 1\n%s\n%s", rc.name, code, stdout, stderr)
			}
			var res map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &res); err != nil || !strings.Contains(res["error"].(string), "--force") {
				t.Errorf("want a JSON refusal naming --force, got %v\n%s", err, stdout)
			}
			if !removeGuardListed(t, home, id) {
				t.Fatalf("%s removed a running session without --force", rc.name)
			}
			out, _, _ := runAgentDeck(t, home, "session", "show", id, "--json")
			if strings.Contains(out, `"status": "stopped"`) || strings.Contains(out, `"status": "error"`) {
				t.Errorf("%s stopped the running session it refused to remove:\n%s", rc.name, out)
			}

			if stdout, stderr, code := runAgentDeck(t, home, rc.args(id, "--force", "--json")...); code != 0 {
				t.Fatalf("%s --force: exit %d\n%s\n%s", rc.name, code, stdout, stderr)
			}
			if removeGuardListed(t, home, id) {
				t.Errorf("%s --force left the session listed", rc.name)
			}
		})
	}
}

// The refusal reads the same from both commands.
func TestRemove_SameRefusalWording(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	var refusals []string
	for _, rc := range removeCommands {
		home := t.TempDir()
		id := addTestSession(t, home, filepath.Join(home, "proj"), "busy-rm")
		forceSetStatus(t, home, id, session.StatusRunning)
		_, stderr, code := runAgentDeck(t, home, rc.args(id)...)
		if code != 1 {
			t.Fatalf("%s of a running row: exit %d, want 1\n%s", rc.name, code, stderr)
		}
		refusals = append(refusals, strings.TrimSpace(stderr))
	}
	for i := 1; i < len(refusals); i++ {
		if refusals[i] != refusals[0] {
			t.Errorf("refusal differs:\n%s: %s\n%s: %s", removeCommands[0].name, refusals[0], removeCommands[i].name, refusals[i])
		}
	}
}

// A session that was added but never started has nothing to kill: both
// commands remove it without --force.
func TestRemove_NeverStartedSessionNeedsNoForce(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	for _, rc := range removeCommands {
		home := t.TempDir()
		id := addOpenTestSession(t, home, "fresh-rm")
		if stdout, stderr, code := runAgentDeck(t, home, rc.args(id, "--json")...); code != 0 {
			t.Fatalf("%s of a never-started session: exit %d\n%s\n%s", rc.name, code, stdout, stderr)
		}
		if removeGuardListed(t, home, id) {
			t.Errorf("%s left the never-started session listed", rc.name)
		}
	}
}

func TestRemove_RejectsExtraArguments(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	for _, rc := range removeCommands {
		home := t.TempDir()
		id := addOpenTestSession(t, home, "extra-rm")
		stdout, stderr, code := runAgentDeck(t, home, rc.args(id, "other-session")...)
		if code != 1 {
			t.Errorf("%s with two names: exit %d, want 1\n%s\n%s", rc.name, code, stdout, stderr)
		}
		if !removeGuardListed(t, home, id) {
			t.Errorf("%s with an extra argument still removed the session", rc.name)
		}
	}
}
