package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
)

// newAccountUsageTestHome points quota's cache dir at a per-test XDG root, so
// nothing leaks between tests or onto the developer's machine.
func newAccountUsageTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func saveClaudeSnapshot(t *testing.T, profile string, snap quota.Snapshot) {
	t.Helper()
	store, err := quota.NewStore(profile)
	if err != nil {
		t.Fatalf("quota.NewStore(%q): %v", profile, err)
	}
	snap.ID = quota.ProviderClaude
	if err := store.Save(snap); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
}

// TestAccountUsageCache_Fresh pins the happy path: a recently-updated
// snapshot with both windows renders Known with both percentages and
// UpdatedAt populated.
func TestAccountUsageCache_Fresh(t *testing.T) {
	newAccountUsageTestHome(t)
	now := time.Now()
	saveClaudeSnapshot(t, "personal", quota.Snapshot{
		Windows: []quota.Window{
			{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 8},
			{Kind: quota.WindowSevenDay, Label: "7d", UsedPercentage: 24},
		},
		UpdatedAt: now.Add(-3 * time.Minute).Unix(),
	})

	cache := NewAccountUsageCache()
	got := cache.Get("personal", now)

	if !got.Known {
		t.Fatalf("Known = false, want true")
	}
	if !got.FiveHour.Known || got.FiveHour.Percent != 8 {
		t.Fatalf("FiveHour = %+v, want Known with Percent 8", got.FiveHour)
	}
	if !got.SevenDay.Known || got.SevenDay.Percent != 24 {
		t.Fatalf("SevenDay = %+v, want Known with Percent 24", got.SevenDay)
	}
	if !got.HasUpdatedAt {
		t.Fatalf("HasUpdatedAt = false, want true")
	}
	if AccountUsageStale(got.HasUpdatedAt, got.UpdatedAt, now) {
		t.Fatalf("a 3-minute-old snapshot must not be stale")
	}
}

// TestAccountUsageCache_Stale pins the 30-minute freshness bound: usage older
// than AccountUsageStaleAfter is still returned (never hidden) but reports
// stale via AccountUsageStale.
func TestAccountUsageCache_Stale(t *testing.T) {
	newAccountUsageTestHome(t)
	now := time.Now()
	saveClaudeSnapshot(t, "personal", quota.Snapshot{
		Windows:   []quota.Window{{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 8}},
		UpdatedAt: now.Add(-2 * time.Hour).Unix(),
	})

	cache := NewAccountUsageCache()
	got := cache.Get("personal", now)

	if !got.Known || !got.FiveHour.Known {
		t.Fatalf("got = %+v, want Known usage with a five-hour window", got)
	}
	if !AccountUsageStale(got.HasUpdatedAt, got.UpdatedAt, now) {
		t.Fatalf("a 2-hour-old snapshot must be stale")
	}
}

// TestAccountUsageCache_Missing pins that a slot with no quota file at all
// (never ingested a statusLine payload) renders Known: false, not an error
// and not a zero percentage.
func TestAccountUsageCache_Missing(t *testing.T) {
	newAccountUsageTestHome(t)
	cache := NewAccountUsageCache()
	got := cache.Get("nobody", time.Now())

	if got.Known {
		t.Fatalf("Known = true for a slot with no quota file, want false")
	}
	if got.Name != "nobody" {
		t.Fatalf("Name = %q, want %q", got.Name, "nobody")
	}
}

// TestAccountUsageCache_Malformed pins that a corrupt cache file degrades to
// unknown rather than a parse error surfacing to the render path.
func TestAccountUsageCache_Malformed(t *testing.T) {
	newAccountUsageTestHome(t)
	store, err := quota.NewStore("personal")
	if err != nil {
		t.Fatalf("quota.NewStore: %v", err)
	}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(store.Dir(), quota.ProviderClaude+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write malformed cache file: %v", err)
	}

	cache := NewAccountUsageCache()
	got := cache.Get("personal", time.Now())
	if got.Known {
		t.Fatalf("Known = true for a malformed cache file, want false")
	}
}

