package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real-binary smoke test for the CLI features that had no end-to-end test in
// the 2026-11 feature sweep (inventory rows with tests = none in the areas
// cli-sessions, cli-comms-and-ops and consistency). One row per feature: its
// help exits 0, the basic happy path prints what the help documents, and
// --json parses. It is a tripwire that each command still runs, not a spec:
// it asserts nothing about behaviour that has an open finding. Those are
// listed as skipped rows that name the finding, so the table stays the full
// list and a row is switched on when its finding is fixed.
//
// Everything runs against stored sessions in a throwaway profile (HOME, XDG
// dirs and tmux socket dir of its own). No session is started, so no tmux
// server exists and the whole table takes a few seconds.

// smokeTitles are the stored sessions every row can use, in creation order.
var smokeTitles = []string{"smoke-a", "smoke-b", "smoke-c"}

// smokeNotFoundCommands take a session id or title; each is run with a name
// no session has. The sweep row ran 26; left out here: switch (searches
// fuzzily by design, switch_cmd_test.go), open --session (open_cmd_test.go),
// session current (takes no name), session send-status (takes a send id),
// session command and session interrupt (not session-name lookups first).
var smokeNotFoundCommands = [][]string{
	{"session", "show"},
	{"session", "start"},
	{"session", "stop"},
	{"session", "restart"},
	{"session", "archive"},
	{"session", "unarchive"},
	{"session", "remove"},
	{"session", "output"},
	{"session", "viewers"},
	{"session", "children"},
	{"session", "focus"},
	{"session", "fork"},
	{"session", "send", "--no-wait", "<name>", "hello"},
	{"session", "set", "<name>", "title", "other"},
	{"rename", "<name>", "other"},
	{"remove"},
	{"group", "move", "<name>", "some-group"},
	{"mcp", "attached"},
	{"skill", "attached"},
	{"worktree", "info"},
}

type smokeRun struct {
	out, errOut string
	code        int
}

type smokeEnv struct {
	*issue2530Env
}

// run runs one command on the smoke profile; it never fails the test itself.
func (e *smokeEnv) run(args ...string) smokeRun {
	e.t.Helper()
	cmd := exec.Command(e.bin, append([]string{"-p", issue2530Profile}, args...)...)
	cmd.Env = e.env
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	r := smokeRun{}
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			e.t.Fatalf("agent-deck %s: %v", strings.Join(args, " "), err)
		}
		r.code = exitErr.ExitCode()
	}
	r.out, r.errOut = out.String(), errb.String()
	return r
}

