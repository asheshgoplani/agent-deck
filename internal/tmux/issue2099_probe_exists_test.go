package tmux

import (
	"os"
	"path/filepath"
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

func TestProbeExists_CompletedClientFailureIsNotAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		absent           bool
	}{
		{"missing session", "can't find session: =probe-unknown", true},
		{"missing server", "no server running on /tmp/probe.sock", true},
		{"client failure", "server exited unexpectedly", false},
		{"permission denied", "error connecting to /tmp/probe.sock (Permission denied)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\necho '"+tc.diagnostic+"' >&2\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			s := NewSession("probe-unknown", t.TempDir())
			exists, err := s.ProbeExists()
			if exists {
				t.Fatal("failed client cannot prove the session exists")
			}
			if tc.absent && err != nil {
				t.Fatalf("confirmed absence returned an error: %v", err)
			}
			if !tc.absent && err == nil {
				t.Fatal("inconclusive client failure was treated as absence")
			}
		})
	}
}

// #1873 relies on ProbeExists separating "tmux said the session is gone" from
// "no tmux client ever answered". A client that cannot be launched at all must
// be an error, not a false.
func TestProbeExists_ClientThatCannotLaunchIsUnknownNotAbsent(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no tmux binary reachable
	s := NewSession("probe-1873-nolaunch", t.TempDir())
	exists, err := s.ProbeExists()
	require.Error(t, err, "a probe with no tmux client is indeterminate")
	assert.False(t, exists)
	assert.Contains(t, err.Error(), "did not complete")
}
