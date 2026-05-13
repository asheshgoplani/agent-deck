package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// EngineConfig holds the configuration for the watcher Engine.
type EngineConfig struct {
	// DB is the state database for event persistence and dedup.
	DB *statedb.StateDB

	// Router routes events to conductors based on sender.
	Router *Router

	// MaxEventsPerWatcher limits the number of stored events per watcher (pruning threshold).
	MaxEventsPerWatcher int

	// HealthCheckInterval controls how often adapter health checks run.
	// Set to 0 to disable the health check loop (useful in tests).
	HealthCheckInterval time.Duration

	// Logger is the structured logger. Defaults to logging.ForComponent(logging.CompWatcher).
	Logger *slog.Logger

	// TriageSpawner launches triage sessions for unrouted events (INTEL-01, D-25).
	// Defaults to AgentDeckLaunchSpawner when nil.
	TriageSpawner TriageSpawner

	// Clock abstracts time for the rate limiter and reaper (D-26).
	// Defaults to realClock{} when nil.
	Clock Clock

	// TriageDir is the directory where triage session results are written.
	// Defaults to $HOME/.agent-deck/triage/ when empty.
	TriageDir string

	// ClientsPath is the path to clients.json for triage hot-reload.
	// Defaults to $HOME/.agent-deck/watcher/clients.json when empty.
	ClientsPath string

	// Profile is the agent-deck profile flag passed to spawned triage sessions.
	// Defaults to AGENTDECK_PROFILE env var, then "default".
	Profile string
}

// eventEnvelope wraps an Event with metadata for the single-writer goroutine.
// This avoids modifying the public Event struct with internal routing fields.
type eventEnvelope struct {
	event     Event
	watcherID string
	tracker   *HealthTracker
}

// adapterEntry holds a registered adapter with its associated metadata.
type adapterEntry struct {
	adapter   WatcherAdapter
	config    AdapterConfig
	watcherID string
	tracker   *HealthTracker
	cancel    context.CancelFunc
}

// Engine orchestrates the watcher event pipeline: adapter goroutines produce Events,
// a single-writer goroutine serializes DB writes via a buffered channel, dedup is
// handled by INSERT OR IGNORE, and the router determines event routing.
//
// Lifecycle: NewEngine -> RegisterAdapter (1..N) -> Start -> Stop.
type Engine struct {
	cfg      EngineConfig
	adapters []adapterEntry

	// eventCh is the internal channel from adapter goroutines to the single-writer.
	// Capacity 64 per D-12 / T-13-06.
	eventCh chan eventEnvelope

	// routedEventCh is the exported channel for TUI consumption (D-20).
	// Successfully persisted events are forwarded here.
	routedEventCh chan Event

	// healthCh is the exported channel for health state updates (D-20).
	healthCh chan HealthState

	// triageReqCh receives unrouted events from writerLoop for triage spawning (D-05).
	// Cap TriageReqChCap (16). writerLoop does a non-blocking send.
	triageReqCh chan TriageRequest

	// triageQueue holds rate-limited requests waiting for window eviction (D-10).
	// Cap TriageQueueCap (16).
	triageQueue chan TriageRequest

	// rateLim is the rolling-window rate limiter for triage spawning (D-10a).
	// Only accessed from triageLoop (no lock needed).
	rateLim *rateLimiter

	// clock abstracts time for deterministic tests (D-26).
	clock Clock

	// reaper processes triage result files (D-08). Set by Start().
	reaper *triageReaper

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    *slog.Logger

	// stopOnce ensures Stop() is idempotent — exported channels are closed
	// at most once even when Stop is called multiple times (V1.9 T5,
	// critical-hunt #9).
	stopOnce sync.Once
}

