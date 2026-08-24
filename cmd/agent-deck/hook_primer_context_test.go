package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestLifecycleFromHookSource(t *testing.T) {
	cases := []struct{ in, want string }{
		{"startup", session.LifecycleCreated},
		{"resume", session.LifecycleResumed},
		{"clear", session.LifecycleCreated},
		{"compact", session.LifecycleResumed},
		{"Resume", session.LifecycleResumed},
		{"", ""},
		{"future-source", ""},
	}
	for _, c := range cases {
		if got := lifecycleFromHookSource(c.in); got != c.want {
			t.Errorf("lifecycleFromHookSource(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestChildrenContextJSON_SingleObject pins the merge contract: primer and
// fleet snapshot travel in ONE hookSpecificOutput JSON object. Claude Code
// parses hook stdout as a single JSON value — two Println'd objects would
// corrupt both payloads.
func TestChildrenContextJSON_SingleObject(t *testing.T) {
	combined := strings.Join([]string{"<agent-deck-context>…</agent-deck-context>", "[agent-deck fleet] 2 children"}, "\n\n")
	out := childrenContextJSON("SessionStart", combined)

	var payload map[string]map[string]string
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("hook output is not one JSON object: %v\n%s", err, out)
	}
	got := payload["hookSpecificOutput"]["additionalContext"]
	if !strings.Contains(got, "agent-deck-context") || !strings.Contains(got, "agent-deck fleet") {
		t.Errorf("combined additionalContext lost a part: %q", got)
	}
	if payload["hookSpecificOutput"]["hookEventName"] != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", payload["hookSpecificOutput"]["hookEventName"])
	}
}

// TestPrimerCommandsExist is the drift guard from SESSION-CONTEXT-PLAN.md §6:
// every `agent-deck <command>` the primer recommends must resolve to a real
// handler in this package. Each map value is a compile-time reference to the
// handler, so deleting or renaming a command breaks this test (and the build)
// instead of letting the primer silently recommend a command that no longer
// exists — the never-stale rule, but compiled.
func TestPrimerCommandsExist(t *testing.T) {
	knownInvocations := map[string]any{
		"agent-deck status":           handleStatus,
		"agent-deck session search":   handleSessionSearch,
		"agent-deck session children": handleSessionChildren,
		"agent-deck session output":   handleSessionOutput,
		"agent-deck session send":     handleSessionSend,
		"agent-deck session primer":   handleSessionPrimer,
		"agent-deck launch":           handleLaunch,
	}

	facts := session.PrimerFacts{
		SessionID: "id", Title: "t", Group: "g", Dir: "/d", Host: "local",
		Harness: "claude", Profile: "default", ParentID: "p1",
		Lifecycle: session.LifecycleResumed, Level: session.ContextLevelFull,
	}
	rendered := session.RenderPrimer(facts, session.ContextLevelFull)

	re := regexp.MustCompile(`agent-deck( session)?( [a-z][a-z-]+)`)
	seen := map[string]bool{}
	for _, m := range re.FindAllString(rendered, -1) {
		seen[m] = true
	}
	if len(seen) == 0 {
		t.Fatalf("no agent-deck invocations found in the rendered primer — the extraction regex or the primer broke:\n%s", rendered)
	}
	for invocation := range seen {
		if _, ok := knownInvocations[invocation]; !ok {
			t.Errorf("primer recommends %q, which is not in the compiled command map — either the primer references a nonexistent command, or a new primer line needs a handler reference added here", invocation)
		}
	}
}

// Pins the #928-class reorder trap for the new flag: a value-taking flag
// missing from reorderArgsForFlagParsing's map has its value stripped off as
// a positional. `--account work` hit this once already; `--context-level
// primer` must not.
func TestReorderArgs_ContextLevelKeepsValue(t *testing.T) {
	got := reorderArgsForFlagParsing([]string{"/work/proj", "--context-level", "primer", "--no-assert-done"})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--context-level primer") {
		t.Fatalf("reorder split --context-level from its value: %v", got)
	}
}
