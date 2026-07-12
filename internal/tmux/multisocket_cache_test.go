package tmux

import (
	"context"
	"testing"
	"time"
)

// TestSelectPipesPerSocket_OnePerDistinctAliveSocket pins the fix for the
// multi-socket cache aliasing bug: RefreshAllActivities must probe one pipe per
// distinct socket (each tmux server) rather than a single arbitrary pipe, which
// only ever sees one server's sessions. Sessions on the other socket would
// otherwise be absent from the cache and reported as tmux_missing.
func TestSelectPipesPerSocket_OnePerDistinctAliveSocket(t *testing.T) {
	pipes := map[string]*ControlPipe{
		"a1": {sessionName: "a1", socketName: "agent-deck", alive: true},
		"a2": {sessionName: "a2", socketName: "agent-deck", alive: true},
		"d1": {sessionName: "d1", socketName: "", alive: true},
		"x1": {sessionName: "x1", socketName: "other", alive: false}, // dead → skipped
	}

	got := selectPipesPerSocket(pipes)

	sockets := map[string]int{}
	for _, p := range got {
		sockets[p.socketName]++
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly one pipe per distinct alive socket (2), got %d: %v", len(got), sockets)
	}
	if sockets["agent-deck"] != 1 {
		t.Fatalf("expected one pipe for socket 'agent-deck', got %d", sockets["agent-deck"])
	}
	if sockets[""] != 1 {
		t.Fatalf("expected one pipe for the default socket, got %d", sockets[""])
	}
	if _, ok := sockets["other"]; ok {
		t.Fatal("a dead pipe's socket must not be selected")
	}
}

// TestSessionExists_NegativeCacheHitNotTrusted pins the core of the multi-socket
// fix: a fresh cache that does NOT contain a session must not, by itself, declare
// that session dead. The cache can transiently omit a live session whose socket
// differs from the one the last refresh was sourced from. Exists() must fall
// through to the live pipe / direct probe instead of trusting the negative.
//
// Deterministic without a real tmux server: prime a fresh cache that omits the
// session, then register a live control pipe for it. Old behavior returned the
// cached false immediately; the fix falls through and the live pipe answers true.
func TestSessionExists_NegativeCacheHitNotTrusted(t *testing.T) {
	oldPM := GetPipeManager()
	defer SetPipeManager(oldPM)

	// Default socket matches the session's socket so the cache guard applies —
	// this is exactly the case that regressed (session's socket == default, yet
	// the cache was built from a different socket and omits it).
	SetDefaultSocketName("")
	defer SetDefaultSocketName("")

	const name = "agentdeck_live_but_uncached_9f1c"

	// Fresh cache that contains some OTHER session but not `name` → a valid
	// negative hit for `name`.
	sessionCacheMu.Lock()
	sessionCacheData = map[string]int64{"agentdeck_someone_else_0001": time.Now().Unix()}
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()
	defer func() {
		sessionCacheMu.Lock()
		sessionCacheData = nil
		sessionCacheTime = time.Time{}
		sessionCacheMu.Unlock()
	}()

	// A live control pipe proves the session is actually alive.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm := NewPipeManager(ctx, nil)
	pm.mu.Lock()
	pm.pipes[name] = &ControlPipe{sessionName: name, socketName: "", alive: true}
	pm.mu.Unlock()
	SetPipeManager(pm)

	s := &Session{Name: name, SocketName: ""}

	if !s.Exists() {
		t.Fatal("Exists() trusted a negative cache hit and declared a live session dead (multi-socket aliasing false negative)")
	}
}