// NewEngine creates a new Engine with the provided configuration.
// Call RegisterAdapter to add adapters, then Start to begin processing.
func NewEngine(cfg EngineConfig) *Engine {
	logger := cfg.Logger
	if logger == nil {
		logger = logging.ForComponent(logging.CompWatcher)
	}

	// Apply defaults for triage configuration.
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.TriageDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.TriageDir = filepath.Join(home, ".agent-deck", "triage")
		}
	}
	if cfg.ClientsPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			// Singular "watcher" per REQ-WF-6. Legacy "watchers/" is served via compatibility symlink created by MigrateLegacyWatchersDir.
			cfg.ClientsPath = filepath.Join(home, ".agent-deck", "watcher", "clients.json")
		}
	}
	if cfg.Profile == "" {
		cfg.Profile = os.Getenv("AGENTDECK_PROFILE")
		if cfg.Profile == "" {
			cfg.Profile = "default"
		}
	}
	if cfg.TriageSpawner == nil {
		cfg.TriageSpawner = AgentDeckLaunchSpawner{} // BinaryPath resolved lazily at spawn time
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Engine{
		cfg:           cfg,
		adapters:      make([]adapterEntry, 0),
		eventCh:       make(chan eventEnvelope, 64),
		routedEventCh: make(chan Event, 64),
		healthCh:      make(chan HealthState, 16),
		triageReqCh:   make(chan TriageRequest, TriageReqChCap),
		triageQueue:   make(chan TriageRequest, TriageQueueCap),
		rateLim:       &rateLimiter{},
		clock:         cfg.Clock,
		ctx:           ctx,
		cancel:        cancel,
		log:           logger,
	}
}

// RegisterAdapter adds an adapter to the engine. Must be called before Start.
// watcherID is the statedb watcher ID used for SaveWatcherEvent persistence.
// maxSilenceMinutes is the threshold for silence detection in the health tracker.
func (e *Engine) RegisterAdapter(watcherID string, adapter WatcherAdapter, config AdapterConfig, maxSilenceMinutes int) {
	tracker := NewHealthTracker(config.Name, maxSilenceMinutes)
	e.adapters = append(e.adapters, adapterEntry{
		adapter:   adapter,
		config:    config,
		watcherID: watcherID,
		tracker:   tracker,
	})
}

// Start begins the event pipeline. For each registered adapter, it calls Setup,
// then launches an adapter goroutine. It also starts the single-writer goroutine
// and optionally the health check loop.
func (e *Engine) Start() error {
	// Migrate legacy watchers/ dir and scaffold watcher/ layout on every boot.
	// Non-fatal: log and continue so a filesystem error never prevents event processing.
	if err := MigrateLegacyWatchersDir(); err != nil {
		e.log.Warn("watcher_migration_failed", slog.String("error", err.Error()))
	}
	if err := ScaffoldWatcherLayout(); err != nil {
		e.log.Warn("watcher_scaffold_failed", slog.String("error", err.Error()))
	}

	for i := range e.adapters {
		entry := &e.adapters[i]

		if err := entry.adapter.Setup(e.ctx, entry.config); err != nil {
			e.log.Warn("adapter_setup_failed",
				slog.String("watcher", entry.config.Name),
				slog.String("type", entry.config.Type),
				slog.String("error", err.Error()),
			)
			continue
		}

		adapterCtx, adapterCancel := context.WithCancel(e.ctx)
		entry.cancel = adapterCancel

		e.wg.Add(1)
		go e.runAdapter(adapterCtx, entry)
	}

	// Single-writer goroutine serializes all DB writes (D-13).
	e.wg.Add(1)
	go e.writerLoop()

	// Health check loop (optional, disabled when HealthCheckInterval is 0).
	if e.cfg.HealthCheckInterval > 0 {
		e.wg.Add(1)
		go e.healthLoop()
	}

	// Triage goroutines: create reaper BEFORE launching triageLoop so spawnTriage
	// can safely reference e.reaper without a data race (D-05/D-08).
	e.reaper = newTriageReaper(
		e.ctx, &e.wg, e.clock,
		e.cfg.TriageDir, e.cfg.ClientsPath,
		e.cfg.Router, e.cfg.DB, e.log,
	)

	e.wg.Add(1)
	go e.triageLoop()

	e.wg.Add(1)
	go e.reaper.loop()

	return nil
}

