package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"golang.org/x/time/rate"
)

// Config defines runtime options for the web server.
type Config struct {
	ListenAddr          string
	Profile             string
	ReadOnly            bool
	WebMutations        bool // When false, POST/PATCH/DELETE endpoints return 403
	Token               string
	MenuData            MenuDataLoader
	PushVAPIDPublicKey  string
	PushVAPIDPrivateKey string
	PushVAPIDSubject    string
	PushTestInterval    time.Duration
}

// DefaultUndoWindow is the default Chrome-style undo grace period for
// session deletes (POST /api/sessions/undelete). Mirrors the TUI ctrl+z
// in-memory undo stack window.
const DefaultUndoWindow = 30 * time.Second

// ErrUndoNothing is returned by SessionMutator.UndoDelete when the undo
// stack is empty.
var ErrUndoNothing = errors.New("nothing to undo")

// ErrUndoExpired is returned by SessionMutator.UndoDelete when the most
// recent delete is older than the configured undo window.
var ErrUndoExpired = errors.New("undo window expired")

// ErrSessionNotFound is returned by SessionMutator.FinishWorktree when the
// target session id does not resolve to a live instance. The handler maps
// this to 404. See issue #1126.
var ErrSessionNotFound = errors.New("session not found")

// ErrNotAWorktree is returned by SessionMutator.FinishWorktree when the
// target session exists but is not in a git/jujutsu worktree (so there is
// nothing to merge or clean up). The handler maps this to 400. See issue
// #1126.
var ErrNotAWorktree = errors.New("session is not in a worktree")

// WorktreeFinishOptions configures a SessionMutator.FinishWorktree call.
// All fields are optional; the zero value asks the implementation to
// auto-detect the target branch, perform the merge, delete the source
// branch, and refuse if the worktree is dirty.
type WorktreeFinishOptions struct {
	// Into is the target branch to merge the worktree branch into. When
	// empty the backend's default branch is used (matches the
	// `agent-deck worktree finish --into` flag).
	Into string
	// NoMerge skips the merge step (mirrors --no-merge). The branch is
	// still removed (unless KeepBranch) and the worktree torn down.
	NoMerge bool
	// KeepBranch leaves the source branch in place after finishing
	// (mirrors --keep-branch). Useful when the branch already lives on a
	// remote PR.
	KeepBranch bool
	// Force skips the dirty-worktree safety check and forces branch
	// deletion even if the merge fast-forward didn't succeed.
	Force bool
}

// WorktreeFinishResult is what SessionMutator.FinishWorktree returns on
// success. Mirrors the JSON-output payload of `agent-deck worktree finish
// --json`.
type WorktreeFinishResult struct {
	SessionID     string
	Branch        string
	MergedInto    string
	Merged        bool
	BranchDeleted bool
}

// MenuDataLoader provides menu snapshots for web APIs and push notifications.
type MenuDataLoader interface {
	LoadMenuSnapshot() (*MenuSnapshot, error)
}

// SessionMutator is implemented by internal/ui.WebMutator and injected at startup.
// It bridges web HTTP handlers to the TUI session/group management methods.
type SessionMutator interface {
	CreateSession(title, tool, projectPath, groupPath, modelID string) (string, error)
	StartSession(sessionID string) error
	StopSession(sessionID string) error
	RestartSession(sessionID string) error
	DeleteSession(sessionID string) error
	// CloseSession stops the session process while keeping its metadata
	// in storage (TUI Shift+D — non-destructive close).
	CloseSession(sessionID string) error
	ForkSession(sessionID string) (string, error)
	// UndoDelete restores the most-recently deleted session if it was
	// deleted within the implementation's undo window. Returns the
	// restored session id. Implementations should return ErrUndoNothing
	// when the stack is empty and ErrUndoExpired when the most recent
	// entry is older than the window — the handler maps both to 404.
	UndoDelete() (string, error)
	CreateGroup(name, parentPath string) (string, error)
	RenameGroup(groupPath, newName string) error
	DeleteGroup(groupPath string) error
	// FinishWorktree merges (or skips), removes the worktree, optionally
	// deletes the source branch, kills the tmux session, and removes the
	// session from storage. Mirrors the TUI W/shift+w hotkey and the
	// `agent-deck worktree finish` CLI. Returns ErrSessionNotFound when
	// the id doesn't resolve and ErrNotAWorktree when the session exists
	// but lacks worktree metadata. See issue #1126.
	FinishWorktree(sessionID string, opts WorktreeFinishOptions) (WorktreeFinishResult, error)
}

