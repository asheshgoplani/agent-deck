//go:build capability_e2e

package capability

import (
	"encoding/json"
	"strings"
	"testing"
)

// listProfile returns the registry rows for a specific profile via
// `-p <profile> list --json`. An empty profile uses the default (same as the
// Wave 1 list helper).
func (c *capSandbox) listProfile(t *testing.T, profile string) []sessionRow {
	t.Helper()
	args := []string{}
	if profile != "" {
		args = append(args, "-p", profile)
	}
	args = append(args, "list", "--json")
	out := strings.TrimSpace(c.run(t, args...))
	if out == "" || out == "null" {
		return nil
	}
	var rows []sessionRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("parse list --json (profile %q): %v\nraw: %s", profile, err, out)
	}
	return rows
}

// titlesInGroup returns the titles of rows whose Group matches want.
func titlesInGroup(rows []sessionRow, want string) []string {
	var out []string
	for _, r := range rows {
		if r.Group == want {
			out = append(out, r.Title)
		}
	}
	return out
}

// TestCapability_Groups_Filtering proves sessions land in the group they were
// created with, and filtering the registry by group returns exactly that
// group's members. The group field is the persisted GroupPath a human sees in
// `list --json` and the TUI tree.
//
// Surfaces: CLI (add -g, list --json) + Persistence (GroupPath on the row).
func TestCapability_Groups_Filtering(t *testing.T) {
	c := newCapSandbox(t)

	c.run(t, "add", "-c", "bash", "-t", "alpha-1", "-g", "alpha", c.WorkDir)
	c.run(t, "add", "-c", "bash", "-t", "alpha-2", "-g", "alpha", c.WorkDir)
	c.run(t, "add", "-c", "bash", "-t", "beta-1", "-g", "beta", c.WorkDir)

	rows := c.list(t)
	if got := titlesInGroup(rows, "alpha"); len(got) != 2 {
		t.Fatalf("filtering by group alpha should return its 2 sessions, got %v\nall rows: %+v", got, rows)
	}
	if got := titlesInGroup(rows, "beta"); len(got) != 1 || got[0] != "beta-1" {
		t.Fatalf("filtering by group beta should return only beta-1, got %v", got)
	}
	// A session in alpha must not bleed into beta's filter.
	for _, r := range rows {
		if r.Title == "beta-1" && r.Group == "alpha" {
			t.Fatalf("beta-1 leaked into group alpha: %+v", r)
		}
	}

	// Display proof: the group view a human checks, showing the per-group
	// session counts the assertions above verified.
	snapshot(t, "groups", c.run(t, "group", "list"))
}

// TestCapability_Profiles_Isolation proves two profiles keep entirely separate
// registries: a session added under one profile is invisible to the other, and
// vice versa. Profile DBs are independent SQLite files, so a leak here would be
// a cross-profile data-bleed bug.
//
// Surfaces: CLI (-p global flag) + Persistence (per-profile state.db isolation).
func TestCapability_Profiles_Isolation(t *testing.T) {
	c := newCapSandbox(t)

	// One session in the default profile, one in an isolated "capalt" profile.
	c.run(t, "add", "-c", "bash", "-t", "in-default", c.WorkDir)
	c.run(t, "-p", "capalt", "add", "-c", "bash", "-t", "in-capalt", c.WorkDir)

	def := c.listProfile(t, "")
	alt := c.listProfile(t, "capalt")

	if _, ok := findTitle(def, "in-default"); !ok {
		t.Fatalf("default profile should contain in-default, rows: %+v", def)
	}
	if _, ok := findTitle(def, "in-capalt"); ok {
		t.Fatalf("default profile must NOT see the capalt session (cross-profile bleed)")
	}
	if _, ok := findTitle(alt, "in-capalt"); !ok {
		t.Fatalf("capalt profile should contain in-capalt, rows: %+v", alt)
	}
	if _, ok := findTitle(alt, "in-default"); ok {
		t.Fatalf("capalt profile must NOT see the default session (cross-profile bleed)")
	}

	// Display proof: the two registries side by side, proving each profile sees
	// only its own session.
	snapshot(t, "profiles", "$ agent-deck list   (default profile)\n"+
		strings.TrimRight(c.run(t, "list"), "\n")+
		"\n\n$ agent-deck -p capalt list   (isolated profile)\n"+
		strings.TrimRight(c.run(t, "-p", "capalt", "list"), "\n"))
}

// findTitle reports whether rows contain a row with the given title.
func findTitle(rows []sessionRow, title string) (sessionRow, bool) {
	for _, r := range rows {
		if r.Title == title {
			return r, true
		}
	}
	return sessionRow{}, false
}
