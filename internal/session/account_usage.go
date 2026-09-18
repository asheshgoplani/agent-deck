package session

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
)

// AccountUsageStaleAfter is the freshness bound for the "accounts" preview
// field: usage last updated longer ago than this renders as stale rather
// than presented as current. Deliberately its own constant rather than
// quota.DefaultStaleAfter — that 15-minute bound is tuned for a terminal
// `agent-deck usage` invocation; this field renders continuously in the TUI
// header/preview panel, so it can afford a longer bound before flagging
// staleness (a session that is merely idle for a few minutes should not
// immediately read as broken).
const AccountUsageStaleAfter = 30 * time.Minute

// AccountUsageWindow is one quota window (5h or 7d) for an account slot.
// Known is false when the provider never reported this window — never a
// zero presented as a real percentage.
type AccountUsageWindow struct {
	Known   bool
	Percent float64
}

// AccountUsage is one named account slot's live quota snapshot for the
// "accounts" preview field ([ui.remote_preview].fields / [ui.header].fields).
//
// Gathered from the same on-disk cache internal/quota reads for
// `agent-deck usage` (~/.cache/agent-deck/quota/<name>/claude.json), keyed by
// the account slot's name — an agent-deck "profile" is exactly the account
// slot `accounts --json` lists, and the statusLine ingester
// (`agent-deck usage ingest claude`) writes that same profile's quota file.
type AccountUsage struct {
	Name string
	// Known is false when no usage file exists for this slot, or it exists
	// but could not be read or parsed. Never a guess at percentages.
	Known bool
	// UnknownReason says why Known is false, one of the AccountUsage*
	// reason constants, so the preview can name the cause ("no feed", "no
	// data yet", "unreadable") instead of a bare "usage unknown". Empty
	// when the reason is not known — a remote older than this field.
	UnknownReason string
	HasUpdatedAt  bool
	UpdatedAt     time.Time
	FiveHour      AccountUsageWindow
	SevenDay      AccountUsageWindow
}

// Reasons an account slot's usage is unknown (AccountUsage.UnknownReason).
const (
	// AccountUsageNoFeed: the slot's Claude settings.json statusLine does
	// not run `agent-deck usage ingest claude`, so nothing ever writes the
	// quota file. `agent-deck hooks install` wires it.
	AccountUsageNoFeed = "no_feed"
	// AccountUsageNoData: the feed is wired but Claude has not run a
	// statusLine refresh for this slot yet, so the quota file does not exist.
	AccountUsageNoData = "no_data"
	// AccountUsageUnreadable: the quota file exists but could not be read
	// or parsed, or carries no claude snapshot.
	AccountUsageUnreadable = "unreadable"
)

// AccountUsageStale reports whether usage last updated at updatedAt (valid
// only when hasUpdatedAt) is older than AccountUsageStaleAfter as of now.
// A never-updated snapshot (hasUpdatedAt false) is always stale: there is no
// age to compare against, and treating it as fresh would be a guess.
func AccountUsageStale(hasUpdatedAt bool, updatedAt, now time.Time) bool {
	if !hasUpdatedAt {
		return true
	}
	return now.Sub(updatedAt) > AccountUsageStaleAfter
}

// AccountUsageCache reads each account slot's quota file at most once per
// distinct mtime, so a render path that calls Get every draw (the TUI header)
// re-parses JSON only when the file actually changed on disk, not on every
// frame. Safe for concurrent use.
type AccountUsageCache struct {
	mu      sync.Mutex
	entries map[string]accountUsageCacheEntry
}

type accountUsageCacheEntry struct {
	mtime time.Time
	usage AccountUsage
}

// NewAccountUsageCache returns an empty cache.
func NewAccountUsageCache() *AccountUsageCache {
	return &AccountUsageCache{entries: make(map[string]accountUsageCacheEntry)}
}

