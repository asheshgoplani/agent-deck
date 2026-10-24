package watcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
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
	// Defaults to the effective agent-deck XDG data triage dir when empty.
	TriageDir string

	// ClientsPath is the path to clients.json for triage hot-reload.
	// Defaults to the effective agent-deck XDG data watcher clients.json when empty.
	ClientsPath string

	// Profile is the agent-deck profile flag passed to spawned triage sessions.
	// Defaults to the profile selected for this process.
	Profile string

	// Outbox, when set, takes every new event routed to a conductor for
	// durable delivery (#2537), and its pending deliveries are reported in
	// each HealthState. Without one, routed events are stored and sent on
	// EventCh but delivered to no conductor.
	Outbox Outbox

	// QueueRetryInterval is how long a routed event the outbox could not
	// queue waits before the writer tries again. Defaults to
	// DefaultQueueRetryInterval.
	QueueRetryInterval time.Duration
}

// DefaultQueueRetryInterval is how often the writer retries a routed event
// the outbox failed to queue for a reason a retry can fix.
const DefaultQueueRetryInterval = 5 * time.Second

// maxUnqueued bounds the routed events waiting for such a retry, per
// conductor, as the engine's event channel is bounded. Past it a routed
// event is given up unstored (dropUnqueued).
const maxUnqueued = 64

// eventEnvelope wraps an Event with metadata for the single-writer goroutine.
// This avoids modifying the public Event struct with internal routing fields.
type eventEnvelope struct {
	event     Event
	watcherID string
	tracker   *HealthTracker
	// queueFailures counts the outbox's failed attempts to queue the event.
	queueFailures int
	// conductor is the conductor a held event is routed to (holdUnqueued).
	conductor string
}

// adapterEntry holds a registered adapter with its associated metadata.
type adapterEntry struct {
	adapter   WatcherAdapter
	config    AdapterConfig
	watcherID string
	tracker   *HealthTracker
	cancel    context.CancelFunc
	// done closes when the entry's runAdapter goroutine returns; nil while
	// none was started (a failed Setup).
	done chan struct{}

	// mu serialises a health probe with the teardown of an entry removed
	// while the engine runs (RemoveAdapter), so a removed adapter is never
	// probed after its Teardown, and guards removed and listenErr.
	mu sync.Mutex
	// removed is set by RemoveAdapter: the health loop no longer reports
	// the entry.
	removed bool
	// listenErr is why the adapter's Listen loop ended on its own (a busy
	// port, say); runAdapter sets it before done closes.
	listenErr string

	// setupOK is true once Setup returned nil. An adapter whose Setup failed is
	// half-constructed — NtfyAdapter and SlackAdapter, for instance, only build
	// their *http.Client after validating the topic — so probing it can panic
	// (#1886). It also decides who owns cleanup: a failed attempt is torn down
	// immediately by setupAdapter, so Stop only tears down entries that are
	// still holding a successful Setup.
	//
	// setupAdapter sets it before the entry is in the adapter list
	// healthLoop reads (Start runs before healthLoop is launched; AddAdapter
	// holds adaptersMu). Afterwards it is read and cleared under mu: Stop and
	// RemoveAdapter clear it as they tear the adapter down, so whichever runs
	// first tears it down and the other skips it.
	setupOK bool
}

