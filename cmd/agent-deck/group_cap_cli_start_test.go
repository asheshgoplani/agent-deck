package main

import (
	"encoding/json"
	"os/exec"
	"testing"
)

// A group with max_concurrent=1 used to queue nothing from the CLI: `session
// start` stores "starting", the cap counted only stored "running", so three
// starts in a row all ran. A started session that is still alive holds its
// slot; the next starts are queued.
func TestSessionStart_GroupCapQueuesAfterACLIStart(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	project := t.TempDir()
	if got := runGroupCreate(t, home, "q"); got != 1 {
		t.Fatalf("group create cap = %d, want 1", got)
	}
	titles := []string{"s1", "s2", "s3"}
	for _, title := range titles {
		if stdout, stderr, code := runAgentDeck(t, home, "add", "-t", title, "-cmd", "sleep 600", "-g", "q", "--no-parent", project); code != 0 {
			t.Fatalf("add %s failed (%d)\n%s\n%s", title, code, stdout, stderr)
		}
	}
	t.Cleanup(func() {
		for _, title := range titles {
			runAgentDeck(t, home, "session", "stop", title)
		}
	})
	start := func(title string) string {
		t.Helper()
		stdout, stderr, code := runAgentDeck(t, home, "session", "start", title, "--json")
		if code != 0 {
			t.Fatalf("session start %s failed (%d)\n%s\n%s", title, code, stdout, stderr)
		}
		var resp struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("unmarshal start %s: %v\n%s", title, err, stdout)
		}
		return resp.Status
	}
	if got := start("s1"); got == "queued" {
		t.Fatalf("first start in an empty group was queued")
	}
	for _, title := range titles[1:] {
		if got := start(title); got != "queued" {
			t.Errorf("session start %s with one session live in a max_concurrent=1 group: status %q, want queued", title, got)
		}
	}
}
