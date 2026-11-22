package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// `profile delete` removes a whole profile directory. It used to ask before
// it knew the profile existed, skip the question in --json mode, never say how
// many sessions go with it, and delete a profile whose sessions were still
// running, leaving their tmux sessions and agents alive and untracked.

func profileDeleteAdd(t *testing.T, home, profile, title string) {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "-p", profile, "add", t.TempDir(), "-t", title, "-c", "bash", "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add %s/%s: exit %d\n%s\n%s", profile, title, code, stdout, stderr)
	}
}

func profileDeleteExists(t *testing.T, home, profile string) bool {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "profile", "list", "--json")
	if code != 0 {
		t.Fatalf("profile list: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var got struct {
		Profiles []struct {
			Name string `json:"name"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("profile list --json: %v\n%s", err, stdout)
	}
	for _, p := range got.Profiles {
		if p.Name == profile {
			return true
		}
	}
	return false
}

func TestProfileDelete_MissingProfileFailsBeforeAsking(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	profileDeleteAdd(t, home, "work", "s1")
	stdout, stderr, code := runAgentDeckStdin(t, home, "y\n", "profile", "delete", "ghost")
	if code == 0 {
		t.Fatalf("profile delete ghost: exit 0\n%s\n%s", stdout, stderr)
	}
	if strings.Contains(stdout, "[y/N]") {
		t.Errorf("asked to confirm deleting a profile that does not exist:\n%s", stdout)
	}
	if !strings.Contains(stdout+stderr, "does not exist") {
		t.Errorf("no 'does not exist' error:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

func TestProfileDelete_PromptNamesSessionCount(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	profileDeleteAdd(t, home, "work", "s1")
	profileDeleteAdd(t, home, "work", "s2")
	stdout, stderr, code := runAgentDeckStdin(t, home, "n\n", "profile", "delete", "work")
	if code != 0 {
		t.Fatalf("profile delete work (answered n): exit %d\n%s\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "2 sessions") {
		t.Errorf("prompt does not say how many sessions go with the profile:\n%s", stdout)
	}
	if !profileDeleteExists(t, home, "work") {
		t.Fatal("answering n deleted the profile")
	}
}

func TestProfileDelete_JSONNeedsYes(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	profileDeleteAdd(t, home, "work", "s1")

	stdout, stderr, code := runAgentDeck(t, home, "profile", "delete", "work", "--json")
	if code == 0 {
		t.Fatalf("profile delete --json without --yes: exit 0\n%s\n%s", stdout, stderr)
	}
	var res map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || res["success"] != false || !strings.Contains(res["error"].(string), "--yes") {
		t.Errorf("want a JSON error naming --yes, got %v\n%s\n%s", err, stdout, stderr)
	}
	if !profileDeleteExists(t, home, "work") {
		t.Fatal("--json alone deleted the profile without confirmation")
	}

	stdout, stderr, code = runAgentDeck(t, home, "profile", "delete", "work", "--json", "--yes")
	if code != 0 {
		t.Fatalf("profile delete --json --yes: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || res["deleted"] != true || res["sessions"] != float64(1) {
		t.Errorf("want {deleted:true, sessions:1}, got %v\n%s", err, stdout)
	}
	if profileDeleteExists(t, home, "work") {
		t.Fatal("profile still listed after --json --yes")
	}
}

func TestProfileDelete_RefusesWhileSessionsRun(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	testutil.RequireTmux(t) // starts a real session pane
	home := t.TempDir()
	profileDeleteAdd(t, home, "work", "live1")
	profileDeleteAdd(t, home, "work", "idle1")
	if stdout, stderr, code := runAgentDeck(t, home, "-p", "work", "session", "start", "live1"); code != 0 {
		t.Fatalf("session start: exit %d\n%s\n%s", code, stdout, stderr)
	}
	t.Cleanup(func() { _, _, _ = runAgentDeck(t, home, "-p", "work", "session", "stop", "live1") })

	for _, args := range [][]string{
		{"profile", "delete", "work", "--yes"},
		{"profile", "delete", "work", "--yes", "--json"},
	} {
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code == 0 {
			t.Fatalf("%v with a running session: exit 0\n%s\n%s", args, stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "live1") {
			t.Errorf("%v: refusal does not name the running session:\n%s\n%s", args, stdout, stderr)
		}
		if !profileDeleteExists(t, home, "work") {
			t.Fatalf("%v deleted a profile with a running session (its agent is now an orphan)", args)
		}
	}
	stdout, stderr, code := runAgentDeck(t, home, "-p", "work", "list", "--json")
	if code != 0 || !strings.Contains(stdout, "live1") {
		t.Fatalf("running session no longer listed after the refused delete: exit %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestProfileDelete_KeepsLastProfileGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	// Only the default profile exists: the helper's own test profile must
	// not count as a second one.
	env := []string{"AGENTDECK_PROFILE=default"}
	if stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "add", t.TempDir(), "-t", "s1", "-c", "bash", "--no-parent", "--json"); code != 0 {
		t.Fatalf("add: exit %d\n%s\n%s", code, stdout, stderr)
	}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "profile", "delete", "default", "--yes")
	if code == 0 {
		t.Fatalf("deleted the only profile: exit 0\n%s\n%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "only remaining profile") {
		t.Errorf("want the last-profile refusal:\n%s\n%s", stdout, stderr)
	}
}
