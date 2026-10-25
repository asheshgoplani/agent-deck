package tmux

import (
	"os/exec"
	"testing"
	"time"
)

// With no tmux server, `list-windows -a` exits with "no server running". That
// is an authoritative empty listing: caching it as invalid left the cache
// stale forever, so every dead session's status check forked its own
// has-session + list-panes (~37 forks/s and ~160% CPU at 492 dead rows).
func TestRefreshSessionCache_NoServerIsAuthoritativeEmpty(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	SetDefaultSocketName(StaleSocketForTest(t))
	t.Cleanup(func() {
		SetDefaultSocketName("")
		sessionCacheMu.Lock()
		sessionCacheData = nil
		sessionCacheTime = time.Time{}
		sessionCacheMu.Unlock()
	})

	RefreshSessionCache()

	exists, valid := sessionExistsFromCache("agentdeck_gone")
	if !valid || exists {
		t.Fatalf("sessionExistsFromCache = (%v, %v) with no server, want (false, true)", exists, valid)
	}
	s := &Session{Name: "agentdeck_gone", SocketName: DefaultSocketName()}
	before := SubprocessStarts()
	if !s.AbsentFromCache() {
		t.Fatal("AbsentFromCache() = false with no server; ghost rechecks would fork tmux")
	}
	if n := SubprocessStarts() - before; n != 0 {
		t.Errorf("AbsentFromCache spawned %d subprocesses, want 0", n)
	}
}
