package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The one-line `status` printed "0 waiting • 0 running • 0 idle" for a
// profile whose sessions were all in error or stopped, while --json had them.
// The text and --json must count the same sessions.
func TestStatus_TextSummaryShowsStoppedAndError(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	dead := addTestSession(t, home, filepath.Join(home, "dead"), "dead")
	forceSetStartedStatus(t, home, dead, session.StatusRunning, time.Now().Add(-time.Hour))
	stopped := addTestSession(t, home, filepath.Join(home, "stopped"), "parked")
	forceSetStartedStatus(t, home, stopped, session.StatusStopped, time.Now().Add(-time.Hour))
	addTestSession(t, home, filepath.Join(home, "fresh"), "fresh")

	stdout, stderr, code := runAgentDeck(t, home, "status")
	if code != 0 {
		t.Fatalf("status failed (exit %d): %s %s", code, stdout, stderr)
	}
	line := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if line != "0 waiting • 0 running • 1 idle • 1 stopped • 1 error" {
		t.Errorf("status summary = %q, want error and stopped shown", line)
	}

	stdout, stderr, code = runAgentDeck(t, home, "status", "--json")
	if code != 0 {
		t.Fatalf("status --json failed (exit %d): %s %s", code, stdout, stderr)
	}
	var counts struct{ Waiting, Running, Idle, Error, Stopped, Total int }
	if err := json.Unmarshal([]byte(stdout), &counts); err != nil {
		t.Fatalf("parse status --json: %v\n%s", err, stdout)
	}
	if counts.Idle != 1 || counts.Error != 1 || counts.Stopped != 1 || counts.Total != 3 {
		t.Errorf("status --json = %+v, want idle 1, error 1, stopped 1, total 3 (the text line's counts)", counts)
	}
	if sum := counts.Waiting + counts.Running + counts.Idle + counts.Error + counts.Stopped; sum != counts.Total {
		t.Errorf("status --json buckets sum to %d, total is %d", sum, counts.Total)
	}

	stdout, stderr, code = runAgentDeck(t, home, "group", "list")
	if code != 0 {
		t.Fatalf("group list failed (exit %d): %s %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "■ 1") || !strings.Contains(stdout, "✕ 1") {
		t.Errorf("group list text hides stopped/error sessions its --json counts:\n%s", stdout)
	}
}

// Plain `list` had no status column and showed an archived session exactly
// like a live one. The table must carry the status list --json reports and
// mark archived rows.
func TestList_TableShowsStatusAndArchived(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	live := addTestSession(t, home, filepath.Join(home, "live"), "live-one")
	gone := addTestSession(t, home, filepath.Join(home, "gone"), "filed-away")
	if stdout, stderr, code := runAgentDeck(t, home, "session", "archive", gone); code != 0 {
		t.Fatalf("archive failed (exit %d): %s %s", code, stdout, stderr)
	}
	want := listStatusOf(t, home, live)

	stdout, stderr, code := runAgentDeck(t, home, "list")
	if code != 0 {
		t.Fatalf("list failed (exit %d): %s %s", code, stdout, stderr)
	}
	var header, liveRow, goneRow string
	for _, ln := range strings.Split(stdout, "\n") {
		switch {
		case strings.HasPrefix(ln, "TITLE"):
			header = ln
		case strings.HasPrefix(ln, "live-one"):
			liveRow = ln
		case strings.HasPrefix(ln, "filed-away"):
			goneRow = ln
		}
	}
	if !strings.Contains(header, "STATUS") {
		t.Fatalf("list header has no STATUS column: %q\n%s", header, stdout)
	}
	if f := strings.Fields(liveRow); len(f) < 2 || f[1] != want {
		t.Errorf("live row %q: status cell want %q (list --json)", liveRow, want)
	}
	if f := strings.Fields(goneRow); len(f) < 2 || f[1] != "archived" {
		t.Errorf("archived row %q is not marked archived", goneRow)
	}
}