// Engine orchestrates the watcher event pipeline: adapter goroutines produce Events,
// a single-writer goroutine serializes DB writes via a buffered channel, dedup is
// handled by INSERT OR IGNORE, and the router determines event routing.
//
// Lifecycle: NewEngine -> RegisterAdapter (1..N) -> Start -> Stop. While it
// runs, AddAdapter and RemoveAdapter change its adapters one at a time
// (#2739), and ReloadRoutes re-reads clients.json.
type Engine struct {
	cfg EngineConfig
	bus *events.Bus

	// adaptersMu guards adapters, started and stopping: the health loop
	// reads the list while the engine's host adds and removes adapters.
	adaptersMu sync.Mutex
	adapters   []*adapterEntry
	started    bool
	stopping   bool

	// routesSeen is the clients.json content ReloadRoutes last read, and
	// routesErr why it did not parse (the router keeps the routes it had);
	// routesMu guards both.
	routesMu   sync.Mutex
	routesSeen []byte
	routesErr  error

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

	// unqueued holds, in arrival order and unstored, the routed events the
	// outbox could not queue yet. A routed event for a conductor with held
	// events waits behind them, so each conductor still gets its events in
	// order; other conductors' events go on. The writer tries them again
	// every QueueRetryInterval. Only writerLoop uses it.
	unqueued []eventEnvelope

	// unstored marks the watchers with a routed event given up unqueued and
	// unstored (dropUnqueued) in this engine's life: their replay position
	// (WatcherState.ReplayFrom) stays. Only writerLoop uses it.
	unstored map[string]bool

	// refusalLogInterval is the least time between two
	// watcher_delivery_queue_full warnings for one watcher and conductor
	// (defaultRefusalLogInterval); refusalLogs is when each pair last logged
	// one. Only writerLoop uses them.
	refusalLogInterval time.Duration
	refusalLogs        map[string]refusalLog

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
		if dir, err := session.TriageDir(); err == nil {
			cfg.TriageDir = dir
		} else {
			logger.Warn("watcher_triage_dir_lookup_failed", slog.String("error", err.Error()))
			cfg.TriageDir = filepath.Join(os.TempDir(), "agent-deck", "triage")
		}
	}
	if cfg.ClientsPath == "" {
		if dir, err := LayoutDir(); err == nil {
			// Singular "watcher" per REQ-WF-6. Legacy "watchers/" is served via compatibility symlink created by MigrateLegacyWatchersDir.
			cfg.ClientsPath = filepath.Join(dir, "clients.json")
		} else {
			logger.Warn("watcher_clients_path_lookup_failed", slog.String("error", err.Error()))
			cfg.ClientsPath = filepath.Join(os.TempDir(), "agent-deck", "watcher", "clients.json")
		}
	}
	if cfg.Profile == "" {
		cfg.Profile = events.CurrentProfile()
	}
	if cfg.TriageSpawner == nil {
		cfg.TriageSpawner = AgentDeckLaunchSpawner{} // BinaryPath resolved lazily at spawn time
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Engine{
		cfg:           cfg,
		adapters:      make([]*adapterEntry, 0),
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

		refusalLogInterval: defaultRefusalLogInterval,
		refusalLogs:        map[string]refusalLog{},
	}
}

// RegisterAdapter adds an adapter to the engine. Must be called before Start.
// watcherID is the statedb watcher ID used for SaveWatcherEvent persistence.
// maxSilenceMinutes is the threshold for silence detection in the health tracker.
func (e *Engine) RegisterAdapter(watcherID string, adapter WatcherAdapter, config AdapterConfig, maxSilenceMinutes int) {
	e.adaptersMu.Lock()
	defer e.adaptersMu.Unlock()
	e.adapters = append(e.adapters, e.newEntry(watcherID, adapter, config, maxSilenceMinutes))
}

func (e *Engine) newEntry(watcherID string, adapter WatcherAdapter, config AdapterConfig, maxSilenceMinutes int) *adapterEntry {
	return &adapterEntry{
		adapter:   newRecoveredAdapter(adapter, config, e.log),
		config:    config,
		watcherID: watcherID,
		tracker:   NewHealthTracker(config.Name, maxSilenceMinutes),
	}
}

// ErrEngineStopped is returned by AddAdapter once Stop has begun.
var ErrEngineStopped = errors.New("watcher engine stopped")

// AddAdapter adds one adapter to a running engine (#2739): it runs Setup and
// starts the adapter the way Start does. A watcher whose Setup fails stays
// registered, unhealthy, as it would at Start, and AddAdapter returns the
// Setup error. Before Start it is RegisterAdapter. A watcher id the engine
// already runs is refused: RemoveAdapter it first.
func (e *Engine) AddAdapter(watcherID string, adapter WatcherAdapter, config AdapterConfig, maxSilenceMinutes int) error {
	e.adaptersMu.Lock()
	defer e.adaptersMu.Unlock()
	if e.stopping {
		return ErrEngineStopped
	}
	for _, entry := range e.adapters {
		if entry.watcherID == watcherID {
			return fmt.Errorf("watcher %s already has an adapter", config.Name)
		}
	}
	entry := e.newEntry(watcherID, adapter, config, maxSilenceMinutes)
	e.adapters = append(e.adapters, entry)
	if !e.started {
		return nil
	}
	if err := e.setupAdapter(entry); err != nil {
		return err
	}
	e.launchAdapter(entry)
	return nil
}

