package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestParseKanbanAttachArgs covers the validator that handleKanbanAttach uses
// before exiting on bad input. Tests the real production logic; the os.Exit
// shell on top of this is too thin to need its own test.
func TestParseKanbanAttachArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "empty", args: []string{}, wantErr: true},
		{name: "one arg", args: []string{"sess"}, wantErr: true},
		{name: "empty session", args: []string{"", "TASK-1"}, wantErr: true},
		{name: "empty task", args: []string{"sess", ""}, wantErr: true},
		{name: "valid", args: []string{"sess", "TASK-1"}, wantErr: false},
		{name: "extra args ignored", args: []string{"sess", "TASK-1", "extra"}, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionID, taskID, err := parseKanbanAttachArgs(tc.args)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for args=%v, got sessionID=%q taskID=%q", tc.args, sessionID, taskID)
			}
			if !tc.wantErr {
				if err != nil {
					t.Errorf("unexpected error for args=%v: %v", tc.args, err)
				}
				if sessionID != tc.args[0] || taskID != tc.args[1] {
					t.Errorf("got sessionID=%q taskID=%q, want %q %q", sessionID, taskID, tc.args[0], tc.args[1])
				}
			}
		})
	}
}

// TestParseTaskIDFromJSON verifies that parseTaskIDFromJSON handles various
// JSON shapes produced by `hermes kanban create --json`.
func TestParseTaskIDFromJSON(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "id field",
			input:  `{"id":"TASK-42","title":"Fix bug"}`,
			expect: "TASK-42",
		},
		{
			name:   "task_id field",
			input:  `{"task_id":"TASK-99","status":"running"}`,
			expect: "TASK-99",
		},
		{
			name:   "taskId camelCase",
			input:  `{"taskId":"TASK-7"}`,
			expect: "TASK-7",
		},
		{
			name:   "empty json",
			input:  `{}`,
			expect: "",
		},
		{
			name:   "invalid json",
			input:  `not-json`,
			expect: "",
		},
		{
			name:   "no id field",
			input:  `{"title":"no id here"}`,
			expect: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTaskIDFromJSON([]byte(tc.input))
			if got != tc.expect {
				t.Errorf("parseTaskIDFromJSON(%q) = %q, want %q", tc.input, got, tc.expect)
			}
		})
	}
}

// TestExtractKanbanProfileFlag checks that -p / --profile flags are stripped
// and returned without touching other args.
func TestExtractKanbanProfileFlag(t *testing.T) {
	args := []string{"-p", "myprofile", "--status", "done", "extra"}
	remaining, profile := extractKanbanProfileFlag(args)
	if profile != "myprofile" {
		t.Errorf("profile = %q, want %q", profile, "myprofile")
	}
	// remaining must be ["--status", "done", "extra"] — 3 args, not 2
	if len(remaining) != 3 || remaining[0] != "--status" || remaining[1] != "done" || remaining[2] != "extra" {
		t.Fatalf("remaining = %v, want [--status done extra]", remaining)
	}
	// Must not contain -p or myprofile
	for _, r := range remaining {
		if r == "-p" || r == "myprofile" {
			t.Errorf("remaining still contains profile flag/value: %v", remaining)
		}
	}
}

// TestExtractKanbanStatusFlag checks --status extraction.
func TestExtractKanbanStatusFlag(t *testing.T) {
	args := []string{"--status", "done,running", "--other", "val"}
	remaining, status := extractKanbanStatusFlag(args)
	if status != "done,running" {
		t.Errorf("status = %q, want %q", status, "done,running")
	}
	for _, r := range remaining {
		if r == "--status" || r == "done,running" {
			t.Errorf("remaining still contains status flag/value: %v", remaining)
		}
	}
}

// TestExtractKanbanStatusFlag_MultipleFlags verifies that multiple --status flags
// are all collected and joined. With the old single-value implementation this test
// would fail because the second flag overwrote the first.
func TestExtractKanbanStatusFlag_MultipleFlags(t *testing.T) {
	args := []string{"--status", "running", "--status", "blocked", "--other", "val"}
	remaining, status := extractKanbanStatusFlag(args)
	if status != "running,blocked" {
		t.Errorf("status = %q, want %q", status, "running,blocked")
	}
	for _, r := range remaining {
		if r == "--status" || r == "running" || r == "blocked" {
			t.Errorf("remaining still contains status flag/value: %v", remaining)
		}
	}
	if len(remaining) != 2 {
		t.Errorf("expected 2 remaining args, got %d: %v", len(remaining), remaining)
	}
}

// TestExtractKanbanStatusFlag_EqualForm verifies --status=value syntax with multiple flags.
func TestExtractKanbanStatusFlag_EqualForm(t *testing.T) {
	args := []string{"--status=running", "--status=blocked"}
	_, status := extractKanbanStatusFlag(args)
	if status != "running,blocked" {
		t.Errorf("status = %q, want %q", status, "running,blocked")
	}
}

