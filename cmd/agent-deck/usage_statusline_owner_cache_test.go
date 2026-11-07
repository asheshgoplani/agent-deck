package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

// isolateStatuslineOwnerEnv points every agent-deck root at a temp HOME.
func isolateStatuslineOwnerEnv(tb testing.TB) string {
	tb.Helper()
	home := tb.TempDir()
	tb.Setenv("HOME", home)
	tb.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	tb.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	tb.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	tb.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	tb.Setenv("CLAUDE_CONFIG_DIR", "")
	tb.Setenv("AGENTDECK_PROFILE", "")
	return home
}

// seedStatuslineProfiles creates n idle profile stores; owner holds one
// Claude session recording nativeID. File times are moved an hour back, as
// for the many idle stores of a long-lived install.
func seedStatuslineProfiles(tb testing.TB, n int, owner, nativeID string) []string {
	tb.Helper()
	var names []string
	for i := 0; i < n; i++ {
		names = append(names, fmt.Sprintf("p%03d", i))
	}
	names = append(names, owner)
	for _, name := range names {
		storage, err := session.NewStorageWithProfile(name)
		require.NoError(tb, err)
		if name == owner {
			require.NoError(tb, storage.GetDB().SaveInstance(&statedb.InstanceRow{ID: "deck-" + name, Title: "c", ProjectPath: "/p", Tool: "claude", CreatedAt: time.Now(), ToolData: json.RawMessage(`{"claude_session_id":"` + nativeID + `"}`)}))
		}
		require.NoError(tb, storage.Close())
		ageStatuslineStore(tb, name)
	}
	return names
}

func ageStatuslineStore(tb testing.TB, profile string) {
	tb.Helper()
	dbPath, err := session.GetDBPathForProfile(profile)
	require.NoError(tb, err)
	old := time.Now().Add(-time.Hour)
	for _, path := range []string{dbPath, dbPath + "-wal"} {
		if _, err := os.Stat(path); err == nil {
			require.NoError(tb, os.Chtimes(path, old, old))
		}
	}
}

func countStatuslineOwnerOpens(tb testing.TB) *int {
	tb.Helper()
	opens := new(int)
	orig := openStatuslineOwnerStore
	openStatuslineOwnerStore = func(profile string) (*session.Storage, error) {
		*opens++
		return orig(profile)
	}
	tb.Cleanup(func() { openStatuslineOwnerStore = orig })
	return opens
}

// ASK-229: every Claude status line refresh ran statuslineOwnerProfile, which
// opened every profile store on the machine (3075 on deckbox, ~2 CPU s per
// refresh). A repeat lookup with no store changed must open none, and a store
// that changed must be the only one opened again.
func TestStatuslineOwnerLookupSkipsUnchangedStores(t *testing.T) {
	isolateStatuslineOwnerEnv(t)
	seedStatuslineProfiles(t, 40, "owner", "native-1")
	opens := countStatuslineOwnerOpens(t)

	owner, ok, err := statuslineOwnerProfile("feed", "native-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "owner", owner)
	require.Equal(t, 41, *opens, "a cold lookup reads every store once")

	*opens = 0
	owner, ok, err = statuslineOwnerProfile("feed", "native-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "owner", owner)
	require.Equal(t, 0, *opens, "a repeat lookup must not reopen unchanged stores")

	// A new session lands in p007: only that store is read again, and the
	// lookup sees it.
	storage, err := session.NewStorageWithProfile("p007")
	require.NoError(t, err)
	require.NoError(t, storage.GetDB().SaveInstance(&statedb.InstanceRow{ID: "deck-new", Title: "n", ProjectPath: "/p", Tool: "claude", CreatedAt: time.Now(), ToolData: json.RawMessage(`{"claude_session_id":"native-2"}`)}))
	require.NoError(t, storage.Close())
	*opens = 0
	owner, ok, err = statuslineOwnerProfile("feed", "native-2")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "p007", owner)
	require.Equal(t, 1, *opens, "only the changed store is reopened")

	// Unowned IDs keep the fallback rule (feed store absent -> default absent).
	*opens = 0
	_, ok, err = statuslineOwnerProfile("feed", "nobody")
	require.NoError(t, err)
	require.False(t, ok)
	require.LessOrEqual(t, *opens, 1, "an unowned lookup must not rescan idle stores")

	// A session recorded in the feed profile itself wins, as before.
	storage, err = session.NewStorageWithProfile("p003")
	require.NoError(t, err)
	require.NoError(t, storage.GetDB().SaveInstance(&statedb.InstanceRow{ID: "deck-dup", Title: "d", ProjectPath: "/p", Tool: "claude", CreatedAt: time.Now(), ToolData: json.RawMessage(`{"claude_session_id":"native-1"}`)}))
	require.NoError(t, storage.Close())
	owner, ok, err = statuslineOwnerProfile("p003", "native-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "p003", owner)
	// ...and with two non-feed owners there is no unique owner: fallback.
	owner, ok, err = statuslineOwnerProfile("feed", "native-1")
	require.NoError(t, err)
	require.False(t, ok, "duplicate owners fall back, got %q", owner)
}

// BenchmarkStatuslineOwnerLookup measures one status line refresh's owner
// lookup against 300 idle profile stores.
func BenchmarkStatuslineOwnerLookup(b *testing.B) {
	isolateStatuslineOwnerEnv(b)
	seedStatuslineProfiles(b, 300, "owner", "native-1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		owner, ok, err := statuslineOwnerProfile("feed", "native-1")
		if err != nil || !ok || owner != "owner" {
			b.Fatalf("lookup = %q %v %v", owner, ok, err)
		}
	}
}
