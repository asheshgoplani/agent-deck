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
	// Hold the MCP manager on the store so /__fixture/reset clears its
	// in-memory attachments too. Without this, attachments leak across the
	// serially-run mcps.spec.js cases (and across Playwright retries, which
	// re-run the whole serial block), breaking the "fresh fixture" and
	// "un-attached → 404" assertions.
	store.mcpMgr = newFixtureMCPManager()
	server.SetMCPManager(store.mcpMgr)
	// Without this, GET /api/skills falls back to defaultSkillsService, which
	// scans the host's real ~/.agent-deck/skills + ~/.claude/skills via
	// session.ListAvailableSkills() — so the seeded alpha/beta/gamma catalog is
	// never served and skills.spec.js fails (empty/host-dependent catalog).
	server.SetSkillsService(store)

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
	catalog      []session.SkillCandidate
	attached     map[string][]session.ProjectSkillAttachment // by projectPath
	mcpMgr       *fixtureMCPManager                          // reset alongside the store on /__fixture/reset

	// undoStack tracks recently-deleted sessions for ctrl+z undo. Capped
	// at 10 entries (FIFO eviction) to match the TUI Home.undoStack.
	undoStack  []fixtureDeletedEntry
	undoWindow time.Duration // 0 → web.DefaultUndoWindow
}

type fixtureDeletedEntry struct {
	session   *web.MenuSession
	deletedAt time.Time
}

