package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #2451: group_sort = "alphabetical" orders the sessions of a group A→Z
// by title, case-insensitively, so a named session is easy to find in a large
// group. Equal titles (ignoring case) keep today's creation order.

func TestUserConfig_GetGroupSort_Alphabetical(t *testing.T) {
	cases := []struct{ in, want string }{
		{"alphabetical", "alphabetical"},
		{"ALPHABETICAL", "creation"}, // exact value only, like "actionable"
		{"alpha", "creation"},
		{"", "creation"},
		{"actionable", "actionable"},
	}
	for _, c := range cases {
		cfg := &UserConfig{GroupSort: c.in}
		if got := cfg.GetGroupSort(); got != c.want {
			t.Errorf("GetGroupSort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGroupSortMode_Alphabetical(t *testing.T) {
	t.Cleanup(func() { SetGroupSortMode("creation") })
	SetGroupSortMode("alphabetical")
	if got := currentGroupSortMode(); got != "alphabetical" {
		t.Fatalf("after set alphabetical = %q, want alphabetical", got)
	}
}

// alphabeticalFixture is a group whose titles mix case and repeat (ignoring
// case). Statuses and recency are arranged so the actionable sort would
// reorder them differently, and the slice is not in Order, so the expected
// result can only come from title then Order.
func alphabeticalFixture() []*Instance {
	now := time.Now()
	mk := func(id, title string, order int, status Status, ago time.Duration) *Instance {
		return &Instance{ID: id, Title: title, GroupPath: "g", Order: order, Status: status, LastAccessedAt: now.Add(-ago)}
	}
	return []*Instance{
		mk("beta-upper", "Beta", 5, StatusError, 0),
		mk("zeta", "zeta", 0, StatusWaiting, time.Minute),
		mk("alpha-lower", "alpha", 3, StatusRunning, time.Hour),
		mk("gamma", "Gamma", 4, StatusIdle, 2*time.Hour),
		mk("alpha-upper", "Alpha", 1, StatusStopped, 3*time.Hour),
		mk("beta-lower", "beta", 2, StatusStopped, 4*time.Hour),
	}
}

var alphabeticalWant = []string{"alpha-upper", "alpha-lower", "beta-lower", "beta-upper", "gamma", "zeta"}

func TestSortInstancesByActionable_AlphabeticalMode(t *testing.T) {
	t.Cleanup(func() { SetGroupSortMode("creation") })
	SetGroupSortMode("alphabetical")

	insts := alphabeticalFixture()
	SortInstancesByActionable(insts)
	if got := ids(insts); !equalStrings(got, alphabeticalWant) {
		t.Fatalf("alphabetical order wrong:\n got  %v\n want %v", got, alphabeticalWant)
	}
}

// The tree constructors and Flatten (what the TUI, web menu and CLI group
// views render) must carry the alphabetical order through unchanged.
func TestGroupTree_AlphabeticalMode_FlattenOrder(t *testing.T) {
	t.Cleanup(func() { SetGroupSortMode("creation") })
	SetGroupSortMode("alphabetical")

	for name, tree := range map[string]*GroupTree{
		"NewGroupTree":           NewGroupTree(alphabeticalFixture()),
		"NewGroupTreeWithGroups": NewGroupTreeWithGroups(alphabeticalFixture(), []*GroupData{{Name: "g", Path: "g", Expanded: true}}),
	} {
		var got []string
		for _, it := range tree.Flatten() {
			if it.Type == ItemTypeSession {
				got = append(got, it.Session.ID)
			}
		}
		if !equalStrings(got, alphabeticalWant) {
			t.Errorf("%s: flattened order wrong:\n got  %v\n want %v", name, got, alphabeticalWant)
		}
	}
}

// Pins and the maestro keep their bands in alphabetical mode; only the normal
// band is ordered by title.
func TestSortInstancesByActionable_AlphabeticalMode_PinsAndMaestro(t *testing.T) {
	t.Cleanup(func() { SetGroupSortMode("creation") })
	SetGroupSortMode("alphabetical")

	maestro := &Instance{ID: "m", Title: "conductor-maestro", Order: 9}
	pinTop := &Instance{ID: "pt", Title: "zzz pinned top", Pin: PinTop, Order: 8}
	pinBottom := &Instance{ID: "pb", Title: "aaa pinned bottom", Pin: PinBottom, Order: 7}
	b := &Instance{ID: "b", Title: "bravo", Order: 0}
	a := &Instance{ID: "a", Title: "Alpha", Order: 1}

	insts := []*Instance{pinBottom, b, pinTop, a, maestro}
	SortInstancesByActionable(insts)
	want := []string{"m", "pt", "a", "b", "pb"}
	if got := ids(insts); !equalStrings(got, want) {
		t.Fatalf("band order wrong:\n got  %v\n want %v", got, want)
	}
}

func TestLoadUserConfig_SetsGroupSortMode_Alphabetical(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	t.Cleanup(func() { SetGroupSortMode("creation") })

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := filepath.Join(agentDeckDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("group_sort = \"alphabetical\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := LoadUserConfig(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := currentGroupSortMode(); got != "alphabetical" {
		t.Fatalf("LoadUserConfig did not apply group_sort: mode = %q, want alphabetical", got)
	}
}