// runAdapter runs a single adapter's Listen loop in its own goroutine.
// Events are wrapped in envelopes and sent to the single-writer via eventCh.
func (e *Engine) runAdapter(ctx context.Context, entry *adapterEntry) {
	defer e.wg.Done()

	// Create an intermediary channel for the adapter to send events to.
	// We wrap each event in an envelope before forwarding to the engine's eventCh.
	adapterCh := make(chan Event, 64)

	// Launch a goroutine to forward events from the adapter channel to the engine channel.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range adapterCh {
			env := eventEnvelope{
				event:     evt,
				watcherID: entry.watcherID,
				tracker:   entry.tracker,
			}
			// Non-blocking send to prevent adapter goroutine hang if channel full (T-13-06).
			select {
			case e.eventCh <- env:
			default:
				e.log.Warn("event_channel_full",
					slog.String("watcher", entry.config.Name),
					slog.String("sender", evt.Sender),
				)
			}
		}
	}()

	err := entry.adapter.Listen(ctx, adapterCh)
	close(adapterCh)
	<-done

	if err != nil && ctx.Err() == nil {
		e.log.Error("adapter_listen_error",
			slog.String("watcher", entry.config.Name),
			slog.String("error", err.Error()),
		)
		entry.tracker.RecordError()
	}
}

