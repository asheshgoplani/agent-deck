package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Recall phase 1 CLI surface (docs/recall.md): the creation flags, the
// annotate edit set, the remote allowlist and the end-to-end write path.

func TestHintEdits_ParseAndValidate(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	h := registerAnnotateFlags(fs)
	args := []string{
		"--hint", "purpose=fix flaky auth test", "--ticket", "SB-412", "--tag", "auth", "--tag", "flaky",
		"--why", "3rd regression this month", "--set-hint", "ticket=SB-413", "--remove-tag", "flaky", "--unset", "why",
		"--decision", "clock skew", "--outcome", "worked",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.set["ticket"] != "SB-413" || h.set["purpose"] != "fix flaky auth test" || h.set["decision"] != "clock skew" || h.set["outcome"] != "worked" {
		t.Fatalf("set = %v", h.set)
	}
	if !reflect.DeepEqual(h.setOrder, []string{"purpose", "ticket", "why", "decision", "outcome"}) {
		t.Fatalf("setOrder = %v (a repeated key keeps its first position)", h.setOrder)
	}
	if !reflect.DeepEqual(h.addTags, []string{"auth", "flaky"}) || !reflect.DeepEqual(h.removeTags, []string{"flaky"}) || !reflect.DeepEqual(h.unset, []string{"why"}) {
		t.Fatalf("tags/unset = %v %v %v", h.addTags, h.removeTags, h.unset)
	}

	for _, bad := range [][]string{
		{"--hint", "nokeyvalue"},
		{"--hint", "=v"},
		{"--hint", "k="},
		{"--hint", "bad key=v"},
		{"--tag", "has space"},
		{"--tag", "a,b"},
		{"--ticket", strings.Repeat("x", maxHintValueBytes+1)},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(&strings.Builder{})
		registerAnnotateFlags(fs)
		if err := fs.Parse(bad); err == nil {
			t.Errorf("args %v accepted; want a parse error", bad)
		}
	}
}

// add and launch expose the creation subset only, and every one of them is
// value-taking in the remote creation catalog so a forwarded --hint never
// swallows the next token.
func TestCreationCatalog_IncludesRecallHintFlags(t *testing.T) {
	for _, command := range []string{"add", "launch"} {
		got := map[string]bool{}
		for _, f := range creationCommandFields(command) {
			got[f.Name] = f.TakesValue
		}
		for _, name := range []string{"hint", "tag", "ticket", "why"} {
			takesValue, ok := got[name]
			if !ok || !takesValue {
				t.Errorf("%s: flag --%s present=%v takesValue=%v; want present and value-taking", command, name, ok, takesValue)
			}
		}
		for _, name := range []string{"set-hint", "unset", "remove-tag", "decision", "outcome", "note-stdin", "self"} {
			if _, ok := got[name]; ok {
				t.Errorf("%s: annotate-only flag --%s leaked into the creation surface", command, name)
			}
		}
	}
}

// `session annotate` writes state.db rows the server owns, so the remote
// passthrough forwards it like the other session verbs.
func TestRemoteCommandArgsSessionAnnotate(t *testing.T) {
	for _, args := range [][]string{
		{"session", "annotate", "task", "--ticket", "SB-412", "--json"},
		{"session", "annotate", "task", "--note-stdin"},
	} {
		got, err := remoteCommandArgs(args)
		if err != nil || !reflect.DeepEqual(got, args) {
			t.Fatalf("remoteCommandArgs(%v) = %v, %v; want the args unchanged", args, got, err)
		}
	}
	if _, err := remoteCommandArgs([]string{"annotate", "task"}); err == nil {
		t.Fatal("bare annotate is not a command; only the session form is forwarded")
	}
}

func TestFirstLineClipped(t *testing.T) {
	if got := firstLineClipped("\n\n  Fix the auth test  \nsecond line", 200); got != "Fix the auth test" {
		t.Fatalf("got %q", got)
	}
	if got := firstLineClipped(strings.Repeat("a", 10), 4); got != "aaaa" {
		t.Fatalf("clip: got %q", got)
	}
	if got := firstLineClipped("  \n ", 10); got != "" {
		t.Fatalf("blank: got %q", got)
	}
}

// End to end through the built binary: `add` writes the creation hints,
// `session annotate` corrects, unsets and untags them, and `--json` reports
// the resulting state. Runs against an isolated HOME.
func TestSessionAnnotate_EndToEnd(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "auth-fix", "-c", "shell", proj,
		"--hint", "purpose=fix flaky auth test", "--ticket", "SB-412", "--tag", "auth", "--tag", "flaky",
		"--why", "3rd regression this month", "--json")
	if code != 0 {
		t.Fatalf("add exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var added struct {
		ID    string            `json:"id"`
		Hints map[string]string `json:"hints"`
		Tags  []string          `json:"tags"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil {
		t.Fatalf("add json: %v\n%s", err, stdout)
	}
	if added.Hints["ticket"] != "SB-412" || added.Hints["why"] == "" || added.Hints["purpose"] != "fix flaky auth test" {
		t.Fatalf("add hints = %v", added.Hints)
	}
	if !reflect.DeepEqual(added.Tags, []string{"auth", "flaky"}) {
		t.Fatalf("add tags = %v", added.Tags)
	}

	stdout, stderr, code = runAgentDeck(t, home, "session", "annotate", "auth-fix",
		"--set-hint", "ticket=SB-413", "--remove-tag", "flaky", "--unset", "why",
		"--decision", "root cause was clock skew", "--outcome", "worked", "--json")
	if code != 0 {
		t.Fatalf("annotate exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var annotated struct {
		Success bool              `json:"success"`
		Hints   map[string]string `json:"hints"`
		Tags    []string          `json:"tags"`
		Changes []hintChange      `json:"changes"`
	}
	if err := json.Unmarshal([]byte(stdout), &annotated); err != nil {
		t.Fatalf("annotate json: %v\n%s", err, stdout)
	}
	want := map[string]string{"purpose": "fix flaky auth test", "ticket": "SB-413", "decision": "root cause was clock skew", "outcome": "worked"}
	if !annotated.Success || !reflect.DeepEqual(annotated.Hints, want) {
		t.Fatalf("annotate hints = %v; want %v", annotated.Hints, want)
	}
	if !reflect.DeepEqual(annotated.Tags, []string{"auth"}) {
		t.Fatalf("annotate tags = %v; want [auth]", annotated.Tags)
	}
	if len(annotated.Changes) != 5 {
		t.Fatalf("changes = %+v; want 5", annotated.Changes)
	}

	// --note-stdin stores the note; the read-only form (no edits) shows it.
	cmdStdin := "Summary line\nmore detail\n"
	stdout, stderr, code = runAgentDeckStdin(t, home, cmdStdin, "session", "annotate", "auth-fix", "--note-stdin", "--json")
	if code != 0 {
		t.Fatalf("note-stdin exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	stdout, _, code = runAgentDeck(t, home, "session", "annotate", "auth-fix")
	if code != 0 || !strings.Contains(stdout, "note:") || !strings.Contains(stdout, "Summary line") || !strings.Contains(stdout, "ticket:") {
		t.Fatalf("read-only annotate output:\n%s", stdout)
	}

	// Unknown session is a not-found error, exit 2.
	if _, _, code = runAgentDeck(t, home, "session", "annotate", "nope", "--ticket", "X"); code != 2 {
		t.Fatalf("unknown session exit = %d; want 2", code)
	}
}

// add/launch with a parent derive the `parent` hint (source derived); the
// launch path additionally derives `purpose` from the message's first line.
// An explicit --hint always wins over a derived value.
func TestCreationHints_DerivedParentAndPurpose(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "parent", "-c", "shell", proj, "--json")
	if code != 0 {
		t.Fatalf("add parent exit %d: %s %s", code, stdout, stderr)
	}
	var parent struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &parent); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runAgentDeck(t, home, "add", "-t", "child", "-c", "shell", proj, "--parent", "parent", "--json")
	if code != 0 {
		t.Fatalf("add child exit %d: %s %s", code, stdout, stderr)
	}
	var child struct {
		Hints map[string]string `json:"hints"`
	}
	if err := json.Unmarshal([]byte(stdout), &child); err != nil {
		t.Fatal(err)
	}
	if child.Hints["parent"] != parent.ID {
		t.Fatalf("child hints = %v; want parent=%s", child.Hints, parent.ID)
	}

	// The merge rule, in process: explicit beats derived, derived fills gaps,
	// and derived rows carry the derived source.
	storage, err := session.NewStorageWithProfile("_test_recall_creation_hints")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	inst := session.NewInstance("merge", proj)
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	explicit := newHintEdits()
	if err := explicit.setHint(hintKeyPurpose, "typed"); err != nil {
		t.Fatal(err)
	}
	auto := map[string]string{hintKeyPurpose: firstLineClipped("derived line\nrest", derivedPurposeLimit), hintKeyParent: "p-1", hintKeyTicket: ""}
	hints, tags := applyCreationHints(storage, inst, explicit, auto)
	if want := map[string]string{"purpose": "typed", "parent": "p-1"}; !reflect.DeepEqual(hints, want) {
		t.Fatalf("hints = %v; want %v", hints, want)
	}
	if len(tags) != 0 {
		t.Fatalf("tags = %v; want none", tags)
	}
	rows, err := storage.GetDB().ListSessionHints(statedb.HintScopeInstance, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		wantSource := statedb.HintSourceDerived
		if r.Key == hintKeyPurpose {
			wantSource = statedb.HintSourceCLICreate
		}
		if r.Source != wantSource {
			t.Errorf("hint %s source = %s; want %s", r.Key, r.Source, wantSource)
		}
	}
	if got, _ := applyCreationHints(nil, nil, explicit, auto); got != nil {
		t.Fatalf("nil storage must be a no-op, got %v", got)
	}
}