// RemoveAdapter stops and removes the adapter of watcherID from a running
// engine (#2739) and reports whether there was one. It returns once the
// adapter is torn down, so a replacement can bind the same port. Events the
// adapter had already produced still reach the writer: only Stop discards
// them. Adapters of other watchers are not touched.
func (e *Engine) RemoveAdapter(watcherID string) bool {
	e.adaptersMu.Lock()
	var entry *adapterEntry
	for i, cand := range e.adapters {
		if cand.watcherID == watcherID {
			entry = cand
			e.adapters = append(e.adapters[:i:i], e.adapters[i+1:]...)
			break
		}
	}
	e.adaptersMu.Unlock()
	if entry == nil {
		return false
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.done != nil {
		<-entry.done
	}
	entry.mu.Lock()
	entry.removed = true
	if entry.setupOK {
		entry.setupOK = false
		e.teardownAdapter(entry, "removed")
	}
	entry.mu.Unlock()
	return true
}

// AdapterInfo describes one adapter of the engine (Adapters).
type AdapterInfo struct {
	WatcherID string
	Config    AdapterConfig
	// Running is false when the adapter's Setup failed or its Listen loop
	// has ended; ListenError is then why Listen ended, when it says.
	Running     bool
	ListenError string
}

// Adapters returns the engine's adapters, in registration order.
func (e *Engine) Adapters() []AdapterInfo {
	e.adaptersMu.Lock()
	entries := append([]*adapterEntry(nil), e.adapters...)
	e.adaptersMu.Unlock()
	out := make([]AdapterInfo, 0, len(entries))
	for _, entry := range entries {
		entry.mu.Lock()
		running, listenErr := entry.setupOK, entry.listenErr
		entry.mu.Unlock()
		if running && entry.done != nil {
			select {
			case <-entry.done:
				running = false
			default:
			}
		}
		out = append(out, AdapterInfo{WatcherID: entry.watcherID, Config: entry.config, Running: running, ListenError: listenErr})
	}
	return out
}

// ReloadRoutes re-reads clients.json and, when it changed since it was last
// read, swaps the router's routes (#2739). A missing file is no routes. An
// empty file is taken for one being written and changes nothing. A file that
// does not parse leaves the routes as they were, and its error is returned on
// every call until the file changes. Without a router it does nothing.
func (e *Engine) ReloadRoutes() (bool, error) {
	if e.cfg.Router == nil {
		return false, nil
	}
	e.routesMu.Lock()
	defer e.routesMu.Unlock()
	data, err := os.ReadFile(e.cfg.ClientsPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		data = nil
	case err != nil:
		return false, err
	case len(data) == 0:
		return false, nil
	}
	if e.routesSeen != nil && bytes.Equal(data, e.routesSeen) || e.routesSeen == nil && data == nil {
		return false, e.routesErr
	}
	clients, err := parseClients(data, e.cfg.ClientsPath)
	e.routesSeen, e.routesErr = data, err
	if err != nil {
		return false, err
	}
	e.cfg.Router.Reload(clients)
	return true, nil
}

// parseClients parses clients.json content; nil content is no routes.
func parseClients(data []byte, path string) (map[string]ClientEntry, error) {
	var clients map[string]ClientEntry
	if data == nil {
		return clients, nil
	}
	if err := json.Unmarshal(data, &clients); err != nil {
		return nil, fmt.Errorf("parse clients.json at %q: %w", path, err)
	}
	return clients, nil
}

// Start begins the event pipeline. For each registered adapter, it calls Setup,
// then launches an adapter goroutine. It also starts the single-writer goroutine
// and optionally the health check loop.
func (e *Engine) Start() error {
	e.bus = events.OpenProfile(e.cfg.Profile)
	// Migrate legacy watchers/ dir and scaffold watcher/ layout on every boot.
	// Non-fatal: log and continue so a filesystem error never prevents event processing.
	if err := MigrateLegacyWatchersDir(); err != nil {
		e.log.Warn("watcher_migration_failed", slog.String("error", err.Error()))
	}
	if err := ScaffoldWatcherLayout(); err != nil {
		e.log.Warn("watcher_scaffold_failed", slog.String("error", err.Error()))
	}

	// The router's baseline for ReloadRoutes: the clients.json it was
	// loaded from just before Start. One that does not parse left the
	// router empty; ReloadRoutes reports it until it changes.
	if data, err := os.ReadFile(e.cfg.ClientsPath); err == nil && len(data) > 0 {
		_, perr := parseClients(data, e.cfg.ClientsPath)
		e.routesSeen, e.routesErr = data, perr
	}

	e.adaptersMu.Lock()
	for _, entry := range e.adapters {
		if e.setupAdapter(entry) != nil {
			continue
		}
		e.launchAdapter(entry)
	}
	e.started = true
	e.adaptersMu.Unlock()

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

// launchAdapter starts the Listen loop of an entry whose Setup succeeded.
// The caller holds adaptersMu, so Stop's snapshot sees either the entry with
// its goroutine counted in wg, or neither.
func (e *Engine) launchAdapter(entry *adapterEntry) {
	// #nosec G118 -- adapterCancel is preserved on entry.cancel, which
	// RemoveAdapter calls; the parent context e.ctx is canceled by Stop(),
	// which propagates to all derived adapter contexts.
	adapterCtx, adapterCancel := context.WithCancel(e.ctx)
	entry.cancel = adapterCancel
	entry.done = make(chan struct{})

	e.wg.Add(1)
	go e.runAdapter(adapterCtx, entry)
}

// setupAdapter runs Setup for one entry and returns its error, nil on success.
//
// A failed Setup is cleaned up here, immediately and exactly once for that
// attempt: Setup is not transactional, so an adapter can bind a listener, open
// a client, or start a goroutine and then fail a later step, and skipping
// Teardown would leak whatever it had already taken. The entry stays registered
// (so the TUI still lists the watcher) but is marked unhealthy and left with
// setupOK=false, which keeps healthLoop from probing a half-constructed adapter
// and tells Stop that this entry's cleanup is already done.
//
// Calling setupAdapter again on the same entry is a retry: each failed attempt
// gets its own cleanup, and a later success leaves exactly one live Setup for
// Stop to release.
func (e *Engine) setupAdapter(entry *adapterEntry) error {
	entry.setupOK = false
	entry.config.ResumeSince = e.resumeSince(entry.config)

	if err := entry.adapter.Setup(e.ctx, entry.config); err != nil {
		e.log.Warn("adapter_setup_failed",
			slog.String("watcher", entry.config.Name),
			slog.String("type", entry.config.Type),
			slog.String("error", err.Error()),
		)
		entry.tracker.SetAdapterHealth(false)
		e.teardownAdapter(entry, "setup_failed")
		return err
	}

	entry.setupOK = true
	return nil
}

// teardownAdapter calls Teardown once and contains anything it throws. A
// half-constructed adapter is precisely the thing that panicked in #1886, and
// cleanup runs on the failed-Setup path where that state is expected, so a
// panicking Teardown must be logged and swallowed rather than allowed to take
// the process down with it.
func (e *Engine) teardownAdapter(entry *adapterEntry, reason string) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("adapter_teardown_panic",
				slog.String("watcher", entry.config.Name),
				slog.String("type", entry.config.Type),
				slog.String("reason", reason),
				slog.Any("panic", r),
			)
		}
	}()

	if err := entry.adapter.Teardown(); err != nil {
		e.log.Warn("adapter_teardown_error",
			slog.String("watcher", entry.config.Name),
			slog.String("reason", reason),
			slog.String("error", err.Error()),
		)
	}
}

