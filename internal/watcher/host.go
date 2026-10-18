package watcher

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// DefaultEngineRetryInterval is how often a process that does not own its
// profile's watcher engine tries to take it over.
const DefaultEngineRetryInterval = 5 * time.Second

// relayStopWait bounds how long Stop waits for the relay to hand on what the
// stopped engine had buffered.
const relayStopWait = 5 * time.Second

// HostConfig configures an EngineHost.
type HostConfig struct {
	// Profile is the profile whose engine owner lock the host takes
	// (EngineLockPath).
	Profile string

	// DB is the profile's state database: the watchers to run and where their
	// events are stored.
	DB *statedb.StateDB

	// DeliverEvent and DeliverHealth receive every routed event and health
	// state of an engine this host runs, on the relay's goroutines (see
	// relayEngine). Either may be nil.
	DeliverEvent  func(Event)
	DeliverHealth func(HealthState)

	// RetryInterval is how often a standby host tries to take the engine
	// over. Defaults to DefaultEngineRetryInterval.
	RetryInterval time.Duration

	// Logger defaults to logging.ForComponent(logging.CompWatcher).
	Logger *slog.Logger
}

// EngineHost runs a profile's watcher engine in whichever long-lived
// agent-deck process owns it (#2530). Every TUI, `web` and `web --no-tui`
// process has one. The host that takes the engine owner lock starts the
// engine and the relay that hands its output to the delivery callbacks; a
// host that loses stays on standby and retries on a ticker, so when the owner
// exits another process takes over without any coordination channel. There is
// at most one engine per profile, and the host has no Bubble Tea types: the
// TUI and the headless server both run it.
//
// Lifecycle: NewEngineHost -> Start -> Stop. The panel channels stay the same
// for the host's whole life, so a TUI listens on them once, and gets events
// after a takeover too.
type EngineHost struct {
	cfg HostConfig
	log *slog.Logger

	panelEvents chan Event
	panelHealth chan HealthState

	stop     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once

	mu        sync.Mutex
	stopped   bool
	owner     *EngineOwner
	engine    *Engine
	relayDone <-chan struct{}
	// waiting is the last reason logged for not owning the engine, so a
	// waiting host logs once per change instead of on every retry.
	// sawOwner records that another process held the lock at some point,
	// which makes winning it later a takeover.
	waiting  string
	sawOwner bool

	// start builds and starts the engine (startEngine; tests replace it).
	start func(*statedb.StateDB, *slog.Logger) *Engine
}

// NewEngineHost returns a host that has not joined the election yet.
func NewEngineHost(cfg HostConfig) *EngineHost {
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = DefaultEngineRetryInterval
	}
	log := cfg.Logger
	if log == nil {
		log = logging.ForComponent(logging.CompWatcher)
	}
	return &EngineHost{
		cfg:         cfg,
		log:         log,
		panelEvents: make(chan Event, 1),
		panelHealth: make(chan HealthState, 1),
		stop:        make(chan struct{}),
		start:       startEngine,
	}
}

// Start tries to take the engine owner lock and, if it wins, starts the
// engine before returning. If another process owns the engine, or there is
// no running watcher yet, the host waits and retries every RetryInterval
// until it runs an engine or Stop is called. Call it once.
func (h *EngineHost) Start() {
	if h.tryOwn() {
		return
	}
	h.wg.Add(1)
	go h.standby()
}

func (h *EngineHost) standby() {
	defer h.wg.Done()
	ticker := time.NewTicker(h.cfg.RetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			if h.tryOwn() {
				return
			}
		}
	}
}

