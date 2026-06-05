package watcher

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ClientEntry holds the routing configuration for a single client.
// Entries in clients.json are keyed by either exact email ("user@example.com")
// or wildcard domain ("*@example.com").
type ClientEntry struct {
	// Conductor is the conductor name that handles events from this client
	Conductor string `json:"conductor"`

	// Group is the TUI group path to place sessions under (e.g., "client-a/inbox")
	Group string `json:"group"`

	// Name is a human-readable label for the client
	Name string `json:"name"`
}

// RouteResult is the result of a Router.Match call.
type RouteResult struct {
	// Conductor is the conductor name to route the event to
	Conductor string

	// Group is the TUI group path
	Group string

	// Name is the client's display name
	Name string

	// MatchType is either "exact" (email match) or "wildcard" (domain match)
	MatchType string
}

// Router provides config-driven routing of events to conductors.
// It loads rules from clients.json and matches incoming senders using
// exact email lookup first, then wildcard domain fallback.
//
// Router is safe for concurrent use. Match takes a read lock so multiple
// callers can proceed in parallel. Reload takes a write lock to atomically
// replace the internal routing tables (D-12/D-15/D-16).
type Router struct {
	// mu guards clients and wildcards for concurrent Reload + Match (D-15).
	mu sync.RWMutex

	// clients holds exact email -> entry mappings
	clients map[string]ClientEntry

	// wildcards holds domain -> entry mappings (from "*@domain" keys)
	wildcards map[string]ClientEntry
}

// NewRouter builds a Router from a flat map of client entries.
// Keys starting with "*@" are treated as wildcard domain rules;
// all other keys are treated as exact email rules.
func NewRouter(clients map[string]ClientEntry) *Router {
	r := &Router{
		clients:   make(map[string]ClientEntry, len(clients)),
		wildcards: make(map[string]ClientEntry),
	}
	for key, entry := range clients {
		if strings.HasPrefix(key, "*@") {
			domain := strings.TrimPrefix(key, "*@")
			r.wildcards[domain] = entry
		} else {
			r.clients[key] = entry
		}
	}
	return r
}

// LoadClientsJSON reads a clients.json file from disk and returns the parsed entries.
// Returns an error if the file does not exist or contains invalid JSON.
//
// Threat T-13-01: entries with empty conductor fields are logged as warnings at engine
// startup; the router itself does not reject them to preserve forward compatibility.
func LoadClientsJSON(path string) (map[string]ClientEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read clients.json at %q: %w", path, err)
	}
	var entries map[string]ClientEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse clients.json at %q: %w", path, err)
	}
	return entries, nil
}

// LoadFromWatcherDir loads clients.json from the effective watcher data
// directory and returns a ready-to-use Router.
func LoadFromWatcherDir() (*Router, error) {
	base, err := session.WatcherDir()
	if err != nil {
		return nil, fmt.Errorf("resolve watcher dir: %w", err)
	}
	path := base + "/clients.json"
	clients, err := LoadClientsJSON(path)
	if err != nil {
		return nil, err
	}
	return NewRouter(clients), nil
}

// Match returns the routing result for a given sender address.
// Exact email match takes priority over wildcard domain match (D-08).
// Returns nil if the sender does not match any rule (unrouted).
//
// Safe to call concurrently with Reload (takes RLock for the duration, D-15).
func (r *Router) Match(sender string) *RouteResult {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. Exact match
	if entry, ok := r.clients[sender]; ok {
		return &RouteResult{
			Conductor: entry.Conductor,
			Group:     entry.Group,
			Name:      entry.Name,
			MatchType: "exact",
		}
	}

	// 2. Wildcard domain match
	at := strings.LastIndex(sender, "@")
	if at >= 0 {
		domain := sender[at+1:]
		if entry, ok := r.wildcards[domain]; ok {
			return &RouteResult{
				Conductor: entry.Conductor,
				Group:     entry.Group,
				Name:      entry.Name,
				MatchType: "wildcard",
			}
		}
	}

	// Unrouted: triage handled in Phase 18
	return nil
}

// Reload atomically replaces the router's routing tables with newClients.
// Safe to call concurrently with Match. Builds fresh exact and wildcard maps
// before acquiring the write lock, then swaps both under a single lock (D-12/D-16).
// A nil input is treated as an empty map (never assigns nil to internal fields, D-15/Pitfall 5).
func (r *Router) Reload(newClients map[string]ClientEntry) {
	newExact := make(map[string]ClientEntry)
	newWild := make(map[string]ClientEntry)
	for k, v := range newClients {
		if strings.HasPrefix(k, "*@") {
			newWild[strings.TrimPrefix(k, "*@")] = v
		} else {
			newExact[k] = v
		}
	}
	r.mu.Lock()
	r.clients = newExact
	r.wildcards = newWild
	r.mu.Unlock()
}
