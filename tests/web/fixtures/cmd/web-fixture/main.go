// Package main is a tiny standalone binary that boots only the agent-deck
// web HTTP server, backed by an in-memory MenuDataLoader and SessionMutator.
//
// It exists so Playwright e2e tests can exercise the web UI against
// deterministic, controllable state without depending on tmux, real
// session storage, or anything else on the host. The Playwright global
// setup builds and spawns this binary; teardown kills it.
//
// All fixture state lives in process memory and resets when the binary
// exits. Tests that need to share state across pages should use the
// admin endpoints exposed at /__fixture/ to seed/reset the in-memory store.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:38291", "Listen address (use 127.0.0.1:0 for OS-allocated ephemeral port)")
	mutationsAllowed := flag.Bool("allow-mutations", true, "Allow POST/DELETE actions through the web API")
	portFile := flag.String("port-file", "", "If set, write the bound TCP port to this file once listening (used with :0)")
	startupToken := flag.String("startup-token", "", "Echoed at /__fixture/whoami so callers can verify they're talking to this exact process")
	flag.Parse()

	store := newFixtureStore()
	store.seed()
	store.startupToken = *startupToken

	// Bind the listener up-front so we can resolve `:0` to a real port and
	// publish it before any test connects. This eliminates the false-pass
	// scenario where a fixed port is already held by a stale server: the
	// listener call fails loudly here, no zombie can answer.
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "web-fixture: listen %s failed: %v\n", *addr, err)
		os.Exit(1)
	}
	boundAddr := listener.Addr().(*net.TCPAddr)
	if *portFile != "" {
		if err := os.WriteFile(*portFile, fmt.Appendf(nil, "%d", boundAddr.Port), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "web-fixture: writing port-file %s failed: %v\n", *portFile, err)
			os.Exit(1)
		}
	}

	server := web.NewServer(web.Config{
		ListenAddr:   boundAddr.String(),
		Profile:      "fixture",
		ReadOnly:     false,
		WebMutations: *mutationsAllowed,
		MenuData:     store,
	})
	server.SetMutator(store)

	// Wrap the server's handler with the fixture admin endpoints so tests can
	// reset and inspect state without going through the real Go test harness.
	handler := server.Handler()
	mux := http.NewServeMux()
	mux.Handle("/__fixture/", store.adminHandler())
	mux.Handle("/", handler)

	httpSrv := &http.Server{
		Addr:              boundAddr.String(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "web-fixture listening on %s (pid=%d)\n", boundAddr.String(), os.Getpid())
		if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "web-fixture: received %s, shutting down\n", sig)
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(os.Stderr, "web-fixture: server error: %v\n", err)
			os.Exit(1)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	_ = server.Shutdown(ctx)
}

// fixtureStore implements both web.MenuDataLoader and web.SessionMutator
// against in-memory state. All operations are concurrency-safe.
type fixtureStore struct {
	mu           sync.Mutex
	now          func() time.Time
	profile      string
	groups       map[string]*web.MenuGroup // keyed by path
	sessions     map[string]*web.MenuSession
	order        []string // session id order
	nextID       int
	startupToken string // echoed at /__fixture/whoami for spawn verification
}

func newFixtureStore() *fixtureStore {
	return &fixtureStore{
		now:      time.Now,
		profile:  "fixture",
		groups:   make(map[string]*web.MenuGroup),
		sessions: make(map[string]*web.MenuSession),
	}
}

// seed populates the store with deterministic data so tests + screenshots
// have a stable starting point. Modify this carefully — it is the visual
// contract baseline.
func (s *fixtureStore) seed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups = map[string]*web.MenuGroup{
		"work":           {Name: "work", Path: "work", Expanded: true, Order: 0, SessionCount: 2},
		"work/innotrade": {Name: "innotrade", Path: "work/innotrade", Expanded: true, Order: 1, SessionCount: 1},
		"personal":       {Name: "personal", Path: "personal", Expanded: false, Order: 2, SessionCount: 1},
	}
	now := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC)
	s.sessions = map[string]*web.MenuSession{
		"sess-001": {
			ID: "sess-001", Title: "agent-deck", Tool: "claude",
			Status: session.StatusIdle, GroupPath: "work", ProjectPath: "/srv/agent-deck",
			Order: 0, CreatedAt: now,
		},
		"sess-002": {
			ID: "sess-002", Title: "frontend", Tool: "claude",
			Status: session.StatusRunning, GroupPath: "work", ProjectPath: "/srv/frontend",
			Order: 1, CreatedAt: now,
			// Populate tmux internals on the running session so parity-state
			// tests can verify the JSON shape carries these fields per the
			// matrix promise. Not rendered by the UI; no screenshot impact.
			TmuxSession:    "agentdeck-fixture-sess-002",
			TmuxSocketName: "agentdeck-fixture",
		},
		"sess-003": {
			ID: "sess-003", Title: "innotrade-api", Tool: "codex",
			Status: session.StatusIdle, GroupPath: "work/innotrade", ProjectPath: "/srv/innotrade-api",
			Order: 0, CreatedAt: now,
		},
		"sess-004": {
			ID: "sess-004", Title: "scratch", Tool: "shell",
			Status: session.StatusIdle, GroupPath: "personal", ProjectPath: "/home/dev/scratch",
			Order: 0, CreatedAt: now,
		},
	}
	s.order = []string{"sess-001", "sess-002", "sess-003", "sess-004"}
	s.nextID = 5
}