// ok runs a command that must exit 0 and returns its stdout.
func (e *smokeEnv) ok(t *testing.T, args ...string) string {
	t.Helper()
	r := e.run(args...)
	if r.code != 0 {
		t.Fatalf("agent-deck %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.out, r.errOut)
	}
	return r.out
}

// helpOK checks that a command's --help exits 0 and prints something.
func (e *smokeEnv) helpOK(t *testing.T, args ...string) string {
	t.Helper()
	r := e.run(append(args, "--help")...)
	text := r.out + r.errOut
	if r.code != 0 || strings.TrimSpace(text) == "" {
		t.Fatalf("agent-deck %s --help: exit %d, output %q", strings.Join(args, " "), r.code, text)
	}
	return text
}

// jsonOK runs a command that must exit 0 and decodes its stdout into v.
func (e *smokeEnv) jsonOK(t *testing.T, v any, args ...string) {
	t.Helper()
	out := e.ok(t, args...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("agent-deck %s: stdout is not the expected JSON: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func wantContains(t *testing.T, what, text string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(text, sub) {
			t.Errorf("%s does not contain %q:\n%s", what, sub, text)
		}
	}
}

func TestCLISmokeE2E_CommandsWithoutEndToEndTests(t *testing.T) {
	e := &smokeEnv{newIssue2530Env(t)}
	t.Cleanup(e.killDetached)
	// `usage` fetches a provider's quota when the caller's environment
	// holds that provider's endpoint and token. The smoke run must not send
	// anything anywhere, so no provider variable reaches the binary.
	env := e.env[:0:0]
	for _, kv := range e.env {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "ZAI_") {
			continue
		}
		env = append(env, kv)
	}
	e.env = env
	start := time.Now()
	for _, title := range smokeTitles {
		dir := filepath.Join(e.root, "proj", title)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		e.ok(t, "add", dir, "-t", title, "-c", "bash")
	}

	rows := []struct {
		feature string // the inventory row
		area    string
		skip    string // why the row is not run; names the finding
		check   func(t *testing.T)
	}{
		{
			feature: "version (--json)",
			area:    "cli-sessions",
			check: func(t *testing.T) {
				e.helpOK(t, "version")
				text := e.ok(t, "version")
				wantContains(t, "version", text, "Agent Deck v")
				var v struct {
					Version string `json:"version"`
				}
				e.jsonOK(t, &v, "version", "--json")
				if v.Version == "" {
					t.Fatal("version --json has no version")
				}
				wantContains(t, "version", text, v.Version)
			},
		},
		{
			feature: "session viewers",
			area:    "cli-sessions",
			check: func(t *testing.T) {
				wantContains(t, "session viewers --help", e.helpOK(t, "session", "viewers"), "Usage: agent-deck session viewers")
				// No terminal is attached to a stored session.
				wantContains(t, "session viewers", e.ok(t, "session", "viewers", "smoke-a"), "Session: smoke-a", "Viewers: none")
				var v struct {
					ID      string            `json:"id"`
					Title   string            `json:"title"`
					Viewers []json.RawMessage `json:"viewers"`
				}
				e.jsonOK(t, &v, "session", "viewers", "--json", "smoke-a")
				if v.ID == "" || v.Title != "smoke-a" || v.Viewers == nil || len(v.Viewers) != 0 {
					t.Errorf("session viewers --json = %+v, want id, title smoke-a and an empty viewers list", v)
				}
			},
		},
		{
			feature: "Session list order (list, session recent)",
			area:    "consistency",
			check: func(t *testing.T) {
				wantContains(t, "list --help", e.helpOK(t, "list"), "Usage: agent-deck list")
				wantContains(t, "session recent --help", e.helpOK(t, "session", "recent"), "Usage: agent-deck session recent")
				// list: creation order, in text and in JSON.
				var listed []struct {
					Title string `json:"title"`
				}
				e.jsonOK(t, &listed, "list", "--json")
				var titles []string
				for _, s := range listed {
					titles = append(titles, s.Title)
				}
				if strings.Join(titles, ",") != strings.Join(smokeTitles, ",") {
					t.Errorf("list --json order = %v, want creation order %v", titles, smokeTitles)
				}
				text := e.ok(t, "list")
				last := -1
				for _, title := range smokeTitles {
					at := strings.Index(text, title)
					if at < 0 || at < last {
						t.Errorf("list does not show %v in creation order:\n%s", smokeTitles, text)
						break
					}
					last = at
				}
				// session recent: sessions that were never attached are left out.
				wantContains(t, "session recent", e.ok(t, "session", "recent"), "No recently used sessions")
				var recent []json.RawMessage
				e.jsonOK(t, &recent, "session", "recent", "--json")
				if len(recent) != 0 {
					t.Errorf("session recent --json lists %d sessions, want none before any attach", len(recent))
				}
			},
		},
		{
			feature: "Not-found error wording, 26 subcommands",
			area:    "consistency",
			// The wording and the exit code are being unified
			// (consistency:C9), so only what holds before and after is
			// checked: an unknown session is an error, never a success.
			check: func(t *testing.T) {
				const missing = "no-such-session-smoke"
				for _, tmpl := range smokeNotFoundCommands {
					args, placed := []string{}, false
					for _, a := range tmpl {
						if a == "<name>" {
							a, placed = missing, true
						}
						args = append(args, a)
					}
					if !placed {
						args = append(args, missing)
					}
					r := e.run(args...)
					if r.code == 0 {
						t.Errorf("agent-deck %s: exit 0 for a session that does not exist\nstdout: %s", strings.Join(args, " "), r.out)
					}
					if strings.TrimSpace(r.out+r.errOut) == "" {
						t.Errorf("agent-deck %s: exit %d with no message", strings.Join(args, " "), r.code)
					}
				}
				// With --json the error is a JSON object on stdout.
				r := e.run("session", "show", "--json", missing)
				var v struct {
					Success *bool  `json:"success"`
					Error   string `json:"error"`
				}
				if err := json.Unmarshal([]byte(r.out), &v); err != nil || v.Success == nil || *v.Success || v.Error == "" {
					t.Errorf("session show --json for an unknown session: exit %d, want a JSON error object with success=false, got (%v):\n%s", r.code, err, r.out)
				}
			},
		},
		{
			feature: "Not-found error wording, 26 subcommands: one wording and one exit code",
			area:    "consistency",
			skip:    "consistency:C9 (voice-glasses:F1): open --session, session focus, rename and remove word not-found differently and worktree info / open exit 1 where the rest exit 2",
		},
		{
			feature: "Time/age format",
			area:    "consistency",
			// The two commands of the row run and their JSON parses. How an
			// age is worded is the skipped row below.
			check: func(t *testing.T) {
				wantContains(t, "status --help", e.helpOK(t, "status"), "-stale")
				if strings.TrimSpace(e.ok(t, "status", "--stale")) == "" {
					t.Error("status --stale printed nothing")
				}
				var stale struct {
					ThresholdSeconds *int64             `json:"threshold_seconds"`
					Candidates       *[]json.RawMessage `json:"candidates"`
				}
				e.jsonOK(t, &stale, "status", "--stale", "--json")
				if stale.ThresholdSeconds == nil || stale.Candidates == nil {
					t.Errorf("status --stale --json lacks threshold_seconds or candidates")
				}
				wantContains(t, "usage --help", e.helpOK(t, "usage"), "Usage: agent-deck usage")
				if strings.TrimSpace(e.ok(t, "usage")) == "" {
					t.Error("usage printed nothing")
				}
				var usage struct {
					Providers *[]json.RawMessage `json:"providers"`
				}
				e.jsonOK(t, &usage, "usage", "--json")
				if usage.Providers == nil {
					t.Error("usage --json lacks providers")
				}
			},
		},
		{
			feature: "Time/age format: one age wording",
			area:    "consistency",
			skip:    "consistency:C12: status --stale prints 'last-active 2m0s ago' where the TUI and web print '2m ago'",
		},
		{
			feature: "Group name with a space",
			area:    "consistency",
			skip:    "consistency:C2: add -g with a space in the group name makes two groups with the same name",
		},
		{
			feature: "Open a file/page from a session (TUI, web, phone app)",
			area:    "consistency, cli-comms-and-ops",
			skip:    "consistency:C13 (cli-comms:F4): the feature exists only as CLI -> Mac app; the CLI side is covered by open_cmd_test.go",
		},
	}

	for _, row := range rows {
		t.Run(row.feature, func(t *testing.T) {
			if row.skip != "" {
				t.Skipf("%s: not asserted while its finding is open: %s", row.area, row.skip)
			}
			row.check(t)
		})
	}
	if took, limit := time.Since(start), 60*time.Second; took > limit {
		t.Errorf("the smoke table took %v, want under %v", took.Round(time.Millisecond), limit)
	}
}