// runAdapter runs a single adapter's Listen loop in its own goroutine.
// Events are wrapped in envelopes and sent to the single-writer via eventCh.
func (e *Engine) runAdapter(ctx context.Context, entry *adapterEntry) {
	defer e.wg.Done()
	defer close(entry.done)

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
			// Wait for the writer instead of dropping on a full channel: the
			// adapter's resume position has already moved past this event, so
			// a drop would lose it for good (#2538). Cancellation still frees
			// the adapter (T-13-06): once ctx is done the remaining events are
			// discarded unpersisted, and the persisted cursor, which only the
			// writer advances, makes the next start fetch them again.
			// Removing this one adapter (RemoveAdapter, ctx done) is not a
			// stop: the writer still runs, so what the adapter produced
			// before it stopped is handed on, a webhook's accepted events
			// included (#2739).
			select {
			case e.eventCh <- env:
			case <-e.ctx.Done():
				e.log.Debug("event_discarded_on_stop",
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
		entry.mu.Lock()
		entry.listenErr = err.Error()
		entry.mu.Unlock()
	}
}

// writerLoop is the single-writer goroutine that serializes all DB writes (D-13).
// It reads event envelopes from eventCh, performs dedup via SaveWatcherEvent,
// routes events, updates health trackers, and forwards persisted events to routedEventCh.
func (e *Engine) writerLoop() {
	defer e.wg.Done()
	// retryC fires while routed events wait for the outbox to recover.
	var retry *time.Timer
	var retryC <-chan time.Time
	defer func() {
		if retry != nil {
			retry.Stop()
		}
		// Stopping: one last attempt each. One that still cannot be queued
		// is given up unstored, so a replay can route it again.
		waiting := e.unqueued
		e.unqueued = nil
		for _, env := range waiting {
			e.handleEvent(env, true)
		}
	}()

	for {
		if len(e.unqueued) > 0 && retryC == nil {
			interval := e.cfg.QueueRetryInterval
			if interval <= 0 {
				interval = DefaultQueueRetryInterval
			}
			retry = time.NewTimer(interval)
			retryC = retry.C
		}
		select {
		case env, ok := <-e.eventCh:
			if !ok {
				return
			}
			e.handleEvent(env, false)

		case <-retryC:
			retryC = nil
			waiting := e.unqueued
			e.unqueued = nil
			for _, env := range waiting {
				e.handleEvent(env, false)
			}

		case <-e.ctx.Done():
			return
		}
	}
}

// handleEvent routes, queues and stores one event (writerLoop). With last
// set, a routed event the outbox cannot queue is given up unstored instead
// of waiting for another try.
func (e *Engine) handleEvent(env eventEnvelope, last bool) {
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

	if e.cfg.Outbox != nil && routedTo != "" && !isTriage {
		// While older routed events for this conductor wait for the outbox,
		// a newer one waits behind them instead of overtaking them in the
		// send queue.
		if !last && e.holding(routedTo) {
			env.conductor = routedTo
			if e.holdUnqueued(env) {
				return
			}
		} else if e.queueDelivery(&env, routedTo) {
			// The outbox failed in a way a retry can fix (reading the
			// sessions, writing the queue). The event stays unstored: a
			// stored event is never queued again, so storing it would lose
			// its delivery for good.
			env.conductor = routedTo
			if last {
				if e.dropUnqueued(env, "the engine stopped before the send queue took it") {
					return
				}
			} else if e.holdUnqueued(env) {
				return
			}
		}
	}

	// Persist with dedup via INSERT OR IGNORE (D-10, D-23).
	inserted, err := e.cfg.DB.SaveWatcherEvent(
		env.watcherID,
		env.event.DedupKey(),
		env.event.Sender,
		env.event.Subject,
		routedTo,
		"", // sessionID: populated later when session is launched
		env.event.Body,
		e.cfg.MaxEventsPerWatcher,
	)

	if err != nil {
		e.log.Error("save_event_failed",
			slog.String("watcher_id", env.watcherID),
			slog.String("sender", env.event.Sender),
			slog.String("error", err.Error()),
		)
		env.tracker.RecordError()
		return
	}

	if inserted {
		// New event: update health tracker and forward to TUI (D-14).
		env.tracker.RecordEvent()

		// Slice 4 (CORE-PLAN): additive tap onto the event bus.
		e.bus.Publish("watcher.event", env.watcherID, map[string]any{
			"sender":    env.event.Sender,
			"subject":   env.event.Subject,
			"routed_to": routedTo,
			"dedup_key": env.event.DedupKey(),
			"is_triage": isTriage,
		})

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
		prev, err := LoadState(watcherName)
		if err != nil {
			prev = nil
		}
		if env.event.Cursor != "" {
			snapshot.DedupCursor = env.event.Cursor
			snapshot.DedupCursorTime = env.event.Timestamp.UTC()
		} else if prev != nil {
			snapshot.DedupCursor = prev.DedupCursor
			snapshot.DedupCursorTime = prev.DedupCursorTime
		}
		// The replay position stays while one of this watcher's routed
		// events waits unstored or was given up in this engine's life.
		if prev != nil && e.replayPending(env.watcherID) {
			snapshot.ReplayFrom = prev.ReplayFrom
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
	} else if env.event.Cursor != "" {
		// A duplicate (typically a message replayed after a restart)
		// was already stored: only move the resume position past it.
		e.advanceResumeCursor(env)
	}
}

// holding reports whether routed events for conductor wait for a retry.
func (e *Engine) holding(conductor string) bool {
	for _, env := range e.unqueued {
		if env.conductor == conductor {
			return true
		}
	}
	return false
}

// holdUnqueued keeps env, unstored, for the writer's next queue retry, and
// records a replay position before it; a failed write is tried again on
// every retry pass. Past maxUnqueued waiting events for its conductor env is
// given up instead (dropUnqueued). It reports false when the caller must
// store env after all.
func (e *Engine) holdUnqueued(env eventEnvelope) bool {
	held := 0
	for _, w := range e.unqueued {
		if w.conductor == env.conductor {
			held++
		}
	}
	if held >= maxUnqueued {
		return e.dropUnqueued(env, "too many routed events for this conductor already wait for the send queue")
	}
	_ = e.keepReplayPosition(env)
	e.unqueued = append(e.unqueued, env)
	return true
}

// dropUnqueued gives up on a routed event the outbox could not queue. It is
// left unstored, once a replay position before it is on disk: a stored event
// is never routed again, while one that is not stored is routed and queued
// again when its source replays it (ntfy and slack resume from that
// position; a webhook cannot replay, so the warning is its only trace).
// When the position cannot be written after a few tries, dropUnqueued
// reports false and the caller stores the event: a restart would skip it
// anyway, and stored it at least stays in watcher_events.
func (e *Engine) dropUnqueued(env eventEnvelope, reason string) bool {
	var err error
	for try := 0; try < 3; try++ {
		if err = e.keepReplayPosition(env); err == nil {
			break
		}
	}
	name := env.tracker.Check().WatcherName
	if err != nil {
		e.log.Warn("watcher_delivery_not_queued",
			slog.String("watcher", name),
			slog.String("sender", env.event.Sender),
			slog.Int("failures", env.queueFailures),
			slog.String("reason", reason+"; stored, as no replay position could be written"),
			slog.String("error", err.Error()),
		)
		return false
	}
	if e.unstored == nil {
		e.unstored = make(map[string]bool)
	}
	e.unstored[env.watcherID] = true
	e.log.Warn("watcher_delivery_not_queued",
		slog.String("watcher", name),
		slog.String("sender", env.event.Sender),
		slog.Int("failures", env.queueFailures),
		slog.String("reason", reason),
	)
	return true
}

// replayPending reports whether watcherID still needs its replay position:
// one of its routed events waits unstored for the send queue, or was given
// up unstored, in this engine's life.
func (e *Engine) replayPending(watcherID string) bool {
	if e.unstored[watcherID] {
		return true
	}
	for _, env := range e.unqueued {
		if env.watcherID == watcherID {
			return true
		}
	}
	return false
}

// keepReplayPosition records in env's watcher state a replay position no
// later than env, before env is left unstored: a restart then resumes from
// there (resumeSince) even when the watcher has no cursor yet, or a later
// stored event has moved its cursor past env. It returns the read or write
// error; a state that exists but cannot be read is never written over.
func (e *Engine) keepReplayPosition(env eventEnvelope) error {
	ts := env.event.Timestamp
	if ts.IsZero() {
		ts = e.clock.Now()
	}
	from := ts.Add(-time.Second).UTC()
	name := env.tracker.Check().WatcherName
	st, err := LoadState(name)
	if err != nil {
		// A state that cannot be read now may hold an earlier replay
		// position; writing over it could move the position past an event
		// still unstored. The caller tries again.
		e.log.Warn("watcher_state_unreadable",
			slog.String("watcher", name),
			slog.String("error", err.Error()))
		return err
	}
	if st == nil {
		st = &WatcherState{}
	}
	if !st.ReplayFrom.IsZero() && !st.ReplayFrom.After(from) {
		return nil
	}
	st.ReplayFrom = from
	if err := SaveState(name, st); err != nil {
		e.log.Warn("state_save_failed",
			slog.String("watcher", name),
			slog.String("error", err.Error()))
		return err
	}
	return nil
}

// queueDelivery hands a new event routed to a conductor to the outbox
// (#2537). It runs before the event is stored, so a crash in between cannot
// leave a stored event that nothing delivers: a stored event is never routed
// again. An event already stored is a replay, which the conductor got the
// first time, and is not queued; the queue's key keeps an event stored again
// after its row was pruned from being queued twice.
//
// It reports whether the outbox failed in a way a retry can fix, for the
// caller to keep the event unstored. A conductor with no session cannot be
// queued for until one is created, so that event is stored as before, and
// so is an event for a conductor whose queue is full (ErrDeliveryQueueFull).
func (e *Engine) queueDelivery(env *eventEnvelope, conductor string) (retry bool) {
	name := env.tracker.Check().WatcherName
	stored, err := e.cfg.DB.HasWatcherEvent(env.watcherID, env.event.DedupKey())
	if err != nil {
		// Queue it anyway: the queue key catches a replay.
		e.log.Warn("watcher_event_lookup_failed",
			slog.String("watcher", name),
			slog.String("error", err.Error()),
		)
	}
	if stored {
		return
	}
	evt := env.event
	evt.RoutedTo = conductor
	err = e.cfg.Outbox.Enqueue(env.watcherID, name, evt)
	if err == nil {
		if env.queueFailures > 0 {
			e.log.Info("watcher_delivery_queued_after_retry",
				slog.String("watcher", name),
				slog.String("conductor", conductor),
				slog.Int("failures", env.queueFailures),
			)
		}
		return false
	}
	if errors.Is(err, ErrDeliveryQueueFull) {
		// The event is stored as before but never delivered: count it in
		// state.db, where `watcher status` and the health loop read it.
		e.recordRefusal(env.watcherID, name, conductor, err)
		return false
	}
	env.queueFailures++
	if !errors.Is(err, ErrNoConductorSession) {
		if env.queueFailures == 1 {
			e.log.Warn("watcher_delivery_queue_failed",
				slog.String("watcher", name),
				slog.String("conductor", conductor),
				slog.String("sender", evt.Sender),
				slog.String("error", err.Error()),
			)
		}
		return true
	}
	e.log.Warn("watcher_delivery_not_queued",
		slog.String("watcher", name),
		slog.String("conductor", conductor),
		slog.String("sender", evt.Sender),
		slog.Int("failures", env.queueFailures),
		slog.String("error", err.Error()),
	)
	return false
}

// defaultRefusalLogInterval is the least time between two
// watcher_delivery_queue_full warnings for one watcher and conductor. Each
// warning carries the running total, so a flood logs a line a minute, not
// one per event, and the log still shows it is going on.
const defaultRefusalLogInterval = time.Minute

// refusalLog is when a watcher and conductor pair last logged a refusal, and
// how many refusals it has not logged since.
type refusalLog struct {
	at         time.Time
	suppressed int
}

// recordRefusal counts a routed event the outbox refused as queue full in
// state.db (per watcher and conductor, with the first and last refusal
// time) and logs watcher_delivery_queue_full at most once per
// refusalLogInterval per pair, with the running total.
func (e *Engine) recordRefusal(watcherID, watcherName, conductor string, cause error) {
	now := time.Now()
	total, dbErr := 0, errors.New("no state database")
	if e.cfg.DB != nil {
		total, dbErr = e.cfg.DB.RecordWatcherDeliveryRefusal(watcherID, conductor, now)
	}
	key := watcherID + "\x00" + conductor
	last, seen := e.refusalLogs[key]
	if seen && now.Sub(last.at) < e.refusalLogInterval && dbErr == nil {
		last.suppressed++
		e.refusalLogs[key] = last
		return
	}
	e.refusalLogs[key] = refusalLog{at: now}
	attrs := []any{
		slog.String("watcher", watcherName),
		slog.String("conductor", conductor),
		slog.Int("refused_total", total),
		slog.Int("refused_since_last_warning", last.suppressed+1),
		slog.String("error", cause.Error()),
		slog.String("action", "the event is stored in watcher_events but not delivered; see agent-deck watcher status"),
	}
	if dbErr != nil {
		attrs = append(attrs, slog.String("count_error", dbErr.Error()))
	}
	e.log.Warn("watcher_delivery_queue_full", attrs...)
}

// DefaultResumeMaxAge bounds how far back a restarted ntfy or slack watcher
// replays. A watcher's [source] table can override it with "resume_max_age"
// (a Go duration such as "6h"; "0" turns replay off).
const DefaultResumeMaxAge = 24 * time.Hour

// resumeSince computes AdapterConfig.ResumeSince from the persisted state.json
// (#2538). A fresh watcher (no state) gets "" and streams only new messages,
// as before. Otherwise it resumes after the persisted message ID; when that
// position is older than the replay bound, or only the older last_event_ts is
// known, it resumes from a unix timestamp no earlier than now minus the bound.
// The server's message cache bounds the replay further, and the event dedup
// key keeps anything already stored from being handled twice.
func (e *Engine) resumeSince(cfg AdapterConfig) string {
	maxAge := DefaultResumeMaxAge
	if raw := cfg.Settings["resume_max_age"]; raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			e.log.Warn("watcher_resume_max_age_invalid",
				slog.String("watcher", cfg.Name),
				slog.String("value", raw),
				slog.String("error", err.Error()),
			)
		} else {
			maxAge = d
		}
	}
	if maxAge <= 0 {
		return ""
	}

	st, err := LoadState(cfg.Name)
	if err != nil {
		e.log.Warn("watcher_resume_state_unreadable",
			slog.String("watcher", cfg.Name),
			slog.String("error", err.Error()),
		)
		return ""
	}
	if st == nil {
		return ""
	}

	floor := e.clock.Now().Add(-maxAge)
	if !st.ReplayFrom.IsZero() {
		// A routed event after this point was left unstored (#2537):
		// replay from here, whatever the cursor says.
		from := st.ReplayFrom
		if from.Before(floor) {
			from = floor
		}
		return strconv.FormatInt(from.Unix(), 10)
	}
	if st.DedupCursor != "" && (st.DedupCursorTime.IsZero() || st.DedupCursorTime.After(floor)) {
		return st.DedupCursor
	}
	from := st.DedupCursorTime
	if st.DedupCursor == "" {
		from = st.LastEventTS
	}
	if from.IsZero() {
		return ""
	}
	if from.Before(floor) {
		from = floor
	}
	return strconv.FormatInt(from.Unix(), 10)
}

