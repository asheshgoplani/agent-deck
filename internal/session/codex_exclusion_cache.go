package session

import (
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

const codexExclusionTTL = 2 * time.Second

type codexOwnershipSnapshot struct {
	at         time.Time
	claims     *codexOwnershipClaims
	refreshing chan struct{}
}

type codexOwnershipClaims struct {
	sync.Mutex
	bySession map[string]string
	// Publications received during a refresh override its earlier tmux reads.
	updates map[string]string
}

// Serialize fallback selection and claim publication, not authoritative probes.
var codexBootstrapMu sync.Mutex

// Share short-lived ownership evidence across standalone detection calls and
// adjacent status passes. Each socket coalesces cold misses independently;
// subprocesses never hold the cache mutex. In-process bindings publish immediately.
var codexOwnershipCache = struct {
	sync.Mutex
	bySocket map[string]codexOwnershipSnapshot
}{bySocket: make(map[string]codexOwnershipSnapshot)}

// StatusUpdatePass pins lazy Codex ownership evidence for an entire sweep,
// even if that sweep outlives the TTL. Its zero value is ready for concurrent
// workers. New bindings and refreshes by other passes remain visible. Create a
// new pass for each sweep; do not retain it between sweeps.
type StatusUpdatePass struct {
	mu       sync.Mutex
	bySocket map[string]codexOwnershipSnapshot
	loading  map[string]chan struct{}
}

// UpdateStatus refreshes one instance using this pass's shared evidence.
func (p *StatusUpdatePass) UpdateStatus(i *Instance) error {
	return i.updateStatus(p, true)
}

// UpdateStatusOnly refreshes status without discovering native tool session IDs.
// Read-only listings use this path; metadata discovery belongs to the poller.
func (p *StatusUpdatePass) UpdateStatusOnly(i *Instance) error {
	return i.updateStatus(p, false)
}

func loadCodexOwnership(socket string) codexOwnershipSnapshot {
	codexOwnershipCache.Lock()
	snapshot := codexOwnershipCache.bySocket[socket]
	if snapshot.refreshing != nil {
		codexOwnershipCache.Unlock()
		<-snapshot.refreshing
		return loadCodexOwnership(socket)
	}
	if time.Since(snapshot.at) < codexExclusionTTL {
		codexOwnershipCache.Unlock()
		return snapshot
	}
	if snapshot.claims == nil {
		snapshot.claims = &codexOwnershipClaims{}
	}
	owners := snapshot.claims
	owners.Lock()
	owners.updates = make(map[string]string)
	owners.Unlock()
	snapshot.refreshing = make(chan struct{})
	codexOwnershipCache.bySocket[socket] = snapshot
	codexOwnershipCache.Unlock()
	var bySession map[string]string
	names, err := tmux.ListAgentDeckSessionsOnSocket(socket)
	if err == nil {
		bySession = make(map[string]string, len(names))
		for _, name := range names {
			peer := &tmux.Session{Name: name, SocketName: socket}
			id, err := peer.ReadEnvironment("CODEX_SESSION_ID")
			if err != nil {
				bySession = nil
				break
			}
			if id != "" {
				bySession[name] = id
			}
		}
	}
	// Only a complete enumeration is usable for bootstrap. Publications still
	// survive successful refreshes that read an older environment value.
	codexOwnershipCache.Lock()
	owners.Lock()
	if bySession != nil {
		for name, id := range owners.updates {
			bySession[name] = id
		}
	}
	owners.bySession = bySession
	owners.updates = nil
	owners.Unlock()
	snapshot.at = time.Now()
	close(snapshot.refreshing)
	snapshot.refreshing = nil
	codexOwnershipCache.bySocket[socket] = snapshot
	codexOwnershipCache.Unlock()
	return snapshot
}

func (p *StatusUpdatePass) codexOwnership(socket string) codexOwnershipSnapshot {
	if p == nil {
		return loadCodexOwnership(socket)
	}
	p.mu.Lock()
	if snapshot, ok := p.bySocket[socket]; ok {
		p.mu.Unlock()
		return snapshot
	}
	if ready := p.loading[socket]; ready != nil {
		p.mu.Unlock()
		<-ready
		return p.codexOwnership(socket)
	}
	if p.loading == nil {
		p.loading = make(map[string]chan struct{})
	}
	ready := make(chan struct{})
	p.loading[socket] = ready
	p.mu.Unlock()
	snapshot := loadCodexOwnership(socket)
	p.mu.Lock()
	if p.bySocket == nil {
		p.bySocket = make(map[string]codexOwnershipSnapshot)
	}
	p.bySocket[socket] = snapshot
	delete(p.loading, socket)
	close(ready)
	p.mu.Unlock()
	return snapshot
}

func (i *Instance) codexExclusions(p *StatusUpdatePass) map[string]bool {
	socket := tmux.DefaultSocketName()
	ownName := ""
	if i.tmuxSession != nil {
		socket = i.tmuxSession.SocketName
		ownName = i.tmuxSession.Name
	}
	snapshot := p.codexOwnership(socket)
	snapshot.claims.Lock()
	defer snapshot.claims.Unlock()
	if snapshot.claims.bySession == nil {
		return nil
	}
	// A caller-owned map preserves the existing API: callers may augment it.
	// Compare owners, not IDs, so an ID also held by a peer stays excluded.
	exclude := make(map[string]bool, len(snapshot.claims.bySession))
	for name, id := range snapshot.claims.bySession {
		if name != ownName {
			exclude[id] = true
		}
	}
	return exclude
}

func (i *Instance) recordCodexOwnership(id string) {
	if i.tmuxSession == nil || id == "" {
		return
	}
	codexOwnershipCache.Lock()
	defer codexOwnershipCache.Unlock()
	claims := codexOwnershipCache.bySocket[i.tmuxSession.SocketName].claims
	if claims == nil {
		return // Refresh registers claims before starting any subprocess reads.
	}
	claims.Lock()
	defer claims.Unlock()
	if claims.updates != nil {
		claims.updates[i.tmuxSession.Name] = id
	}
	if claims.bySession != nil {
		claims.bySession[i.tmuxSession.Name] = id
	}
}