// LoadMenuSnapshot implements web.MenuDataLoader.
func (s *fixtureStore) LoadMenuSnapshot() (*web.MenuSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := make([]web.MenuItem, 0, len(s.groups)+len(s.sessions))
	idx := 0
	for _, g := range s.groups {
		items = append(items, web.MenuItem{
			Index: idx, Type: web.MenuItemTypeGroup, Path: g.Path, Group: g, Level: 0,
		})
		idx++
	}
	for _, id := range s.order {
		sess, ok := s.sessions[id]
		if !ok {
			continue
		}
		items = append(items, web.MenuItem{
			Index: idx, Type: web.MenuItemTypeSession, Session: sess, Level: 1,
		})
		idx++
	}

	return &web.MenuSnapshot{
		Profile:       s.profile,
		GeneratedAt:   s.now(),
		TotalGroups:   len(s.groups),
		TotalSessions: len(s.sessions),
		Items:         items,
	}, nil
}

// CreateSession implements web.SessionMutator.
func (s *fixtureStore) CreateSession(title, tool, projectPath, groupPath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := fmt.Sprintf("sess-%03d", s.nextID)
	s.nextID++
	s.sessions[id] = &web.MenuSession{
		ID: id, Title: title, Tool: tool,
		Status: session.StatusIdle, GroupPath: groupPath, ProjectPath: projectPath,
		Order: len(s.order), CreatedAt: s.now(),
	}
	s.order = append(s.order, id)
	return id, nil
}

func (s *fixtureStore) StartSession(id string) error {
	return s.transition(id, session.StatusRunning)
}

func (s *fixtureStore) StopSession(id string) error {
	return s.transition(id, session.StatusStopped)
}

func (s *fixtureStore) RestartSession(id string) error {
	return s.transition(id, session.StatusRunning)
}

func (s *fixtureStore) DeleteSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return fmt.Errorf("session %q not found", id)
	}
	delete(s.sessions, id)
	for i, oid := range s.order {
		if oid == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

func (s *fixtureStore) ForkSession(parentID string) (string, error) {
	s.mu.Lock()
	parent, ok := s.sessions[parentID]
	if !ok {
		s.mu.Unlock()
		return "", fmt.Errorf("parent session %q not found", parentID)
	}
	id := fmt.Sprintf("sess-%03d", s.nextID)
	s.nextID++
	s.sessions[id] = &web.MenuSession{
		ID: id, Title: parent.Title + " (fork)", Tool: parent.Tool,
		Status: session.StatusIdle, GroupPath: parent.GroupPath, ProjectPath: parent.ProjectPath,
		ParentSessionID: parentID, Order: len(s.order), CreatedAt: s.now(),
	}
	s.order = append(s.order, id)
	s.mu.Unlock()
	return id, nil
}

func (s *fixtureStore) CreateGroup(name, parentPath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := name
	if parentPath != "" {
		path = parentPath + "/" + name
	}
	if _, exists := s.groups[path]; exists {
		return "", fmt.Errorf("group %q already exists", path)
	}
	s.groups[path] = &web.MenuGroup{Name: name, Path: path, Order: len(s.groups)}
	return path, nil
}

func (s *fixtureStore) RenameGroup(groupPath, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupPath]
	if !ok {
		return fmt.Errorf("group %q not found", groupPath)
	}
	g.Name = newName
	return nil
}

func (s *fixtureStore) DeleteGroup(groupPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[groupPath]; !ok {
		return fmt.Errorf("group %q not found", groupPath)
	}
	delete(s.groups, groupPath)
	return nil
}

func (s *fixtureStore) transition(id string, to session.Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	sess.Status = to
	return nil
}

// adminHandler exposes /__fixture/* endpoints used by Playwright tests to
// reset, seed, or inspect store state without going through the real
// session lifecycle (which depends on tmux being present on the host).
func (s *fixtureStore) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/__fixture/whoami", func(w http.ResponseWriter, r *http.Request) {
		// Returns this binary's PID and the startup token it was launched
		// with. Lets test setup verify it is talking to the exact process
		// it spawned, not a stale server that happened to be on the port.
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pid":          os.Getpid(),
			"startupToken": s.startupToken,
		})
	})
	mux.HandleFunc("/__fixture/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		s.seed()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/__fixture/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		snap, _ := s.LoadMenuSnapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})
	mux.HandleFunc("/__fixture/session/", func(w http.ResponseWriter, r *http.Request) {
		// /__fixture/session/{id}/status?to=active
		// Lets tests force a status transition without going through start/stop
		// (e.g. simulate a TUI-side change; verify the web sees it).
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		path := r.URL.Path[len("/__fixture/session/"):]
		// id/status
		var id, action string
		if i := indexOf(path, '/'); i >= 0 {
			id = path[:i]
			action = path[i+1:]
		}
		if id == "" || action == "" {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		switch action {
		case "status":
			to := session.Status(r.URL.Query().Get("to"))
			if err := s.transition(id, to); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unknown action", http.StatusNotFound)
		}
	})
	return mux
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
