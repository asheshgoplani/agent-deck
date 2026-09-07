package quota

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// secondProviderID stands in for a provider other than Claude. The store is
// one-file-per-provider so that independent writers never collide, and these
// tests pin that with two ids; it sorts after "claude", which the ordering
// assertions rely on.
const secondProviderID = "other"

// sentinelToken is a value that would only ever reach the cache by mistake. The
// cache holds percentages and reset times, never a credential.
const sentinelToken = "SENTINEL-QUOTA-TOKEN-do-not-leak"

// newTestStore points the store at a per-test XDG cache root so nothing leaks
// between tests or onto the developer's machine.
func newTestStore(t *testing.T, profile string) *Store {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	store, err := NewStore(profile)
	require.NoError(t, err)
	return store
}

func TestStoreRoundTrip(t *testing.T) {
	store := newTestStore(t, "default")
	resets := int64(1738425600)
	saved := Snapshot{
		ID:        ProviderClaude,
		Label:     claudeLabel,
		Windows:   []Window{{Kind: WindowFiveHour, Label: "5h", UsedPercentage: 23.5, ResetsAt: &resets}},
		UpdatedAt: time.Now().Unix(),
	}
	require.NoError(t, store.Save(saved))

	loaded, err := store.Load()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, ProviderClaude, loaded[0].ID)
	require.Len(t, loaded[0].Windows, 1)
	assert.Equal(t, 23.5, loaded[0].Windows[0].UsedPercentage)
	require.NotNil(t, loaded[0].Windows[0].ResetsAt)
	assert.Equal(t, resets, *loaded[0].Windows[0].ResetsAt)
}

func TestStoreStalenessIsComputedAtReadAndStillReturned(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		wantStale bool
	}{
		{"fresh", time.Minute, false},
		{"older than ttl", DefaultStaleAfter + time.Minute, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t, "default")
			require.NoError(t, store.Save(Snapshot{
				ID:        secondProviderID,
				Label:     secondProviderID,
				UpdatedAt: time.Now().Add(-tt.age).Unix(),
				// Stale is never persisted: a snapshot written as fresh would
				// otherwise claim to be fresh forever.
				Stale: false,
			}))

			loaded, err := store.Load()
			require.NoError(t, err)
			// A stale snapshot is still RETURNED, marked. Hiding it leaves the
			// user with nothing where they previously had a number.
			require.Len(t, loaded, 1)
			assert.Equal(t, tt.wantStale, loaded[0].Stale)
		})
	}
}

func TestStoreConcurrentProvidersBothSurvive(t *testing.T) {
	// Claude's snapshot is PUSHED by the statusLine ingester, possibly by
	// several Claude sessions at once, and a second provider would have a
	// writer of its own. One file per provider is what makes that safe; a
	// shared file would lose one of these writes.
	store := newTestStore(t, "default")

	var wg sync.WaitGroup
	for _, id := range []string{ProviderClaude, secondProviderID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				assert.NoError(t, store.Save(Snapshot{ID: id, Label: id, UpdatedAt: time.Now().Unix()}))
			}
		}(id)
	}
	wg.Wait()

	loaded, err := store.Load()
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	assert.Equal(t, ProviderClaude, loaded[0].ID)
	assert.Equal(t, secondProviderID, loaded[1].ID)
}

func TestStoreCorruptFileDoesNotHideOtherProvider(t *testing.T) {
	store := newTestStore(t, "default")
	require.NoError(t, store.Save(Snapshot{ID: secondProviderID, Label: secondProviderID, UpdatedAt: time.Now().Unix()}))
	require.NoError(t, os.WriteFile(filepath.Join(store.Dir(), ProviderClaude+".json"), []byte("{not json"), 0o644))

	loaded, err := store.Load()
	require.NoError(t, err)
	require.Len(t, loaded, 2)

	assert.Equal(t, ProviderClaude, loaded[0].ID)
	assert.NotEmpty(t, loaded[0].Error, "a corrupt cache entry is reported, not silently dropped")
	assert.Equal(t, secondProviderID, loaded[1].ID)
	assert.Empty(t, loaded[1].Error)
}

func TestStoreWritesNoCredential(t *testing.T) {
	store := newTestStore(t, "default")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", sentinelToken)
	require.NoError(t, store.Save(Snapshot{
		ID:        secondProviderID,
		Label:     secondProviderID,
		Windows:   []Window{{Kind: WindowSevenDay, Label: "7d", UsedPercentage: 22}},
		UpdatedAt: time.Now().Unix(),
	}))

	entries, err := os.ReadDir(store.Dir())
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		contents, err := os.ReadFile(filepath.Join(store.Dir(), entry.Name()))
		require.NoError(t, err)
		assert.NotContains(t, string(contents), sentinelToken)
	}
}

func TestStoreDirIsPrivate(t *testing.T) {
	store := newTestStore(t, "default")
	require.NoError(t, store.Save(Snapshot{ID: secondProviderID, Label: secondProviderID, UpdatedAt: time.Now().Unix()}))

	info, err := os.Stat(store.Dir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestStoreProfilesAreIsolated(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	work, err := NewStore("work")
	require.NoError(t, err)
	personal, err := NewStore("personal")
	require.NoError(t, err)

	require.NoError(t, work.Save(Snapshot{ID: secondProviderID, Label: secondProviderID, UpdatedAt: time.Now().Unix()}))

	loaded, err := personal.Load()
	require.NoError(t, err)
	assert.Empty(t, loaded)
	assert.NotEqual(t, work.Dir(), personal.Dir())
}

func TestStoreLoadOnMissingDirIsEmptyNotAnError(t *testing.T) {
	// The state before anything has ever been ingested or fetched. `usage` must
	// print an empty report there, not fail.
	store := newTestStore(t, "default")
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Empty(t, loaded)
	_, statErr := os.Stat(store.Dir())
	assert.True(t, os.IsNotExist(statErr), "Load must not create the cache directory")
}

func TestStoreRejectsUnusableProviderID(t *testing.T) {
	// The provider id becomes a filename. A traversal component in it would
	// write outside the cache directory.
	store := newTestStore(t, "default")
	for _, id := range []string{"", "../escape", "a/b", strings.Repeat("x", 300)} {
		assert.Error(t, store.Save(Snapshot{ID: id, UpdatedAt: time.Now().Unix()}), "id %q", id)
	}
}
