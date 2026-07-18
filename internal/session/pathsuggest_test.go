package session

import (
	"testing"
	"time"
)

func mkInst(path string, last time.Time) *Instance {
	return &Instance{ProjectPath: path, LastAccessedAt: last, CreatedAt: last}
}

// TestPathSuggest_RanksRecentAboveGroupAboveZoxide verifies the source
// hierarchy: a recently-accessed session path outranks a bare group default,
// which outranks a zoxide-only hit, when nothing else distinguishes them.
func TestPathSuggest_RanksRecentAboveGroupAboveZoxide(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	out := PathSuggest(PathSuggestInput{
		Instances:     []*Instance{mkInst("/home/u/recent", now.Add(-1*time.Hour))},
		GroupDefaults: []string{"/home/u/groupdef"},
		Zoxide:        []string{"/home/u/zox"},
		Now:           now,
	})
	if len(out) != 3 {
		t.Fatalf("got %d candidates, want 3: %+v", len(out), out)
	}
	if out[0].Path != "/home/u/recent" || out[0].Source != SourceRecent {
		t.Errorf("rank 0 = %q/%s, want /home/u/recent/recent", out[0].Path, out[0].Source)
	}
	if out[1].Path != "/home/u/groupdef" || out[1].Source != SourceGroup {
		t.Errorf("rank 1 = %q/%s, want /home/u/groupdef/group", out[1].Path, out[1].Source)
	}
	if out[2].Path != "/home/u/zox" || out[2].Source != SourceZoxide {
		t.Errorf("rank 2 = %q/%s, want /home/u/zox/zoxide", out[2].Path, out[2].Source)
	}
}

// TestPathSuggest_RecencyOrdersRecents verifies more-recently-accessed paths
// sort ahead of older ones within the recents source.
func TestPathSuggest_RecencyOrdersRecents(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	out := PathSuggest(PathSuggestInput{
		Instances: []*Instance{
			mkInst("/old", now.Add(-200*time.Hour)),
			mkInst("/fresh", now.Add(-1*time.Hour)),
		},
		Now: now,
	})
	if len(out) != 2 || out[0].Path != "/fresh" || out[1].Path != "/old" {
		t.Fatalf("recency order wrong: %+v", out)
	}
}

// TestPathSuggest_DedupsAcrossSourcesKeepingStrongest verifies a path present
// in several sources appears once, tagged with the strongest (recent) source.
func TestPathSuggest_DedupsAcrossSourcesKeepingStrongest(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	out := PathSuggest(PathSuggestInput{
		Instances:     []*Instance{mkInst("/shared", now.Add(-2*time.Hour))},
		GroupDefaults: []string{"/shared"},
		Zoxide:        []string{"/shared"},
		Now:           now,
	})
	if len(out) != 1 {
		t.Fatalf("got %d, want 1 deduped candidate: %+v", len(out), out)
	}
	if out[0].Source != SourceRecent {
		t.Errorf("deduped source = %s, want recent (strongest)", out[0].Source)
	}
}

// TestPathSuggest_FrequencyBoost verifies that a path backed by multiple
// sessions outranks a single-session path of the same recency.
func TestPathSuggest_FrequencyBoost(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	at := now.Add(-1 * time.Hour)
	out := PathSuggest(PathSuggestInput{
		Instances: []*Instance{
			mkInst("/single", at),
			mkInst("/multi", at),
			mkInst("/multi", at),
			mkInst("/multi", at),
		},
		Now: now,
	})
	if len(out) != 2 || out[0].Path != "/multi" {
		t.Fatalf("frequency boost failed: %+v", out)
	}
}

// TestPathSuggest_QueryFiltersCaseInsensitiveSubstring verifies the query
// narrows candidates by case-insensitive substring on the path.
func TestPathSuggest_QueryFiltersCaseInsensitiveSubstring(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	out := PathSuggest(PathSuggestInput{
		Instances: []*Instance{
			mkInst("/home/u/agent-deck", now.Add(-1*time.Hour)),
			mkInst("/home/u/other", now.Add(-1*time.Hour)),
		},
		Query: "DECK",
		Now:   now,
	})
	if len(out) != 1 || out[0].Path != "/home/u/agent-deck" {
		t.Fatalf("query filter failed: %+v", out)
	}
}

// TestPathSuggest_QueryExemptsZoxideFuzzyHits verifies zoxide candidates
// survive the query filter even when they aren't a literal substring match
// (zoxide already fuzzy-matched), while non-matching recents are still dropped.
func TestPathSuggest_QueryExemptsZoxideFuzzyHits(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	out := PathSuggest(PathSuggestInput{
		Instances: []*Instance{mkInst("/home/u/unrelated", now.Add(-1*time.Hour))},
		Zoxide:    []string{"/home/u/agent-deck"}, // fuzzy hit for "dck"
		Query:     "dck",
		Now:       now,
	})
	if len(out) != 1 || out[0].Path != "/home/u/agent-deck" {
		t.Fatalf("zoxide fuzzy hit not exempt from query filter: %+v", out)
	}
}

// TestPathSuggest_PrefersWorktreeRepoRoot verifies worktree sessions surface
// their original repo root, not the ephemeral worktree directory.
func TestPathSuggest_PrefersWorktreeRepoRoot(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	inst := mkInst("/home/u/repo/.worktrees/feat", now.Add(-1*time.Hour))
	inst.WorktreeRepoRoot = "/home/u/repo"
	out := PathSuggest(PathSuggestInput{Instances: []*Instance{inst}, Now: now})
	if len(out) != 1 || out[0].Path != "/home/u/repo" {
		t.Fatalf("worktree repo root not preferred: %+v", out)
	}
}

// TestPathSuggest_SkipsEmptyAndNormalizes verifies empty paths are ignored and
// paths are cleaned (trailing slash removed) before dedup.
func TestPathSuggest_SkipsEmptyAndNormalizes(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	out := PathSuggest(PathSuggestInput{
		Instances:     []*Instance{mkInst("", now), mkInst("/a/b/", now.Add(-1*time.Hour))},
		GroupDefaults: []string{"/a/b", ""},
		Now:           now,
	})
	if len(out) != 1 || out[0].Path != "/a/b" {
		t.Fatalf("normalize/skip-empty failed: %+v", out)
	}
}

// TestPathSuggest_RespectsLimit verifies Limit caps the returned candidates.
func TestPathSuggest_RespectsLimit(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	var insts []*Instance
	for i, p := range []string{"/a", "/b", "/c", "/d", "/e"} {
		insts = append(insts, mkInst(p, now.Add(-time.Duration(i)*time.Hour)))
	}
	out := PathSuggest(PathSuggestInput{Instances: insts, Now: now, Limit: 2})
	if len(out) != 2 {
		t.Fatalf("limit not respected: got %d", len(out))
	}
}
