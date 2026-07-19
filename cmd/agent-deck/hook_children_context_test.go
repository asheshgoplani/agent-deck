package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatChildrenContext(t *testing.T) {
	t.Run("no children yields empty", func(t *testing.T) {
		if got := formatChildrenContext(nil); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("all running yields header only", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "lint-a", Status: "running"},
			{ID: "b2", Title: "lint-b", Status: "running"},
		}
		got := formatChildrenContext(rows)
		if !strings.Contains(got, "2 children") || !strings.Contains(got, "2 running") {
			t.Errorf("missing counts in %q", got)
		}
		if strings.Contains(got, "\n- ") {
			t.Errorf("no bullets expected for running-only fleet: %q", got)
		}
	})

	t.Run("waiting child gets an actionable bullet", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "lint-a", Status: "waiting"},
		}
		got := formatChildrenContext(rows)
		if !strings.Contains(got, "waiting on input: lint-a") {
			t.Errorf("missing waiting bullet in %q", got)
		}
		if !strings.Contains(got, "session output a1") || !strings.Contains(got, "session send a1") {
			t.Errorf("missing next-step commands in %q", got)
		}
	})

	t.Run("done child shows sentinel status and summary", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "lint-a", Status: "idle", DoneStatus: "fail", DoneSummary: "tests broke"},
		}
		got := formatChildrenContext(rows)
		if !strings.Contains(got, "completed: lint-a") || !strings.Contains(got, "fail") || !strings.Contains(got, "tests broke") {
			t.Errorf("missing done details in %q", got)
		}
		if !strings.Contains(got, "session output a1 --json") {
			t.Errorf("collect command should target the ID in %q", got)
		}
	})

	// The bullets are injected into an agent's context as runnable commands, so
	// they must target the ID: a title is a display string that may contain
	// spaces (and may repeat across sessions under allow_multiple), which would
	// make the command parse as the wrong session or fail outright.
	t.Run("commands target the id, not a title with spaces", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "fix the flaky test", Status: "waiting"},
			{ID: "b2", Title: "ship it", Status: "idle", DoneStatus: "ok"},
		}
		got := formatChildrenContext(rows)
		for _, want := range []string{
			"session output a1 --json", "session send a1", "session output b2 --json",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
		for _, unwanted := range []string{
			"session output fix the flaky test", "session send fix the flaky test",
			"session output ship it",
		} {
			if strings.Contains(got, unwanted) {
				t.Errorf("command built from a title with spaces: %q in %q", unwanted, got)
			}
		}
		// The titles still name the children for the reader.
		if !strings.Contains(got, "waiting on input: fix the flaky test") ||
			!strings.Contains(got, "completed: ship it") {
			t.Errorf("titles should remain as display names in %q", got)
		}
	})

	t.Run("mixed fleet counts each state", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "a", Status: "running"},
			{ID: "b2", Title: "b", Status: "waiting"},
			{ID: "c3", Title: "c", Status: "idle", DoneStatus: "ok", DoneSummary: "done"},
		}
		got := formatChildrenContext(rows)
		for _, want := range []string{"3 children", "1 running", "1 waiting", "1 done"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
		if strings.Contains(got, "stopped/other") {
			t.Errorf("no other bucket expected in %q", got)
		}
	})

	t.Run("stopped children surface in the other bucket", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "a", Status: "stopped"},
			{ID: "b2", Title: "b", Status: "running"},
		}
		got := formatChildrenContext(rows)
		if !strings.Contains(got, "1 stopped/other") {
			t.Errorf("missing other bucket in %q", got)
		}
	})

	t.Run("bullet lists are capped", func(t *testing.T) {
		var rows []childRow
		for i := 0; i < 12; i++ {
			rows = append(rows, childRow{ID: string(rune('a' + i)), Title: "w", Status: "waiting"})
		}
		got := formatChildrenContext(rows)
		if strings.Count(got, "waiting on input:") > maxContextBullets {
			t.Errorf("expected at most %d waiting bullets: %q", maxContextBullets, got)
		}
		if !strings.Contains(got, "more") {
			t.Errorf("expected overflow marker in %q", got)
		}
	})
}

func TestClaudeContextEventName(t *testing.T) {
	cases := map[string]string{
		"UserPromptSubmit": "UserPromptSubmit",
		"SessionStart":     "SessionStart",
		"Stop":             "",
		"PreToolUse":       "",
		"before_agent":     "",
	}
	for in, want := range cases {
		if got := claudeContextEventName(in); got != want {
			t.Errorf("claudeContextEventName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChildrenContextJSON(t *testing.T) {
	out := childrenContextJSON("UserPromptSubmit", "fleet: 1 running")
	var m map[string]map[string]string
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	hso := m["hookSpecificOutput"]
	if hso["hookEventName"] != "UserPromptSubmit" {
		t.Errorf("wrong hookEventName: %q", out)
	}
	if hso["additionalContext"] != "fleet: 1 running" {
		t.Errorf("wrong additionalContext: %q", out)
	}
}
