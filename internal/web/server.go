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

	"github.com/asheshgoplani/agent-deck/internal/hub"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Config defines runtime options for the web server.
type Config struct {
	ListenAddr          string
	Profile             string
	ReadOnly            bool
	Token               string
	MenuData            MenuDataLoader
	PushVAPIDPublicKey  string
	PushVAPIDPrivateKey string
	PushVAPIDSubject    string
	PushTestInterval    time.Duration
}

// MenuDataLoader provides menu snapshots for web APIs and push notifications.
type MenuDataLoader interface {
	LoadMenuSnapshot() (*MenuSnapshot, error)
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

	// Hub dashboard state.
	hubTasks        *hub.TaskStore
	hubProjects     *hub.ProjectRegistry
	containerExec   hub.ContainerExecutor
	sessionLauncher *hub.SessionLauncher

	taskSubscribersMu sync.Mutex
	taskSubscribers   map[chan struct{}]struct{}
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
		taskSubscribers:  make(map[chan struct{}]struct{}),
	}
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	webLog := logging.ForComponent(logging.CompWeb)

	// Initialize hub task store and project registry.
	if hubDir, err := hub.GetHubDir(cfg.Profile); err != nil {
		webLog.Warn("hub_disabled", slog.String("error", err.Error()))
	} else {
		if ts, err := hub.NewTaskStore(hubDir); err != nil {
			webLog.Warn("hub_task_store_disabled", slog.String("error", err.Error()))
		} else {
			s.hubTasks = ts
		}
		s.hubProjects = hub.NewProjectRegistry(hubDir)
	}

	// Initialize container executor for Docker-based task execution.
	s.containerExec = &hub.DockerExecutor{}
	s.sessionLauncher = &hub.SessionLauncher{Executor: s.containerExec}

	if pushSvc, err := newPushService(cfg, menuData); err != nil {
		webLog.Warn("push_disabled", slog.String("error", err.Error()))
	} else {
		s.push = pushSvc
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/s/", s.handleIndex)
	mux.HandleFunc("/terminal", s.handleTerminal)
	mux.HandleFunc("/manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("/sw.js", s.handleServiceWorker)
	mux.Handle("/static/", http.StripPrefix("/static/", s.staticFileServer()))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		resp := map[string]any{
			"ok":       true,
			"profile":  cfg.Profile,
			"readOnly": cfg.ReadOnly,
			"time":     time.Now().UTC().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/menu", s.handleMenu)
	mux.HandleFunc("/api/session/", s.handleSessionByID)
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/tasks/", s.handleTaskByID)
	mux.HandleFunc("/api/projects", s.handleProjects)
	mux.HandleFunc("/api/route", s.handleRoute)
	mux.HandleFunc("/api/push/config", s.handlePushConfig)
	mux.HandleFunc("/api/push/subscribe", s.handlePushSubscribe)
	mux.HandleFunc("/api/push/unsubscribe", s.handlePushUnsubscribe)
	mux.HandleFunc("/api/push/presence", s.handlePushPresence)
	mux.HandleFunc("/events/menu", s.handleMenuEvents)
	mux.HandleFunc("/ws/session/", s.handleSessionWS)

	handler := withRecover(mux)

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

func (s *Server) subscribeTaskChanges() chan struct{} {
	ch := make(chan struct{}, 1)
	s.taskSubscribersMu.Lock()
	s.taskSubscribers[ch] = struct{}{}
	s.taskSubscribersMu.Unlock()
	return ch
}

func (s *Server) unsubscribeTaskChanges(ch chan struct{}) {
	if ch == nil {
		return
	}
	s.taskSubscribersMu.Lock()
	if _, ok := s.taskSubscribers[ch]; ok {
		delete(s.taskSubscribers, ch)
		close(ch)
	}
	s.taskSubscribersMu.Unlock()
}

// notifyTaskChanged broadcasts to SSE subscribers that task data changed.
func (s *Server) notifyTaskChanged() {
	s.taskSubscribersMu.Lock()
	for ch := range s.taskSubscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.taskSubscribersMu.Unlock()
}
