package main

import (
	"flag"
	"reflect"
	"testing"
)

// reorderGroupArgs used to hoist flags ahead of positionals using a HARDCODED
// list of which flags take a value. --max-concurrent was never on that list, so
// `group update <name> --max-concurrent 12` was rewritten to
// `--max-concurrent <name> 12` — the group name became the flag's value, the
// int parse failed, and the command died printing its own usage block, one that
// advertises the very form that does not work
// ("agent-deck group update mobile --max-concurrent 2").
//
// The allowlist is the defect: any flag added later is silently broken in its
// space-separated form until someone remembers to update a map in a different
// function. normalizeArgs derives the same information from the FlagSet itself,
// so it cannot drift. These tests pin the parse result, not the reordering.

// parseGroupUpdateArgs mirrors handleGroupUpdate's parsing prologue.
func parseGroupUpdateArgs(t *testing.T, args []string) (name string, maxConcurrent int, defaultPath string, err error) {
	t.Helper()
	fs := flag.NewFlagSet("group update", flag.ContinueOnError)
	fs.SetOutput(discardWriter{})
	dp := fs.String("default-path", "", "")
	fs.Bool("clear-default-path", false, "")
	mc := fs.Int("max-concurrent", -1, "")
	fs.Bool("json", false, "")
	fs.Bool("quiet", false, "")
	fs.Bool("q", false, "")

	if err := fs.Parse(normalizeArgs(fs, reorderGroupArgs(args))); err != nil {
		return "", 0, "", err
	}
	return fs.Arg(0), *mc, *dp, nil
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestGroupUpdate_SpaceSeparatedMaxConcurrent(t *testing.T) {
	name, max, _, err := parseGroupUpdateArgs(t, []string{"mobile", "--max-concurrent", "12"})
	if err != nil {
		t.Fatalf("the space form must parse (its own usage text documents it): %v", err)
	}
	if name != "mobile" {
		t.Errorf("group name: got %q, want %q", name, "mobile")
	}
	if max != 12 {
		t.Errorf("max_concurrent: got %d, want 12", max)
	}
}

func TestGroupUpdate_EqualsFormStillWorks(t *testing.T) {
	name, max, _, err := parseGroupUpdateArgs(t, []string{"mobile", "--max-concurrent=12"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "mobile" || max != 12 {
		t.Errorf("got name=%q max=%d, want mobile/12", name, max)
	}
}

// Both orderings must agree — a user who puts the flag first is not writing a
// different command.
func TestGroupUpdate_FlagBeforeNameParsesTheSame(t *testing.T) {
	name, max, _, err := parseGroupUpdateArgs(t, []string{"--max-concurrent", "12", "mobile"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "mobile" || max != 12 {
		t.Errorf("got name=%q max=%d, want mobile/12", name, max)
	}
}

// The flags reorderGroupArgs did know about must keep working, including a
// value that itself looks like a positional.
func TestGroupUpdate_SpaceSeparatedDefaultPath(t *testing.T) {
	name, _, path, err := parseGroupUpdateArgs(t, []string{"mobile", "--default-path", "/repo/x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "mobile" || path != "/repo/x" {
		t.Errorf("got name=%q path=%q, want mobile//repo/x", name, path)
	}
}

// A value-taking flag not on the old allowlist, combined with a later
// positional, is the exact shape that mis-parsed.
func TestGroupUpdate_MaxConcurrentThenOtherFlag(t *testing.T) {
	name, max, path, err := parseGroupUpdateArgs(t,
		[]string{"mobile", "--max-concurrent", "4", "--default-path", "/repo/y"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "mobile" || max != 4 || path != "/repo/y" {
		t.Errorf("got name=%q max=%d path=%q, want mobile/4//repo/y", name, max, path)
	}
}

// reorderGroupArgs must not reorder anything normalizeArgs would not: a bare
// positional list passes through untouched.
func TestReorderGroupArgs_PositionalsOnly(t *testing.T) {
	got := reorderGroupArgs([]string{"source", "dest"})
	if want := []string{"source", "dest"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