// TestExtractKanbanStatusFlag_Empty verifies no --status flag returns empty string.
func TestExtractKanbanStatusFlag_Empty(t *testing.T) {
	args := []string{"--other", "val"}
	remaining, status := extractKanbanStatusFlag(args)
	if status != "" {
		t.Errorf("expected empty status, got %q", status)
	}
	if len(remaining) != 2 {
		t.Errorf("expected 2 remaining args, got %d", len(remaining))
	}
}

// TestExtractKanbanProfileFlag_NextArgIsFlag verifies that a following flag is not consumed as the value.
func TestExtractKanbanProfileFlag_NextArgIsFlag(t *testing.T) {
	args := []string{"--profile", "--status", "blocked"}
	remaining, profileVal := extractKanbanProfileFlag(args)
	if profileVal != "" {
		t.Errorf("profile should be empty when next arg is a flag, got %q", profileVal)
	}
	if len(remaining) != 2 || remaining[0] != "--status" || remaining[1] != "blocked" {
		t.Errorf("remaining = %v, want [--status blocked]", remaining)
	}
}

// TestExtractKanbanStatusFlag_NextArgIsFlag verifies that a following flag is not consumed as the value.
func TestExtractKanbanStatusFlag_NextArgIsFlag(t *testing.T) {
	args := []string{"--status", "--other", "val"}
	remaining, status := extractKanbanStatusFlag(args)
	if status != "" {
		t.Errorf("status should be empty when next arg is a flag, got %q", status)
	}
	if len(remaining) != 2 || remaining[0] != "--other" || remaining[1] != "val" {
		t.Errorf("remaining = %v, want [--other val]", remaining)
	}
}

// TestExtractKanbanSessionFlag_NextArgIsFlag verifies that a following flag is not consumed as the value.
// session must be non-nil (flag was present) with empty value (malformed).
func TestExtractKanbanSessionFlag_NextArgIsFlag(t *testing.T) {
	args := []string{"Title", "--session", "--body", "desc"}
	remaining, session := extractKanbanSessionFlag(args)
	if session == nil {
		t.Fatal("session should be non-nil when --session token was present")
	}
	if *session != "" {
		t.Errorf("session should be empty when next arg is a flag, got %q", *session)
	}
	want := []string{"Title", "--body", "desc"}
	if len(remaining) != len(want) {
		t.Errorf("remaining = %v, want %v", remaining, want)
	} else {
		for i, v := range want {
			if remaining[i] != v {
				t.Errorf("remaining[%d] = %q, want %q (full: %v)", i, remaining[i], v, remaining)
				break
			}
		}
	}
}

// TestExtractKanbanSessionFlag_Absent verifies that a missing --session flag
// returns session == nil (not an empty string).
func TestExtractKanbanSessionFlag_Absent(t *testing.T) {
	args := []string{"Title", "--body", "desc"}
	_, session := extractKanbanSessionFlag(args)
	if session != nil {
		t.Errorf("session should be nil when --session absent, got %q", *session)
	}
}

// TestPrintKanbanHelp_WritesToWriter verifies that printKanbanHelp writes
// to the provided io.Writer instead of stdout, so error-path callers can route
// output to stderr.
func TestPrintKanbanHelp_WritesToWriter(t *testing.T) {
	var buf bytes.Buffer
	printKanbanHelp(&buf)
	out := buf.String()
	if !strings.Contains(out, "Usage: agent-deck kanban <command>") {
		t.Errorf("help output missing usage line; got: %q", out)
	}
	// Spot-check a couple of verbs are listed so future refactors don't
	// silently truncate the help text.
	for _, verb := range []string{"list", "create", "attach"} {
		if !strings.Contains(out, verb) {
			t.Errorf("help output missing verb %q; got: %q", verb, out)
		}
	}
}

// TestExtractKanbanSessionFlag checks --session extraction.
func TestExtractKanbanSessionFlag(t *testing.T) {
	args := []string{"My Title", "--session", "my-session", "--body", "desc"}
	remaining, session := extractKanbanSessionFlag(args)
	if session == nil {
		t.Fatal("session should be non-nil")
	}
	if *session != "my-session" {
		t.Errorf("session = %q, want %q", *session, "my-session")
	}
	for _, r := range remaining {
		if r == "--session" || r == "my-session" {
			t.Errorf("remaining still contains session flag/value: %v", remaining)
		}
	}
	// "My Title" and "--body" and "desc" should still be present.
	joined := bytes.Join(func() [][]byte {
		var out [][]byte
		for _, r := range remaining {
			out = append(out, []byte(r))
		}
		return out
	}(), []byte(","))
	if !bytes.Contains(joined, []byte("My Title")) {
		t.Errorf("remaining should contain 'My Title', got: %v", remaining)
	}
}
