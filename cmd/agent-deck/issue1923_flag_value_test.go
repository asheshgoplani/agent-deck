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
