package tmux

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #2099: ProbeExists is the authoritative post-spawn check. It must ask
// the server for this exact session and ignore every cached or assumed
// answer that Exists() is allowed to give on the status hot path.

func TestIssue2099_ProbeExistsIsExactAndUncached(t *testing.T) {
	skipIfNoTmuxBinary(t)

	live := NewSession("probe-2099-live", t.TempDir())
	require.NoError(t, live.Start("sleep 60"))
	t.Cleanup(func() { _ = live.Kill() })

	exists, err := live.ProbeExists()
	require.NoError(t, err)
	assert.True(t, exists, "a running session must probe as present")

	// A session whose name is a strict prefix of the live one must NOT be
	// answered by tmux's default prefix matching.
	prefix := NewSession("probe-2099", t.TempDir())
	prefix.Name = live.Name[:len(live.Name)-2]
	exists, err = prefix.ProbeExists()
	require.NoError(t, err)
	assert.False(t, exists, "prefix name %q must not match live session %q", prefix.Name, live.Name)

	// A positive cache entry must not be trusted: the session is killed and
	// the probe must report it gone even while the cache still lists it.
	registerSessionInCache(live.Name)
	sessionCacheMu.Lock()
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()
	t.Cleanup(func() {
		sessionCacheMu.Lock()
		delete(sessionCacheData, live.Name)
		sessionCacheMu.Unlock()
	})
	require.NoError(t, live.Kill())
	cached, valid := sessionExistsFromCache(live.Name)
	require.True(t, cached && valid, "fixture: the cache must still list the killed session")
	assert.True(t, live.Exists(), "fixture: Exists() trusts the positive cache entry, which is what ProbeExists must not do")
	exists, err = live.ProbeExists()
	require.NoError(t, err)
	assert.False(t, exists, "a killed session must probe as gone regardless of the cache")
}