// writerLoop is the single-writer goroutine that serializes all DB writes (D-13).
// It reads event envelopes from eventCh, performs dedup via SaveWatcherEvent,
// routes events, updates health trackers, and forwards persisted events to routedEventCh.
func (e *Engine) writerLoop() {
	defer e.wg.Done()

	for {
		select {
		case env, ok := <-e.eventCh:
			if !ok {
				return
			}

			// Route the event via the router (D-08).
			var routedTo string
			if e.cfg.Router != nil {
				result := e.cfg.Router.Match(env.event.Sender)
				if result != nil {
					routedTo = result.Conductor
				}
			}

			// Thread reply routing (D-07): if the event has a ParentDedupKey,
			// look up the parent's session_id and route the reply to the same session.
			var threadSessionID string
			if env.event.ParentDedupKey != "" {
				sid, lookupErr := e.cfg.DB.LookupWatcherEventSessionByDedupKey(env.watcherID, env.event.ParentDedupKey)
				if lookupErr != nil {
					e.log.Warn("thread_reply_lookup_failed",
						slog.String("watcher_id", env.watcherID),
						slog.String("parent_dedup_key", env.event.ParentDedupKey),
						slog.String("error", lookupErr.Error()),
					)
				}
				if sid != "" {
					threadSessionID = sid
					e.log.Debug("thread_reply_routed",
						slog.String("watcher_id", env.watcherID),
						slog.String("parent_dedup_key", env.event.ParentDedupKey),
						slog.String("session_id", sid),
					)
				}
				// D-08: if sid is empty, fall through to normal routing (routedTo from Router.Match)
			}

			// Unrouted branch: if event has no router match and no thread parent,
			// it goes to triage. Mark routedTo = "triage" for persistence (D-05, T-18-12).
			isTriage := routedTo == "" && env.event.ParentDedupKey == "" && e.triageReqCh != nil

			if isTriage {
				routedTo = "triage"
			}

			// Persist with dedup via INSERT OR IGNORE (D-10, D-23).
			inserted, err := e.cfg.DB.SaveWatcherEvent(
				env.watcherID,
				env.event.DedupKey(),
				env.event.Sender,
				env.event.Subject,
				routedTo,
				"", // sessionID: populated later when session is launched
				e.cfg.MaxEventsPerWatcher,
			)

			if err != nil {
				e.log.Error("save_event_failed",
					slog.String("watcher_id", env.watcherID),
					slog.String("sender", env.event.Sender),
					slog.String("error", err.Error()),
				)
				env.tracker.RecordError()
				continue
			}

			if inserted {
				// New event: update health tracker and forward to TUI (D-14).
				env.tracker.RecordEvent()

				// Persist per-watcher event log + state snapshot. Failures MUST NOT drop the event — log and continue.
				// Snapshot current health via the existing public Check() method. Check() returns a
				// read-locked HealthState copy: we pull ConsecutiveErrors directly and derive
				// AdapterHealthy from Status != HealthStatusError (the classifier sets Status=Error
				// when !adapterHealthy OR consecutiveErrors >= 10; since we record ConsecutiveErrors
				// separately, Status==Error here uniquely implies adapterHealthy==false for the
				// purposes of the persisted snapshot's AdapterHealthy bool).
				hs := env.tracker.Check()
				watcherName := hs.WatcherName
				summary := env.event.Subject
				if len(summary) > 400 {
					summary = summary[:400]
				}
				logEntry := fmt.Sprintf("## %s - %s: %s",
					e.clock.Now().UTC().Format(time.RFC3339),
					env.event.Sender,
					summary)
				if err := AppendEventLog(watcherName, logEntry); err != nil {
					e.log.Warn("event_log_append_failed",
						slog.String("watcher", watcherName),
						slog.String("error", err.Error()))
				}
				snapshot := &WatcherState{
					LastEventTS:    e.clock.Now().UTC(),
					ErrorCount:     hs.ConsecutiveErrors,
					AdapterHealthy: hs.Status != HealthStatusError,
				}
				if err := SaveState(watcherName, snapshot); err != nil {
					e.log.Warn("state_save_failed",
						slog.String("watcher", watcherName),
						slog.String("error", err.Error()))
				}

				// Set thread session ID for downstream routing (D-07).
				if threadSessionID != "" {
					env.event.ThreadSessionID = threadSessionID
				}

				// If this event is headed to triage, push to triageReqCh (D-05).
				if isTriage {
					triageSubDir := filepath.Join(e.cfg.TriageDir, env.event.DedupKey())
					req := TriageRequest{
						Event:      env.event,
						WatcherID:  env.watcherID,
						Profile:    e.cfg.Profile,
						Tracker:    env.tracker,
						TriageDir:  triageSubDir,
						ResultPath: filepath.Join(triageSubDir, "result.json"),
						SpawnedAt:  e.clock.Now(),
					}
					select {
					case e.triageReqCh <- req:
					default:
						// triageReqCh full: update routed_to to triage-req-dropped (T-18-12).
						if dbErr := e.cfg.DB.UpdateWatcherEventRoutedTo(
							env.watcherID, env.event.DedupKey(), "triage-req-dropped", "",
						); dbErr != nil {
							e.log.Warn("triage_req_dropped_update_failed",
								slog.String("watcher_id", env.watcherID),
								slog.String("error", dbErr.Error()),
							)
						}
						e.log.Warn("triage_req_channel_full",
							slog.String("sender", env.event.Sender),
						)
					}
				}

				// Non-blocking send to routedEventCh for TUI consumption.
				env.event.RoutedTo = routedTo
				select {
				case e.routedEventCh <- env.event:
				default:
					e.log.Debug("routed_event_channel_full",
						slog.String("sender", env.event.Sender),
					)
				}
			}

		case <-e.ctx.Done():
			return
		}
	}
}

// triageLoop consumes triageReqCh and manages spawning with rate limiting (INTEL-03, D-10).
func (e *Engine) triageLoop() {
	defer e.wg.Done()

	// evictTicker drives periodic queue draining when the rate-limit window opens.
	evictTicker := e.clock.NewTicker(1 * time.Second)
	defer evictTicker.Stop()

	for {
		select {
		case req := <-e.triageReqCh:
			e.handleTriageRequest(req)

		case <-evictTicker.C:
			e.PumpTriageQueue()

		case <-e.ctx.Done():
			return
		}
	}
}