// Server wraps an HTTP server for Agent Deck web mode.
type Server struct {
	cfg         Config
	httpServer  *http.Server
	menuData    MenuDataLoader
	push        pushServiceAPI
	baseCtx     context.Context
	cancelBase  context.CancelFunc
	hookWatcher *session.StatusFileWatcher

	menuSubscribersMu sync.Mutex
	menuSubscribers   map[chan struct{}]struct{}

	costStore       *costs.Store
	mutator         SessionMutator
	skills          SkillsService
	mcpMgr          MCPManager
	mutationLimiter *rate.Limiter

	// hookStatusLoader returns the latest hook payload for every instance
	// whose hook file is present on disk. Defaults to defaultLoadHookStatuses
	// (which reads ~/.agent-deck/hooks/) but is injectable for tests.
	hookStatusLoader func() map[string]*session.HookStatus
}

// NewServer creates a new web server with base routes and middleware.
func NewServer(cfg Config) *Server {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8420"
	}

	menuData := cfg.MenuData
	if menuData == nil {
		menuData = NewSessionDataService(cfg.Profile)
	}

	s := &Server{
		cfg:              cfg,
		menuData:         menuData,
		menuSubscribers:  make(map[chan struct{}]struct{}),
		mutationLimiter:  rate.NewLimiter(rate.Limit(20), 40), // 20 req/s, burst 40
		hookStatusLoader: defaultLoadHookStatuses,
	}
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	webLog := logging.ForComponent(logging.CompWeb)
	if pushSvc, err := newPushService(cfg, menuData); err != nil {
		webLog.Warn("push_disabled", slog.String("error", err.Error()))
	} else {
		s.push = pushSvc
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/s/", s.handleIndex)
	mux.HandleFunc("/manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("/sw.js", s.handleServiceWorker)
	mux.Handle("/static/", gzipAndCacheStatic(http.StripPrefix("/static/", s.staticFileServer())))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		resp := map[string]any{
			"ok":           true,
			"profile":      cfg.Profile,
			"readOnly":     cfg.ReadOnly,
			"webMutations": cfg.WebMutations,
			"version":      buildVersion(),
			"time":         time.Now().UTC().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/menu", s.handleMenu)
	mux.HandleFunc("/api/session/", s.handleSessionByID)
	mux.HandleFunc("/api/sessions", s.handleSessionsCollection)
	// /api/sessions/undelete is a collection-level action (Chrome-style
	// ctrl+z undo). Register before the subtree pattern so Go 1.22+
	// ServeMux precedence routes it cleanly instead of treating
	// "undelete" as a sessionID.
	mux.HandleFunc("POST /api/sessions/undelete", s.handleSessionUndelete)
	mux.HandleFunc("/api/sessions/", s.handleSessionByAction)
	mux.HandleFunc("/api/groups", s.handleGroupsCollection)
	mux.HandleFunc("/api/groups/", s.handleGroupByPath)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/profiles", s.handleProfiles)
	mux.HandleFunc("/api/push/config", s.handlePushConfig)
	mux.HandleFunc("/api/push/subscribe", s.handlePushSubscribe)
	mux.HandleFunc("/api/push/unsubscribe", s.handlePushUnsubscribe)
	mux.HandleFunc("/api/push/presence", s.handlePushPresence)
	mux.HandleFunc("/events/menu", s.handleMenuEvents)
	mux.HandleFunc("/ws/session/", s.handleSessionWS)

	mux.HandleFunc("/api/costs/summary", s.handleCostsSummary)
	mux.HandleFunc("/api/costs/daily", s.handleCostsDaily)
	mux.HandleFunc("/api/costs/sessions", s.handleCostsSessions)
	mux.HandleFunc("/api/costs/models", s.handleCostsModels)
	mux.HandleFunc("/api/costs/export", s.handleCostsExport)
	mux.HandleFunc("/api/costs/groups", s.handleCostsGroups)
	mux.HandleFunc("/api/costs/session", s.handleCostsSessionDetail)
	mux.HandleFunc("/api/costs/batch", s.handleCostsBatch)
	mux.HandleFunc("/api/costs/stream", s.handleCostsStream)

	mux.HandleFunc("/api/system/stats", s.handleSystemStats)

	mux.HandleFunc("/api/skills", s.handleSkillsCatalog)

	// MCP management (Web UI parity with TUI `m` key dialog). Closes the
	// four MISSING rows under "MCP MANAGEMENT" in PARITY_MATRIX.md.
	mux.HandleFunc("/api/mcps", s.handleMCPsCatalog)
	mux.HandleFunc("GET /api/sessions/{id}/mcps", s.handleSessionMCPsRouter)
	mux.HandleFunc("POST /api/sessions/{id}/mcps/{name}", s.handleSessionMCPsRouter)
	mux.HandleFunc("DELETE /api/sessions/{id}/mcps/{name}", s.handleSessionMCPsRouter)
	mux.HandleFunc("PATCH /api/sessions/{id}/mcps/{name}", s.handleSessionMCPsRouter)

	handler := withRecover(csrfProtect(mux))

	s.httpServer = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		BaseContext:       func(_ net.Listener) context.Context { return s.baseCtx },
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return s
}

// Addr returns the listen address.
func (s *Server) Addr() string {
	return s.httpServer.Addr
}

// Handler returns the configured HTTP handler (used by tests).
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// Start starts the HTTP server and blocks until shutdown or error.
// Returns nil on graceful shutdown.
func (s *Server) Start() error {
	webLog := logging.ForComponent(logging.CompWeb)
	if watcher, err := session.NewStatusFileWatcher(func() {
		s.notifyMenuChanged()
		if s.push != nil {
			s.push.TriggerSync()
		}
	}); err != nil {
		webLog.Warn("hooks_watcher_disabled", slog.String("error", err.Error()))
	} else {
		s.hookWatcher = watcher
		go watcher.Start()
	}

	if s.push != nil {
		s.push.Start(s.baseCtx)
	}
	err := s.httpServer.ListenAndServe()
	if s.hookWatcher != nil {
		s.hookWatcher.Stop()
		s.hookWatcher = nil
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.cancelBase != nil {
		// Signal long-lived handlers (SSE/WS) to stop promptly.
		s.cancelBase()
	}
	if s.hookWatcher != nil {
		s.hookWatcher.Stop()
		s.hookWatcher = nil
	}

	err := s.httpServer.Shutdown(ctx)
	if err == nil {
		return nil
	}

	// Long-lived connections may still block graceful shutdown. Force close
	// as a fallback so Ctrl+C exits promptly.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if closeErr := s.httpServer.Close(); closeErr == nil {
			return nil
		} else {
			return fmt.Errorf("graceful shutdown timed out and force close failed: %w", closeErr)
		}
	}

	return err
}

func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logging.ForComponent(logging.CompWeb).Error("panic",
					slog.String("recover", fmt.Sprintf("%v", rec)),
					slog.String("path", r.URL.Path))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) String() string {
	return fmt.Sprintf("web-server(addr=%s, profile=%s, readOnly=%t)", s.cfg.ListenAddr, s.cfg.Profile, s.cfg.ReadOnly)
}