func newFixtureStore() *fixtureStore {
	return &fixtureStore{
		now:      time.Now,
		profile:  "fixture",
		groups:   make(map[string]*web.MenuGroup),
		sessions: make(map[string]*web.MenuSession),
		attached: make(map[string][]session.ProjectSkillAttachment),
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
	yoloTrue := true
	sandboxCPU := "2.0"
	s.sessions = map[string]*web.MenuSession{
		"sess-001": {
			ID: "sess-001", Title: "agent-deck", Tool: "claude",
			Status: session.StatusIdle, GroupPath: "work", ProjectPath: "/srv/agent-deck",
			Order: 0, CreatedAt: now,
			// Populate every promoted MenuSession field on a single session so
			// parity-state's "at least one session carries this key" assertion
			// passes for every row promoted out of MISSING in PARITY_MATRIX.md.
			// Not rendered by the UI; no screenshot impact.
			IsConductor:       true,
			ClaudeSessionID:   "fixture-claude-sess-001",
			GeminiSessionID:   "fixture-gemini-sess-001",
			GeminiModel:       "gemini-2.5-pro",
			GeminiYoloMode:    &yoloTrue,
			CodexSessionID:    "fixture-codex-sess-001",
			OpenCodeSessionID: "fixture-opencode-sess-001",
			LatestPrompt:      "what's the next step?",
			Notes:             "fixture notes for parity tests",
			Color:             "#ff8800",
			Command:           "claude --resume fixture-claude-sess-001",
			Wrapper:           "env FOO=bar {command}",
			Channels:          []string{"plugin:telegram@user/repo"},
			ExtraArgs:         []string{"--agent", "reviewer"},
			ToolOptionsJSON:   json.RawMessage(`{"tool":"claude","options":{"agent":"reviewer"}}`),
			Sandbox: &session.SandboxConfig{
				Enabled:  true,
				Image:    "ghcr.io/asheshgoplani/agent-deck-sandbox:latest",
				CPULimit: &sandboxCPU,
			},
			SandboxContainer:   "agent-deck-sbx-sess-001",
			SSHHost:            "remote.example",
			SSHRemotePath:      "/srv/remote-agent-deck",
			MultiRepoEnabled:   true,
			AdditionalPaths:    []string{"/srv/lib", "/srv/api"},
			MultiRepoTempDir:   "/tmp/multi-repo-sess-001",
			MultiRepoWorktrees: []session.MultiRepoWorktree{{OriginalPath: "/srv/agent-deck", WorktreePath: "/tmp/wt/sess-001", RepoRoot: "/srv/agent-deck", Branch: "feat/fixture"}},
			WorktreePath:       "/tmp/worktrees/sess-001",
			WorktreeRepoRoot:   "/srv/agent-deck",
			WorktreeBranch:     "feat/fixture",
			TitleLocked:        true,
			NoTransitionNotify: true,
			LoadedMCPNames:     []string{"exa", "filesystem"},
			GeminiAnalytics: &session.GeminiSessionAnalytics{
				InputTokens: 100, OutputTokens: 200, Model: "gemini-2.5-pro",
			},
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
	s.catalog = []session.SkillCandidate{
		{ID: "pool/alpha", Name: "alpha", Source: "pool", EntryName: "alpha", Kind: "dir", Description: "Alpha test skill"},
		{ID: "pool/beta", Name: "beta", Source: "pool", EntryName: "beta", Kind: "dir", Description: "Beta test skill"},
		{ID: "pool/gamma", Name: "gamma", Source: "pool", EntryName: "gamma", Kind: "dir", Description: "Gamma test skill"},
	}
	s.attached = map[string][]session.ProjectSkillAttachment{
		"/srv/agent-deck": {
			{ID: "pool/alpha", Name: "alpha", Source: "pool", EntryName: "alpha", TargetPath: ".claude/skills/alpha"},
		},
	}
	s.undoStack = nil
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
	active := 0
	for _, id := range s.order {
		sess, ok := s.sessions[id]
		if !ok || !sess.ArchivedAt.IsZero() {
			continue
		}
		active++
		items = append(items, web.MenuItem{
			Index: idx, Type: web.MenuItemTypeSession, Session: sess, Level: 1,
		})
		idx++
	}

	return &web.MenuSnapshot{
		Profile:       s.profile,
		GeneratedAt:   s.now(),
		TotalGroups:   len(s.groups),
		TotalSessions: active,
		Items:         items,
	}, nil
}

// LoadArchivedMenuSnapshot implements the optional archived-only menu loader.
func (s *fixtureStore) LoadArchivedMenuSnapshot() (*web.MenuSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := make([]web.MenuItem, 0)
	idx := 0
	archived := 0
	for _, id := range s.order {
		sess, ok := s.sessions[id]
		if !ok || sess.ArchivedAt.IsZero() {
			continue
		}
		archived++
		items = append(items, web.MenuItem{
			Index: idx, Type: web.MenuItemTypeSession, Session: sess, Level: 0,
		})
		idx++
	}
	return &web.MenuSnapshot{
		Profile:       s.profile,
		GeneratedAt:   s.now(),
		TotalSessions: archived,
		Items:         items,
	}, nil
}

// CreateSession implements web.SessionMutator.
func (s *fixtureStore) CreateSession(title, tool, projectPath, groupPath, modelID string) (string, error) {
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
	sess, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	// Snapshot the session for undo BEFORE removing from primary state.
	s.undoStack = append(s.undoStack, fixtureDeletedEntry{
		session:   sess,
		deletedAt: s.now(),
	})
	if len(s.undoStack) > 10 {
		s.undoStack = s.undoStack[len(s.undoStack)-10:]
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

// CloseSession mirrors the TUI's Shift+D handler: stop the session but
// keep its metadata in storage (web parity row "Close session").
func (s *fixtureStore) CloseSession(id string) error {
	return s.transition(id, session.StatusStopped)
}

func (s *fixtureStore) ArchiveSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	sess.Status = session.StatusStopped
	sess.ArchivedAt = s.now()
	return nil
}

func (s *fixtureStore) UnarchiveSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if sess.ArchivedAt.IsZero() {
		return fmt.Errorf("session is not archived: %s", id)
	}
	sess.ArchivedAt = time.Time{}
	return nil
}

// UndoDelete restores the most-recently deleted session if its delete
// was within s.undoWindow (default web.DefaultUndoWindow).
func (s *fixtureStore) UndoDelete() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.undoStack) == 0 {
		return "", web.ErrUndoNothing
	}
	entry := s.undoStack[len(s.undoStack)-1]
	s.undoStack = s.undoStack[:len(s.undoStack)-1]
	window := s.undoWindow
	if window == 0 {
		window = web.DefaultUndoWindow
	}
	if s.now().Sub(entry.deletedAt) > window {
		return "", web.ErrUndoExpired
	}
	restored := *entry.session
	restored.Status = session.StatusStopped
	s.sessions[restored.ID] = &restored
	s.order = append(s.order, restored.ID)
	return restored.ID, nil
}

// UpdateSession implements web.SessionMutator. Mirrors the production path:
// validates field names against a small allowlist and applies the value to
// the in-memory MenuSession DTO. Restart-required fields are tracked with
// the same policy as session.RestartPolicyFor so e2e tests can assert the
// restartRequired flag without booting a real session.
func (s *fixtureStore) UpdateSession(id string, updates map[string]string) ([]string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, false, fmt.Errorf("session not found: %s", id)
	}
	changed := make([]string, 0, len(updates))
	restartRequired := false
	for field, value := range updates {
		var oldValue string
		switch field {
		case session.FieldTitle:
			oldValue = sess.Title
			sess.Title = value
		case session.FieldTool:
			oldValue = sess.Tool
			sess.Tool = value
		case session.FieldNotes, session.FieldColor, session.FieldExtraArgs,
			session.FieldPlugins, session.FieldChannels,
			session.FieldSkipPermissions, session.FieldAutoMode:
			// Fixture DTO doesn't carry these; treat as accepted no-op so
			// tests can verify the round-trip without expanding MenuSession.
			oldValue = value
		default:
			return nil, false, fmt.Errorf("invalid field: %s", field)
		}
		if oldValue == value {
			continue
		}
		changed = append(changed, field)
		if session.RestartPolicyFor(field) == session.FieldRestartRequired {
			restartRequired = true
		}
	}
	return changed, restartRequired, nil
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

// FinishWorktree implements web.SessionMutator for issue #1126. Without a
// real git backend the fixture validates inputs the same way the live
// path does (session exists, worktree fields populated) and then removes
// the session deterministically so e2e tests can verify the menu refresh.
func (s *fixtureStore) FinishWorktree(id string, opts web.WorktreeFinishOptions) (web.WorktreeFinishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return web.WorktreeFinishResult{}, web.ErrSessionNotFound
	}
	if sess.WorktreeBranch == "" || sess.WorktreeRepoRoot == "" {
		return web.WorktreeFinishResult{}, web.ErrNotAWorktree
	}
	branch := sess.WorktreeBranch
	merged := !opts.NoMerge
	mergedInto := opts.Into
	if merged && mergedInto == "" {
		mergedInto = "main"
	}
	if !merged {
		mergedInto = ""
	}
	branchDeleted := !opts.KeepBranch
	delete(s.sessions, id)
	for i, x := range s.order {
		if x == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return web.WorktreeFinishResult{
		SessionID:     id,
		Branch:        branch,
		MergedInto:    mergedInto,
		Merged:        merged,
		BranchDeleted: branchDeleted,
	}, nil
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
		if s.mcpMgr != nil {
			s.mcpMgr.Reset()
		}
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

// --- web.SkillsService -----------------------------------------------------

// ListCatalog implements web.SkillsService.
func (s *fixtureStore) ListCatalog() ([]session.SkillCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]session.SkillCandidate, len(s.catalog))
	copy(out, s.catalog)
	return out, nil
}