// handleTriageRequest applies rate limiting and either spawns or queues the request.
func (e *Engine) handleTriageRequest(req TriageRequest) {
	if e.rateLim.tryAcquire(e.clock.Now()) {
		e.spawnTriage(req)
		return
	}

	// Rate limited: try to queue (D-10).
	select {
	case e.triageQueue <- req:
		e.log.Info("triage_queued",
			slog.String("sender", req.Event.Sender),
			slog.Int("queue_len", len(e.triageQueue)),
		)
	default:
		// Queue full (17th+ event): drop and mark in DB (D-10).
		if err := e.cfg.DB.UpdateWatcherEventRoutedTo(
			req.WatcherID, req.Event.DedupKey(), "triage-dropped", "",
		); err != nil {
			e.log.Warn("triage_dropped_update_failed",
				slog.String("sender", req.Event.Sender),
				slog.String("error", err.Error()),
			)
		}
		e.log.Warn("triage_queue_full_dropped",
			slog.String("sender", req.Event.Sender),
		)
	}
}

// PumpTriageQueue attempts to drain the triage queue by re-trying queued requests
// through the rate limiter. Called by the eviction ticker and by tests.
func (e *Engine) PumpTriageQueue() {
	for {
		select {
		case req := <-e.triageQueue:
			if e.rateLim.tryAcquire(e.clock.Now()) {
				e.spawnTriage(req)
			} else {
				// Still rate-limited: put it back and stop draining.
				select {
				case e.triageQueue <- req:
				default:
				}
				return
			}
		default:
			return
		}
	}
}

// spawnTriage invokes the spawner and registers birth with the reaper.
func (e *Engine) spawnTriage(req TriageRequest) {
	sessionID, err := e.cfg.TriageSpawner.Spawn(e.ctx, req)
	if err != nil {
		e.log.Error("triage_spawn_failed",
			slog.String("sender", req.Event.Sender),
			slog.String("error", err.Error()),
		)
		if req.Tracker != nil {
			req.Tracker.RecordError()
		}
		return
	}
	e.log.Info("triage_spawned",
		slog.String("sender", req.Event.Sender),
		slog.String("session_id", sessionID),
	)
	// Register birth with reaper so it can track the timeout.
	if e.reaper != nil {
		e.reaper.registerBirth(req.Event.DedupKey(), req.WatcherID)
	}
}

// healthLoop periodically checks adapter health and emits HealthState snapshots.
func (e *Engine) healthLoop() {
	defer e.wg.Done()

	ticker := time.NewTicker(e.cfg.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			for i := range e.adapters {
				entry := &e.adapters[i]

				if err := entry.adapter.HealthCheck(); err != nil {
					entry.tracker.SetAdapterHealth(false)
					entry.tracker.RecordError()
				} else {
					entry.tracker.SetAdapterHealth(true)
				}

				state := entry.tracker.Check()

				// Non-blocking send to healthCh.
				select {
				case e.healthCh <- state:
				default:
					e.log.Debug("health_channel_full",
						slog.String("watcher", entry.config.Name),
					)
				}
			}

		case <-e.ctx.Done():
			return
		}
	}
}

// Stop cancels all adapter contexts, calls Teardown on each adapter,
// waits for all goroutines to exit, and closes the exported channels
// (EventCh, HealthCh) so consumers receive (_, false) instead of
// blocking forever (V1.9 T5, critical-hunt #9). Safe to call multiple
// times — the close happens at most once via stopOnce.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		// Cancel root context, which propagates to all derived adapter contexts.
		e.cancel()

		// Best-effort teardown of all adapters.
		for i := range e.adapters {
			entry := &e.adapters[i]
			if err := entry.adapter.Teardown(); err != nil {
				e.log.Warn("adapter_teardown_error",
					slog.String("watcher", entry.config.Name),
					slog.String("error", err.Error()),
				)
			}
		}

		// Wait for all goroutines (adapters + writer + health + triage) to exit.
		// After Wait, no goroutine can send on routedEventCh / healthCh, so it
		// is safe to close them.
		e.wg.Wait()

		close(e.routedEventCh)
		close(e.healthCh)
	})
}

// EventCh returns a read-only channel of routed events for TUI consumption (D-20).
func (e *Engine) EventCh() <-chan Event {
	return e.routedEventCh
}

// HealthCh returns a read-only channel of health state updates for TUI consumption (D-20).
func (e *Engine) HealthCh() <-chan HealthState {
	return e.healthCh
}