// TestAccountUsageCache_MtimeCached pins the hot-path contract: a second Get
// call for the same slot with an unchanged mtime must not re-observe a
// content change made without touching mtime — i.e. it really is cached by
// mtime, not re-read unconditionally.
func TestAccountUsageCache_MtimeCached(t *testing.T) {
	newAccountUsageTestHome(t)
	now := time.Now()
	saveClaudeSnapshot(t, "personal", quota.Snapshot{
		Windows:   []quota.Window{{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 8}},
		UpdatedAt: now.Unix(),
	})

	cache := NewAccountUsageCache()
	first := cache.Get("personal", now)
	if !first.FiveHour.Known || first.FiveHour.Percent != 8 {
		t.Fatalf("first read = %+v, want 8%%", first.FiveHour)
	}

	// Overwrite the file's content but pin its mtime to the original value:
	// a correct mtime cache must keep serving the first read.
	store, _ := quota.NewStore("personal")
	path := filepath.Join(store.Dir(), quota.ProviderClaude+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	originalMtime := info.ModTime()
	saveClaudeSnapshot(t, "personal", quota.Snapshot{
		Windows:   []quota.Window{{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 99}},
		UpdatedAt: now.Unix(),
	})
	if err := os.Chtimes(path, originalMtime, originalMtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	second := cache.Get("personal", now)
	if second.FiveHour.Percent != 8 {
		t.Fatalf("second read = %+v, want cached 8%% (mtime unchanged)", second.FiveHour)
	}

	// Now let the mtime actually advance and confirm the cache picks up the
	// new content.
	newMtime := originalMtime.Add(time.Second)
	if err := os.Chtimes(path, newMtime, newMtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	third := cache.Get("personal", now)
	if third.FiveHour.Percent != 99 {
		t.Fatalf("third read = %+v, want refreshed 99%% after mtime change", third.FiveHour)
	}
}

// TestCollectAccountUsage_ListsConfiguredSlots pins that CollectAccountUsage
// lists exactly the profiles with a claude config_dir set, sorted by name,
// regardless of whether each one has cached usage yet.
func TestCollectAccountUsage_ListsConfiguredSlots(t *testing.T) {
	newAccountUsageTestHome(t)
	now := time.Now()
	saveClaudeSnapshot(t, "work", quota.Snapshot{
		Windows:   []quota.Window{{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 61}},
		UpdatedAt: now.Add(-time.Minute).Unix(),
	})

	config := &UserConfig{Profiles: map[string]ProfileSettings{
		"personal":  {Claude: ProfileClaudeSettings{ConfigDir: "/tmp/does-not-matter-personal"}},
		"work":      {Claude: ProfileClaudeSettings{ConfigDir: "/tmp/does-not-matter-work"}},
		"no-claude": {}, // no config_dir set: not an account slot
	}}

	got := CollectAccountUsage(config, NewAccountUsageCache(), now)
	if len(got) != 2 {
		t.Fatalf("got %d slots, want 2: %+v", len(got), got)
	}
	if got[0].Name != "personal" || got[1].Name != "work" {
		t.Fatalf("got names %q, %q, want personal, work (sorted)", got[0].Name, got[1].Name)
	}
	if got[0].Known {
		t.Fatalf("personal slot has no cached usage yet, want Known: false")
	}
	if !got[1].Known || !got[1].FiveHour.Known || got[1].FiveHour.Percent != 61 {
		t.Fatalf("work slot = %+v, want Known usage with FiveHour 61%%", got[1])
	}
}

// TestCollectAccountUsage_NoSlots pins that zero configured claude account
// slots yields an empty (not nil-vs-empty-ambiguous) list, which the renderer
// turns into "accounts none".
func TestCollectAccountUsage_NoSlots(t *testing.T) {
	newAccountUsageTestHome(t)
	got := CollectAccountUsage(&UserConfig{}, NewAccountUsageCache(), time.Now())
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}