// tryOwn takes the lock and starts the engine. It reports whether the host
// is done waiting: it runs the engine now, or it was stopped.
//
// A process with no running watcher does not take the lock, and one whose
// engine did not start gives it back: holding it with nothing to run would
// keep every other process of the profile from running a watcher started
// later.
func (h *EngineHost) tryOwn() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return true
	}
	if n, err := runningWatchers(h.cfg.DB); err != nil {
		h.wait("load_failed:"+err.Error(), func() {
			h.log.Warn("watcher_engine_load_failed",
				slog.String("profile", h.cfg.Profile),
				slog.String("error", err.Error()))
		})
		return false
	} else if n == 0 {
		h.wait("idle", func() {
			h.log.Info("watcher_engine_idle",
				slog.String("profile", h.cfg.Profile),
				slog.String("reason", "no running watcher"))
		})
		return false
	}

	path, err := EngineLockPath(h.cfg.Profile)
	var owner *EngineOwner
	if err == nil {
		owner, err = AcquireEngineOwner(path)
	}
	if err != nil {
		var running *AlreadyRunningError
		if errors.As(err, &running) {
			h.sawOwner = true
			h.wait("standby:"+strconv.Itoa(running.PID), func() {
				h.log.Info("watcher_engine_standby",
					slog.String("profile", h.cfg.Profile),
					slog.Int("owner_pid", running.PID))
			})
		} else {
			h.wait("lock_failed:"+err.Error(), func() {
				h.log.Warn("watcher_engine_lock_failed",
					slog.String("profile", h.cfg.Profile),
					slog.String("error", err.Error()))
			})
		}
		return false
	}

	eng := h.start(h.cfg.DB, h.log)
	if eng == nil {
		// The engine did not start, or its watchers were stopped meanwhile:
		// give the lock back and try again on the next tick.
		if err := owner.Close(); err != nil {
			h.log.Warn("watcher_engine_release_failed", slog.String("error", err.Error()))
		}
		return false
	}
	h.owner, h.engine = owner, eng
	outcome := "watcher_engine_owner"
	if h.sawOwner {
		outcome = "watcher_engine_took_over"
	}
	h.log.Info(outcome,
		slog.String("profile", h.cfg.Profile),
		slog.Int("pid", os.Getpid()),
		slog.String("lock", path))
	h.relayDone = relayEngine(eng.EventCh(), eng.HealthCh(), h.cfg.DeliverEvent, h.cfg.DeliverHealth,
		h.panelEvents, h.panelHealth, h.log)
	return true
}

// wait logs why the host does not run the engine, once per reason.
func (h *EngineHost) wait(reason string, log func()) {
	if reason != h.waiting {
		log()
		h.waiting = reason
	}
}

// Stop leaves the election. An owner stops its engine, waits up to 5 s for
// the relay to hand on what the engine had buffered, and runs drain (when not
// nil). drain finishes or reports the deliveries this process has queued and
// returns a channel that closes once none of them is still being sent (nil
// when none is). Only then is the lock released: a successor never binds a
// port this engine still holds, or types into a conductor pane next to a
// delivery still running here. When a delivery outlives Stop, the lock is
// released as it returns, or when the process exits, which ends it. Safe to
// call more than once and from several goroutines; later calls wait for the
// first.
func (h *EngineHost) Stop(drain func() <-chan struct{}) {
	h.stopOnce.Do(func() {
		h.mu.Lock()
		h.stopped = true
		h.mu.Unlock()
		close(h.stop)
		h.wg.Wait()

		h.mu.Lock()
		owner, eng, relayDone := h.owner, h.engine, h.relayDone
		h.mu.Unlock()
		if eng != nil {
			eng.Stop()
			select {
			case <-relayDone:
			case <-time.After(relayStopWait):
				h.log.Warn("watcher_relay_stop_timeout")
			}
		}
		var inFlight <-chan struct{}
		if drain != nil {
			inFlight = drain()
		}
		if owner == nil {
			// Never ran an engine: no relay will close the panel channels.
			close(h.panelEvents)
			close(h.panelHealth)
			return
		}
		release := func() {
			if err := owner.Close(); err != nil {
				h.log.Warn("watcher_engine_release_failed", slog.String("error", err.Error()))
			}
			h.log.Info("watcher_engine_released", slog.String("profile", h.cfg.Profile))
		}
		if inFlight == nil {
			release()
			return
		}
		select {
		case <-inFlight:
			release()
		default:
			h.log.Info("watcher_engine_release_deferred",
				slog.String("profile", h.cfg.Profile),
				slog.String("reason", "a conductor delivery is still in flight"))
			go func() {
				<-inFlight
				release()
			}()
		}
	})
}

