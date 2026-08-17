package main

import (
	"flag"
	"strings"
	"testing"
)

// issue1923FlagSet mirrors the shape of `add`'s flags: a mix of value-taking
// and boolean, including the short -q that the reported command used.
func issue1923FlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.String("account", "", "")
	fs.String("t", "", "")
	fs.String("wrapper", "", "")
	// #1928: extra-arg and model must be registered for the pass-through case
	// to be exercised at all — an unregistered flag is skipped by the guard, so
	// without these the P2 regression test would pass vacuously.
	fs.String("extra-arg", "", "")
	fs.String("model", "", "")
	fs.Bool("q", false, "")
	fs.Bool("sandbox", false, "")
	return fs
}

// The bug (#1923): with its value omitted, --account binds the FOLLOWING flag
// as its value. Both effects are silent — the account is stored verbatim and
// -q never takes effect.
func TestIssue1923_FlagValueSwallowsNextFlag(t *testing.T) {
	fs := issue1923FlagSet()
	args := []string{"--account", "-q", "/path"}

	// Establish that this is what the parser really does, so the guard below
	// is pinned to observed behaviour rather than to an assumption about it.
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := fs.Lookup("account").Value.String(); got != "-q" {
		t.Fatalf("precondition changed: account = %q, want %q", got, "-q")
	}
	if fs.Lookup("q").Value.String() != "false" {
		t.Fatalf("precondition changed: -q should have been swallowed")
	}

	err := checkFlagValueNotFlag(issue1923FlagSet(), args)
	if err == nil {
		t.Fatal("checkFlagValueNotFlag accepted --account with the next flag as its value")
	}
	for _, want := range []string{"-account", "-q"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q so the user can see what to fix", err, want)
		}
	}
}

// The guard must not fire on correct usage, including the exact command from
// the issue, which parses fine once --account has its value.
func TestIssue1923_GuardAcceptsValidUsage(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"reported command with the value present", []string{"/p", "-t", "title", "--account", "work", "-q"}},
		{"bool flags adjacent", []string{"-q", "--sandbox", "/p"}},
		{"value that merely starts with a dash", []string{"--wrapper", "-custom-thing", "/p"}},
		{"explicit = form is always the user's choice", []string{"--account=-q", "/p"}},
		{"unknown flag is flag.Parse's business, not ours", []string{"--nope", "-q", "/p"}},
		{"trailing flag with no next token", []string{"/p", "--account"}},
		{"everything after -- is positional", []string{"--", "--account", "-q"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkFlagValueNotFlag(issue1923FlagSet(), tc.args); err != nil {
				t.Errorf("rejected valid args %v: %v", tc.args, err)
			}
		})
	}
}

// #1928: the first version of this guard policed EVERY value-taking flag and
// ran after reorderArgsForFlagParsing. Both choices produced false rejections of
// valid commands. These pin the two reported shapes and the original #1923 case
// together, because the fix has to keep all three true at once.
func TestIssue1928_ValidCommandsAreNotRejected(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{
			// P1. reorderArgsForFlagParsing did not know --account takes a
			// value, so it moved "work" away as a positional and left
			// --account sitting next to -q — a pair the user never wrote.
			"account with its value, followed by a bool flag",
			[]string{"add", ".", "-t", "title", "--account", "work", "-q"},
		},
		{
			// P2. --extra-arg's whole purpose is passing flags through, so a
			// flag-shaped value is correct usage, not an omitted value.
			"documented --extra-arg pass-through of a registered flag name",
			[]string{"add", ".", "-t", "t2", "--extra-arg", "--model", "--extra-arg", "opus"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkFlagValueNotFlag(issue1923FlagSet(), tc.args); err != nil {
				t.Errorf("valid command rejected (#1928): %v\nargs: %v", err, tc.args)
			}
		})
	}
}

// Pins WHY the guard runs before reordering rather than after. Reordering the
// P1 command separates --account from "work"; a guard reading that output sees
// two adjacent flags and reports a mistake the user never made. Checking the
// original argv is what makes the guard honest.
func TestIssue1928_GuardMustRunBeforeReorder(t *testing.T) {
	original := []string{".", "-t", "title", "--account", "work", "-q"}

	if err := checkFlagValueNotFlag(issue1923FlagSet(), original); err != nil {
		t.Fatalf("guard rejected the original argv: %v", err)
	}

	// Simulate the pre-fix call order on a reorderer that does not know
	// --account takes a value, which is the state main was in.
	naive := []string{"-t", "title", "--account", "-q", ".", "work"}
	if err := checkFlagValueNotFlag(issue1923FlagSet(), naive); err == nil {
		t.Fatal("expected the reordered form to trip the guard — if it does not, " +
			"this test no longer demonstrates why order matters")
	}
}

// The #1923 protection must survive the #1928 narrowing: --account with its
// value omitted still swallows the next flag, and that is still reported.
func TestIssue1928_OriginalCaseStillRejected(t *testing.T) {
	err := checkFlagValueNotFlag(issue1923FlagSet(), []string{"add", ".", "--account", "-q"})
	if err == nil {
		t.Fatal("#1923 regression: --account with no value no longer reports swallowing -q")
	}
	if !strings.Contains(err.Error(), "-q") {
		t.Errorf("error should still name the swallowed flag: %v", err)
	}
}

// The root cause of P1: reorderArgsForFlagParsing keeps a flag and its value
// together only for flags it knows take one. --account was missing from that
// map, which separated them before any guard ran — a mis-parse that predates
// the guard and would have mangled --account on its own.
func TestIssue1928_ReorderKeepsAccountValue(t *testing.T) {
	got := reorderArgsForFlagParsing([]string{".", "-t", "title", "--account", "work", "-q"})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--account work") {
		t.Errorf("reorder separated --account from its value: %v", got)
	}
	if got[len(got)-1] != "." {
		t.Errorf("positional path should be last, got %v", got)
	}
}
