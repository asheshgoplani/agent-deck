package watcher_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// buildClients creates a standard test client map with exact and wildcard entries.
func buildClients() map[string]watcher.ClientEntry {
	return map[string]watcher.ClientEntry{
		"contact@clienta.com": {
			Conductor: "client-a",
			Group:     "client-a/inbox",
			Name:      "Client A",
		},
		"*@clienta.com": {
			Conductor: "client-a-domain",
			Group:     "client-a/inbox",
			Name:      "Client A (domain)",
		},
		"*@clientb.com": {
			Conductor: "client-b",
			Group:     "client-b/inbox",
			Name:      "Client B",
		},
	}
}

// TestRouter_ExactOverWildcard verifies that an exact email match takes priority over a wildcard.
func TestRouter_ExactOverWildcard(t *testing.T) {
	r := watcher.NewRouter(buildClients())
	result := r.Match("contact@clienta.com")
	if result == nil {
		t.Fatal("expected a RouteResult, got nil")
	}
	if result.MatchType != "exact" {
		t.Errorf("expected MatchType 'exact', got %q", result.MatchType)
	}
	if result.Conductor != "client-a" {
		t.Errorf("expected Conductor 'client-a', got %q", result.Conductor)
	}
}

// TestRouter_UnroutedEvent verifies that unknown senders return nil.
func TestRouter_UnroutedEvent(t *testing.T) {
	r := watcher.NewRouter(buildClients())
	result := r.Match("nobody@unknown.com")
	if result != nil {
		t.Errorf("expected nil for unrouted sender, got %+v", result)
	}
}

// TestRouter_WildcardMatch verifies that wildcard matching works for domain patterns.
func TestRouter_WildcardMatch(t *testing.T) {
	r := watcher.NewRouter(buildClients())
	result := r.Match("anyone@clientb.com")
	if result == nil {
		t.Fatal("expected a RouteResult for wildcard match, got nil")
	}
	if result.MatchType != "wildcard" {
		t.Errorf("expected MatchType 'wildcard', got %q", result.MatchType)
	}
	if result.Conductor != "client-b" {
		t.Errorf("expected Conductor 'client-b', got %q", result.Conductor)
	}
}

// TestRouter_EmptyClientsMap verifies that an empty map returns nil for all senders.
func TestRouter_EmptyClientsMap(t *testing.T) {
	r := watcher.NewRouter(map[string]watcher.ClientEntry{})
	result := r.Match("anyone@example.com")
	if result != nil {
		t.Errorf("expected nil for empty client map, got %+v", result)
	}
}

// TestRouter_ReloadUnderConcurrentMatch verifies that Router.Reload is safe to call
// concurrently with Match. 100 goroutines call Match while a parallel goroutine
// calls Reload 50 times. Must pass under -race, no panic, no nil-map deref.
func TestRouter_ReloadUnderConcurrentMatch(t *testing.T) {
	r := watcher.NewRouter(buildClients())

	newClients := map[string]watcher.ClientEntry{
		"new@example.com": {
			Conductor: "new-conductor",
			Group:     "new/inbox",
			Name:      "New Client",
		},
	}

	var wg sync.WaitGroup
	// 100 goroutines calling Match concurrently.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = r.Match("anyone@x.com")
			}
		}()
	}
	// Parallel goroutine calling Reload 50 times.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			r.Reload(newClients)
		}
	}()

	wg.Wait()
}

// TestRouter_ReloadNil verifies that Reload(nil) is safe and subsequent Match returns nil.
func TestRouter_ReloadNil(t *testing.T) {
	r := watcher.NewRouter(nil)
	r.Reload(nil)
	result := r.Match("x@y.com")
	if result != nil {
		t.Errorf("expected nil after Reload(nil), got %+v", result)
	}
}

// TestRouter_LoadClientsJSON verifies that LoadClientsJSON reads a valid file correctly.
func TestRouter_LoadClientsJSON(t *testing.T) {
	clients := map[string]watcher.ClientEntry{
		"user@test.com": {
			Conductor: "test-conductor",
			Group:     "test/inbox",
			Name:      "Test User",
		},
		"*@test.com": {
			Conductor: "test-domain",
			Group:     "test/inbox",
			Name:      "Test Domain",
		},
	}
	data, err := json.Marshal(clients)
	if err != nil {
		t.Fatalf("failed to marshal clients: %v", err)
	}
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "clients.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	loaded, err := watcher.LoadClientsJSON(path)
	if err != nil {
		t.Fatalf("LoadClientsJSON returned error: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("expected 2 entries, got %d", len(loaded))
	}
	entry, ok := loaded["user@test.com"]
	if !ok {
		t.Fatal("expected 'user@test.com' entry not found")
	}
	if entry.Conductor != "test-conductor" {
		t.Errorf("expected Conductor 'test-conductor', got %q", entry.Conductor)
	}
}

// TestRouter_MatchCaseInsensitive verifies that sender addresses and
// clients.json keys match regardless of letter case, for both exact and
// wildcard rules, through NewRouter and Reload alike. Gmail hands the From
// address through as written, so "Bob@Example.COM" must hit "*@example.com".
func TestRouter_MatchCaseInsensitive(t *testing.T) {
	clients := map[string]watcher.ClientEntry{
		"contact@clienta.com": {Conductor: "client-a"},
		"*@clienta.com":       {Conductor: "client-a-domain"},
		"Boss@ClientC.com":    {Conductor: "client-c"},
		"*@ClientD.COM":       {Conductor: "client-d-domain"},
	}
	cases := []struct {
		sender, conductor, matchType string
	}{
		{"Contact@ClientA.com", "client-a", "exact"},
		{"CONTACT@CLIENTA.COM", "client-a", "exact"},
		{"Bob@Example.ClientA.com", "", ""},
		{"Bob@ClientA.COM", "client-a-domain", "wildcard"},
		{"bob@clienta.com", "client-a-domain", "wildcard"},
		{"boss@clientc.com", "client-c", "exact"},
		{"carol@clientd.com", "client-d-domain", "wildcard"},
	}
	reloaded := watcher.NewRouter(nil)
	reloaded.Reload(clients)
	for name, r := range map[string]*watcher.Router{"NewRouter": watcher.NewRouter(clients), "Reload": reloaded} {
		for _, tc := range cases {
			got := r.Match(tc.sender)
			if tc.conductor == "" {
				if got != nil {
					t.Errorf("%s: Match(%q) = %+v, want nil", name, tc.sender, got)
				}
				continue
			}
			if got == nil {
				t.Errorf("%s: Match(%q) = nil, want %s match to %q", name, tc.sender, tc.matchType, tc.conductor)
				continue
			}
			if got.Conductor != tc.conductor || got.MatchType != tc.matchType {
				t.Errorf("%s: Match(%q) = %s/%q, want %s/%q", name, tc.sender, got.MatchType, got.Conductor, tc.matchType, tc.conductor)
			}
		}
	}
}