// advanceResumeCursor records env's resume position in state.json, keeping
// the rest of the persisted snapshot.
func (e *Engine) advanceResumeCursor(env eventEnvelope) {
	name := env.tracker.Check().WatcherName
	st, err := LoadState(name)
	if err != nil || st == nil {
		st = &WatcherState{}
	}
	st.DedupCursor = env.event.Cursor
	st.DedupCursorTime = env.event.Timestamp.UTC()
	if err := SaveState(name, st); err != nil {
		e.log.Warn("state_save_failed",
			slog.String("watcher", name),
			slog.String("error", err.Error()))
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
			pending, pendingErr := e.pendingDeliveries()
			refused := e.deliveryRefusals()
			e.adaptersMu.Lock()
			entries := append([]*adapterEntry(nil), e.adapters...)
			e.adaptersMu.Unlock()
			for _, entry := range entries {
				// Adapters whose Setup failed are already marked unhealthy in
				// Start; probing them would call into a half-constructed adapter
				// and take the whole process down (#1886). Still emit their state
				// so the TUI keeps reporting the watcher as errored. An adapter
				// removed meanwhile is torn down: skip it.
				entry.mu.Lock()
				if entry.setupOK {
					if err := entry.adapter.HealthCheck(); err != nil {
						entry.tracker.SetAdapterHealth(false)
						entry.tracker.RecordError()
					} else {
						entry.tracker.SetAdapterHealth(true)
					}
				}
				removed := entry.removed
				entry.mu.Unlock()
				if removed {
					continue
				}

				state := entry.tracker.Check()
				state.Undelivered = pending[entry.watcherID]
				if pendingErr != nil {
					state.UndeliveredError = pendingErr.Error()
				}
				applyRefusals(&state, refused[entry.watcherID], time.Now())

				// Slice 4 (CORE-PLAN): additive tap onto the event bus —
				// this is the "health journal" producer named in the plan.
				e.bus.Publish("watcher.health", entry.config.Name, state)

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

// deliveryRefusals reads the persisted refusal counts, per watcher id; nil
// without a state database.
func (e *Engine) deliveryRefusals() map[string][]DeliveryRefusal {
	if e.cfg.DB == nil {
		return nil
	}
	rows, err := e.cfg.DB.LoadWatcherDeliveryRefusals("")
	if err != nil {
		e.log.Warn("watcher_delivery_refusals_unreadable", slog.String("error", err.Error()))
		return nil
	}
	out := map[string][]DeliveryRefusal{}
	for _, r := range rows {
		out[r.WatcherID] = append(out[r.WatcherID], DeliveryRefusal{
			Conductor: r.Conductor, Count: r.Count,
			FirstRefusedAt: r.FirstRefusedAt, LastRefusedAt: r.LastRefusedAt,
		})
	}
	return out
}

// applyRefusals puts a watcher's refusals in its health state and, for each
// conductor with a refusal newer than RefusalWarningWindow, says so in the
// message, moving a healthy watcher to warning so the health alert fires.
func applyRefusals(state *HealthState, refused []DeliveryRefusal, now time.Time) {
	state.Refused = refused
	var recent []string
	for _, r := range refused {
		if now.Sub(r.LastRefusedAt) < RefusalWarningWindow {
			recent = append(recent, fmt.Sprintf("%d routed events for conductor %s refused since %s, last at %s (delivery queue full; stored but not delivered)",
				r.Count, r.Conductor, r.FirstRefusedAt.UTC().Format(time.RFC3339), r.LastRefusedAt.UTC().Format(time.RFC3339)))
		}
	}
	if len(recent) == 0 {
		return
	}
	msg := strings.Join(recent, "; ")
	if state.Status == HealthStatusHealthy {
		state.Status = HealthStatusWarning
		state.Message = msg
		return
	}
	state.Message += "; " + msg
}

// pendingDeliveries reads the outbox's undelivered events, per watcher id;
// nil without an outbox. The error is the queue's when it cannot be read.
func (e *Engine) pendingDeliveries() (map[string][]PendingDelivery, error) {
	if e.cfg.Outbox == nil {
		return nil, nil
	}
	pending, err := e.cfg.Outbox.Pending()
	if err != nil {
		e.log.Warn("watcher_pending_deliveries_unreadable", slog.String("error", err.Error()))
		return nil, err
	}
	return pending, nil
}

// Stop cancels all adapter contexts, calls Teardown on each adapter,
// waits for all goroutines to exit, and closes the exported channels
// (EventCh, HealthCh) so consumers receive (_, false) instead of
// blocking forever (V1.9 T5, critical-hunt #9). Safe to call multiple
// times — the close happens at most once via stopOnce.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		// No adapter is added from here on (AddAdapter checks stopping).
		e.adaptersMu.Lock()
		e.stopping = true
		entries := append([]*adapterEntry(nil), e.adapters...)
		e.adaptersMu.Unlock()

		// Cancel root context, which propagates to all derived adapter contexts.
		e.cancel()

		// Best-effort teardown of every adapter still holding a successful Setup.
		// Entries whose Setup failed were already torn down by setupAdapter at
		// the moment they failed, so skipping them here is what makes cleanup
		// exactly-once rather than twice (#1886); clearing setupOK under the
		// entry's lock does the same against a RemoveAdapter running now.
		for _, entry := range entries {
			entry.mu.Lock()
			if entry.setupOK {
				entry.setupOK = false
				e.teardownAdapter(entry, "stop")
			}
			entry.mu.Unlock()
		}

		// Wait for all goroutines (adapters + writer + health + triage) to exit.
		// After Wait, no goroutine can send on routedEventCh / healthCh, so it
		// is safe to close them.
		e.wg.Wait()
		if e.bus != nil {
			_ = e.bus.Close()
		}

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