// Get returns the account slot's cached usage, re-reading its quota file
// only when the file's mtime has changed since the last call for this name.
// A missing quota directory or file is not an error: it is the normal state
// for a slot that has never run Claude with the statusLine ingester wired up,
// and yields AccountUsage{Name: name, Known: false}.
func (c *AccountUsageCache) Get(name string, now time.Time) AccountUsage {
	store, err := quota.NewStore(name)
	if err != nil {
		return AccountUsage{Name: name, UnknownReason: AccountUsageUnreadable}
	}
	path := filepath.Join(store.Dir(), quota.ProviderClaude+".json")
	fi, statErr := os.Stat(path)
	var info time.Time
	if statErr == nil {
		info = fi.ModTime()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if statErr != nil {
		delete(c.entries, name)
		return AccountUsage{Name: name, UnknownReason: AccountUsageNoData}
	}
	if entry, ok := c.entries[name]; ok && entry.mtime.Equal(info) {
		return entry.usage
	}

	usage := loadAccountUsage(store, name)
	c.entries[name] = accountUsageCacheEntry{mtime: info, usage: usage}
	return usage
}

// loadAccountUsage reads and parses a slot's quota store, extracting the
// claude provider's five_hour/seven_day windows. Any failure — no claude
// snapshot present, an unreadable or corrupt cache file (quota.Store reports
// those as Snapshot.Error rather than an error return) — yields Known: false
// with AccountUsageUnreadable rather than a guessed number.
func loadAccountUsage(store *quota.Store, name string) AccountUsage {
	unreadable := AccountUsage{Name: name, UnknownReason: AccountUsageUnreadable}
	snapshots, err := store.Load()
	if err != nil {
		return unreadable
	}
	for _, snap := range snapshots {
		if snap.ID != quota.ProviderClaude || snap.Error != "" {
			continue
		}
		usage := AccountUsage{Name: name, Known: true}
		if snap.UpdatedAt > 0 {
			usage.UpdatedAt = time.Unix(snap.UpdatedAt, 0)
			usage.HasUpdatedAt = true
		}
		for _, w := range snap.Windows {
			switch w.Kind {
			case quota.WindowFiveHour:
				usage.FiveHour = AccountUsageWindow{Known: true, Percent: w.UsedPercentage}
			case quota.WindowSevenDay:
				usage.SevenDay = AccountUsageWindow{Known: true, Percent: w.UsedPercentage}
			}
		}
		return usage
	}
	return unreadable
}

// CollectAccountUsage lists this host's named Claude account slots — the same
// [profiles.<name>.claude].config_dir bindings `accounts --json` reports —
// and each one's cached quota usage, sorted by name. Used both by the
// remote-side `system stats` gatherer (assembling the accounts block of the
// poll payload on the remote host) and the controller's own local header:
// both read the vantage point's own local quota cache, never one over SSH.
func CollectAccountUsage(config *UserConfig, cache *AccountUsageCache, now time.Time) []AccountUsage {
	if config == nil || cache == nil {
		return nil
	}
	names := configuredClaudeAccountNames(config)
	if len(names) == 0 {
		return nil
	}
	out := make([]AccountUsage, 0, len(names))
	for _, name := range names {
		usage := cache.Get(name, now)
		// A slot whose statusLine does not run the ingester has no feed:
		// its quota file will never appear, which is a different fix
		// (`hooks install`) than waiting for Claude to refresh.
		if !usage.Known && usage.UnknownReason == AccountUsageNoData && !UsageFeedStatus(config.GetProfileClaudeConfigDir(name), name).Wired {
			usage.UnknownReason = AccountUsageNoFeed
		}
		out = append(out, usage)
	}
	return out
}

// configuredClaudeAccountNames returns the sorted names of every profile with
// a [profiles.<name>.claude].config_dir set — the same slot list
// `accounts --json` (default --harness claude) reports, without the
// config_dir/Exists detail this feature does not need.
func configuredClaudeAccountNames(config *UserConfig) []string {
	if config == nil || config.Profiles == nil {
		return nil
	}
	names := make([]string, 0, len(config.Profiles))
	for name := range config.Profiles {
		if config.GetProfileClaudeConfigDir(name) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