func (s *Server) subscribeMenuChanges() chan struct{} {
	ch := make(chan struct{}, 1)
	s.menuSubscribersMu.Lock()
	s.menuSubscribers[ch] = struct{}{}
	s.menuSubscribersMu.Unlock()
	return ch
}

func (s *Server) unsubscribeMenuChanges(ch chan struct{}) {
	if ch == nil {
		return
	}
	s.menuSubscribersMu.Lock()
	if _, ok := s.menuSubscribers[ch]; ok {
		delete(s.menuSubscribers, ch)
		close(ch)
	}
	s.menuSubscribersMu.Unlock()
}

func (s *Server) SetCostStore(store *costs.Store) {
	s.costStore = store
}

// SetMutator injects the session mutator implementation (typically *ui.WebMutator).
func (s *Server) SetMutator(m SessionMutator) {
	s.mutator = m
}

// SetSkillsService injects an alternate SkillsService (used by tests).
// When nil, handlers fall back to defaultSkillsService.
func (s *Server) SetSkillsService(svc SkillsService) {
	s.skills = svc
}

// HasMutator reports whether a SessionMutator has been wired. Mutating
// endpoints (POST/PATCH/DELETE) return 503 NOT_IMPLEMENTED when this is
// false, even if WebMutations is true. Exposed for regression tests on the
// `agent-deck web` bootstrap path.
func (s *Server) HasMutator() bool {
	return s.mutator != nil
}

func (s *Server) notifyMenuChanged() {
	s.menuSubscribersMu.Lock()
	for ch := range s.menuSubscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.menuSubscribersMu.Unlock()
}

// checkMutationsAllowed writes a 403 response and returns false when web mutations are disabled.
func (s *Server) checkMutationsAllowed(w http.ResponseWriter) bool {
	if !s.cfg.WebMutations {
		writeAPIError(w, http.StatusForbidden, ErrCodeForbidden, "web mutations are disabled")
		return false
	}
	return true
}

// checkMutationRateLimit writes a 429 response and returns false when the rate limit is exceeded.
func (s *Server) checkMutationRateLimit(w http.ResponseWriter) bool {
	if !s.mutationLimiter.Allow() {
		writeAPIError(w, http.StatusTooManyRequests, ErrCodeRateLimited, "too many requests")
		return false
	}
	return true
}
