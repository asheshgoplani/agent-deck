package tmux

import (
	"testing"
	"time"
)

// ExpireToolDetectionForTest clears a session's detection cache and process
// binding so the next DetectTool walks again.
func ExpireToolDetectionForTest(s *Session) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.detectedTool = ""
	s.toolDetectedAt = time.Time{}
	s.detectedAgentPID = 0
	s.detectedAgentStart = ""
	s.mu.Unlock()
}

// SetAgentTreeForTest replaces this session's pane process probe. The live
// /proc and tmux reads are not used until cleanup. The probe is reported as
// successful; an empty agent set is a real "no agent" result, not a failure.
func (s *Session) SetAgentTreeForTest(t testing.TB, panePID int, procs []PaneProcess) {
	t.Helper()
	if s == nil {
		t.Fatal("nil session")
	}
	prev := s.agentTreeOverride
	samples := make([]processSample, len(procs))
	for i, proc := range procs {
		argv := append([]string(nil), proc.Argv...)
		samples[i] = processSample{
			PID:     proc.PID,
			PPID:    proc.PPID,
			Comm:    proc.Comm,
			Argv:    argv,
			StartID: proc.StartID,
			Zombie:  proc.Zombie,
		}
	}
	s.agentTreeOverride = func() (int, []processSample, error) {
		out := make([]processSample, len(samples))
		copy(out, samples)
		return panePID, out, nil
	}
	t.Cleanup(func() { s.agentTreeOverride = prev })
}

// SeedPaneInfoCacheForTest replaces the package's pane info cache with the
// supplied data and marks it fresh. Test cleanup wipes the cache back to its
// pristine zero state so concurrent or follow-on tests do not see seeded data.
//
// Production callers must use RefreshPaneInfoCache; this exists so packages
// outside internal/tmux (notably internal/ui) can drive snapshot/render tests
// without standing up a real tmux server.
func SeedPaneInfoCacheForTest(t testing.TB, info map[string]PaneInfo) {
	t.Helper()
	paneCacheMu.Lock()
	paneCacheData = info
	paneCacheTime = time.Now()
	paneCacheMu.Unlock()
	t.Cleanup(func() {
		paneCacheMu.Lock()
		paneCacheData = nil
		paneCacheTime = time.Time{}
		paneCacheMu.Unlock()
	})
}

// SeedWindowCacheForTest replaces the package's window cache with the supplied
// data and marks it fresh. Test cleanup wipes the cache back to its pristine
// zero state so concurrent or follow-on tests do not see seeded data.
//
// Production callers must use RefreshSessionCache; this exists so packages
// outside internal/tmux (notably internal/ui) can drive window-row injection
// through the real rebuildFlatItems path without standing up a real tmux
// server.
func SeedWindowCacheForTest(t testing.TB, windows map[string][]WindowInfo) {
	t.Helper()
	windowCacheMu.Lock()
	windowCacheData = windows
	windowCacheTime = time.Now()
	windowCacheMu.Unlock()
	t.Cleanup(func() {
		windowCacheMu.Lock()
		windowCacheData = nil
		windowCacheTime = time.Time{}
		windowCacheMu.Unlock()
	})
}

// ExpireStartupWindowForTest ends the session's startup window (see
// inStartupWindowLocked) so GetStatus classifies the pane from live evidence
// instead of reporting "starting".
//
// Why tests need this (issue #1720): a session leaves the startup window early
// only when a poll finds a busy indicator or a PROMPT indicator, and the shell
// prompt patterns are the literal strings "$ ", "# " and "% ". A pane running
// the developer's own login shell prints whatever prompt that shell is
// configured to print — fish, starship, powerlevel10k and friends end in "❯",
// and a dotfiles-managed zsh prompt need not contain any of the three. Those
// panes stay "starting" for the full startupStateWindow, so any test that
// asserts a tmux-derived waiting/idle/running verdict on a real shell session
// would pass or fail according to the machine's shell configuration. Ending the
// window explicitly makes the assertion about the code under test.
func ExpireStartupWindowForTest(t testing.TB, s *Session) {
	t.Helper()
	if s == nil {
		// Explicit return: t.Fatal is not a terminating call to static analysis,
		// so without it the writes below read as a possible nil dereference.
		t.Fatal("ExpireStartupWindowForTest: nil session")
		return
	}
	s.mu.Lock()
	s.startupAt = time.Time{}
	s.mu.Unlock()
}

// ExpirePaneInfoCacheForTest leaves the cache contents intact but rewinds the
// timestamp past the freshness threshold so GetCachedPaneInfo treats it as
// stale. Used to model the case where backgroundStatusUpdate hasn't run for a
// while (e.g. navigation hot-window) and the snapshot rebuild path must not
// blow away previously-known pane titles. t.Cleanup restores the timestamp
// so calling Expire alone (without a prior Seed that owns its own cleanup)
// is also safe.
func ExpirePaneInfoCacheForTest(t testing.TB) {
	t.Helper()
	paneCacheMu.Lock()
	paneCacheTime = time.Now().Add(-1 * time.Hour)
	paneCacheMu.Unlock()
	t.Cleanup(func() {
		paneCacheMu.Lock()
		paneCacheTime = time.Time{}
		paneCacheMu.Unlock()
	})
}