// ListAttached implements web.SkillsService.
func (s *fixtureStore) ListAttached(projectPath string) ([]session.ProjectSkillAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]session.ProjectSkillAttachment, len(s.attached[projectPath]))
	copy(out, s.attached[projectPath])
	return out, nil
}

// Attach implements web.SkillsService.
func (s *fixtureStore) Attach(projectPath, tool, skillRef, source string) (*session.ProjectSkillAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var match *session.SkillCandidate
	for i := range s.catalog {
		c := s.catalog[i]
		if (source == "" || c.Source == source) && (c.Name == skillRef || c.ID == skillRef || c.EntryName == skillRef) {
			match = &c
			break
		}
	}
	if match == nil {
		return nil, fmt.Errorf("%w: %s", session.ErrSkillNotFound, skillRef)
	}
	for _, a := range s.attached[projectPath] {
		if a.ID == match.ID {
			return nil, session.ErrSkillAlreadyAttached
		}
	}
	att := session.ProjectSkillAttachment{
		ID: match.ID, Name: match.Name, Source: match.Source,
		EntryName: match.EntryName, TargetPath: ".claude/skills/" + match.EntryName,
	}
	s.attached[projectPath] = append(s.attached[projectPath], att)
	return &att, nil
}

// Detach implements web.SkillsService.
func (s *fixtureStore) Detach(projectPath, skillRef, source string) (*session.ProjectSkillAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.attached[projectPath]
	for i, a := range list {
		if (source == "" || a.Source == source) && (a.Name == skillRef || a.ID == skillRef || a.EntryName == skillRef) {
			removed := a
			s.attached[projectPath] = append(list[:i], list[i+1:]...)
			return &removed, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", session.ErrSkillNotAttached, skillRef)
}