// IsOwner reports whether this host holds the engine owner lock.
func (h *EngineHost) IsOwner() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.owner != nil && !h.stopped
}

// Engine returns the running engine, or nil while the host waits (another
// process owns the engine, or no watcher is running) and after Stop.
func (h *EngineHost) Engine() *Engine {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return nil
	}
	return h.engine
}

// PanelEvents and PanelHealth carry what the relay delivered, for a TUI's
// watcher panel. They hold at most one pending item and close when the host
// stops.
func (h *EngineHost) PanelEvents() <-chan Event       { return h.panelEvents }
func (h *EngineHost) PanelHealth() <-chan HealthState { return h.panelHealth }

// runningWatchers counts the watchers marked running in db.
func runningWatchers(db *statedb.StateDB) (int, error) {
	if db == nil {
		return 0, nil
	}
	rows, err := db.LoadWatchers()
	if err != nil {
		return 0, err
	}
	return runningCount(rows), nil
}

// startEngine builds the watcher engine from the state database and starts
// it: every watcher marked running gets its adapter. It returns nil when db
// is nil or no watcher is running.
func startEngine(db *statedb.StateDB, log *slog.Logger) *Engine {
	if db == nil {
		return nil
	}
	rows, err := db.LoadWatchers()
	if err != nil || runningCount(rows) == 0 {
		return nil
	}

	// Load user config for watcher settings.
	cfg, _ := session.LoadUserConfig()
	var watcherCfg session.WatcherSettings
	if cfg != nil {
		watcherCfg = cfg.Watcher
	}

	// Build engine config.
	router, _ := LoadFromWatcherDir() // nil on error (no clients.json yet)
	healthInterval := time.Duration(watcherCfg.GetHealthCheckIntervalSeconds()) * time.Second

	engineCfg := EngineConfig{
		DB:                  db,
		Router:              router,
		MaxEventsPerWatcher: watcherCfg.GetMaxEventsPerWatcher(),
		HealthCheckInterval: healthInterval,
	}
	eng := NewEngine(engineCfg)

	maxSilenceMinutes := watcherCfg.GetMaxSilenceMinutes()

	// Register all running watchers as adapters.
	for _, row := range rows {
		if row.Status != "running" {
			continue
		}
		var adapter WatcherAdapter
		switch row.Type {
		case "webhook":
			adapter = &WebhookAdapter{}
		case "ntfy":
			adapter = &NtfyAdapter{}
		case "slack":
			adapter = &SlackAdapter{}
		case "github":
			adapter = &GitHubAdapter{}
		default:
			continue
		}

		adapterCfg := AdapterConfig{
			Type:     row.Type,
			Name:     row.Name,
			Settings: loadSourceSettings(row.Name),
		}
		eng.RegisterAdapter(row.ID, adapter, adapterCfg, maxSilenceMinutes)
	}

	if err := eng.Start(); err != nil {
		log.Warn("watcher_engine_start_failed", "error", err.Error())
		return nil
	}

	log.Info("watcher_engine_started",
		slog.Int("watcher_count", len(rows)),
		slog.Int("running_count", runningCount(rows)))
	return eng
}

// runningCount returns how many watcher rows are in the "running" state.
func runningCount(rows []*statedb.WatcherRow) int {
	n := 0
	for _, r := range rows {
		if r != nil && r.Status == "running" {
			n++
		}
	}
	return n
}

// loadSourceSettings reads the [source] table from
// ~/.agent-deck/watcher/<name>/watcher.toml into a map[string]string suitable for
// AdapterConfig.Settings. Returns an empty (non-nil) map on any error so the engine
// falls back to per-adapter defaults instead of failing to register.
func loadSourceSettings(name string) map[string]string {
	out := map[string]string{}
	dir, err := session.WatcherNameDir(name)
	if err != nil {
		return out
	}
	path := filepath.Join(dir, "watcher.toml")
	var cfg struct {
		Source map[string]string `toml:"source"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return out
	}
	for k, v := range cfg.Source {
		out[k] = v
	}
	return out
}
