package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// `group delete work --force` also deleted subgroup work/sub and reported
// only "Deleted group: work". The refusal and the result now name the
// subgroups that go with it.
func TestGroupDelete_NamesSubgroups(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	run := func(args ...string) (string, string, int) {
		t.Helper()
		return runAgentDeck(t, home, args...)
	}
	for _, args := range [][]string{
		{"group", "create", "work"},
		{"group", "create", "sub", "--parent", "work"},
	} {
		if stdout, stderr, code := run(args...); code != 0 {
			t.Fatalf("%v: exit %d\n%s\n%s", args, code, stdout, stderr)
		}
	}
	if stdout, stderr, code := run("add", t.TempDir(), "-t", "in-sub", "-c", "bash", "-g", "work/sub", "--no-parent", "--json"); code != 0 {
		t.Fatalf("add: exit %d\n%s\n%s", code, stdout, stderr)
	}

	// Without --force the refusal says the subgroup goes too.
	_, stderr, code := run("group", "delete", "work")
	if code == 0 {
		t.Fatal("group delete of a non-empty group without --force: exit 0")
	}
	if !strings.Contains(stderr, "work/sub") {
		t.Errorf("refusal does not name the subgroup that would be deleted:\n%s", stderr)
	}

	stdout, stderr, code := run("group", "delete", "work", "--force")
	if code != 0 {
		t.Fatalf("group delete --force: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "work/sub") {
		t.Errorf("result does not say the subgroup was deleted:\n%s", stdout)
	}

	// --json carries the list.
	for _, args := range [][]string{
		{"group", "create", "team"},
		{"group", "create", "inner", "--parent", "team"},
	} {
		if stdout, stderr, code := run(args...); code != 0 {
			t.Fatalf("%v: exit %d\n%s\n%s", args, code, stdout, stderr)
		}
	}
	stdout, stderr, code = run("group", "delete", "team", "--force", "--json")
	if code != 0 {
		t.Fatalf("group delete --json: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var res struct {
		Subgroups []string `json:"subgroups_deleted"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || len(res.Subgroups) != 1 || res.Subgroups[0] != "team/inner" {
		t.Errorf("want subgroups_deleted [team/inner], got %v (%v)\n%s", res.Subgroups, err, stdout)
	}
}
