package watcher

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// DefaultEngineRetryInterval is how often a process that does not own its
// profile's watcher engine tries to take it over, and how often the owner
// applies changed watchers and routes to its running engine (#2739).
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

	// DeliverHealth receives every health state of an engine this host
	// runs, on the relay's goroutine (see relayEngine). It may be nil. Routed
	// events are delivered by the engine itself, through a ConductorOutbox
	// writing to the profile's send queue (#2537).
	DeliverHealth func(HealthState)

	// SpawnSendWorker starts a conductor's send worker for the outbox; nil
	// uses sendqueue.SpawnWorker. Tests replace it.
	SpawnSendWorker func(profile, sessionID string) error

	// RetryInterval is how often a standby host tries to take the engine
	// over, and how often the owner reconciles its engine with the watchers
	// and routes on disk. Defaults to DefaultEngineRetryInterval.
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
// The owner keeps its engine in step with the profile while it runs (#2739):
// on the same ticker it starts the adapter of a watcher started since, stops
// the adapter of one stopped or deleted, restarts one whose [source] table
// changed, and swaps the routes when clients.json changed. Adapters of
// unchanged watchers are not touched. `watcher reload` asks for that pass
// now (RequestReload) and gets its ReloadReport.
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

	// lastReload is the reload request the owner last answered, and
	// reconcileWaiting the last reconcile failure logged. Only the host's
	// goroutine uses them.
	lastReload       string
	reconcileWaiting string
	// lastReloadLog is the last reconcile outcome logged, so a failure that
	// repeats on every pass is logged once.
	lastReloadLog string

	// start builds and starts the engine (startEngine; tests replace it).
	start func(HostConfig, *slog.Logger) *Engine
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
// until it runs an engine or Stop is called. An owner reconciles its engine
// on that interval. Call it once.
func (h *EngineHost) Start() {
	owned := h.tryOwn()
	h.wg.Add(1)
	go h.run(owned)
}

func (h *EngineHost) run(owned bool) {
	defer h.wg.Done()
	ticker := time.NewTicker(h.cfg.RetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			if !owned {
				owned = h.tryOwn()
				continue
			}
			h.reconcile()
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

	eng := h.start(h.cfg, h.log)
	if eng == nil {
		// The engine did not start, or its watchers were stopped meanwhile:
		// give the lock back and try again on the next tick.
		if err := owner.Close(); err != nil {
			h.log.Warn("watcher_engine_release_failed", slog.String("error", err.Error()))
		}
		return false
	}
	h.owner, h.engine = owner, eng
	// A reload request an earlier owner already answered is not replayed.
	if prev, err := ReadReloadReport(h.cfg.Profile); err == nil && prev != nil {
		if req := readReloadRequest(h.cfg.Profile); req == prev.Request {
			h.lastReload = req
		}
	}
	outcome := "watcher_engine_owner"
	if h.sawOwner {
		outcome = "watcher_engine_took_over"
	}
	h.log.Info(outcome,
		slog.String("profile", h.cfg.Profile),
		slog.Int("pid", os.Getpid()),
		slog.String("lock", path))
	h.relayDone = relayEngine(eng.EventCh(), eng.HealthCh(), h.cfg.DeliverHealth,
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

// reconcile applies the watchers and routes on disk to the running engine
// (#2739), and answers a pending `watcher reload` request. It runs on the
// host's goroutine, never next to Stop's engine shutdown: Stop waits for
// that goroutine before it stops the engine.
func (h *EngineHost) reconcile() {
	h.mu.Lock()
	eng, stopped := h.engine, h.stopped
	h.mu.Unlock()
	if eng == nil || stopped {
		return
	}
	started := time.Now()
	request := readReloadRequest(h.cfg.Profile)
	forced := request != "" && request != h.lastReload

	rep := &ReloadReport{Request: request, PID: os.Getpid(), StartedAt: started}
	rows, err := h.cfg.DB.LoadWatchers()
	if err != nil {
		rep.Error = err.Error()
		if reason := "load_failed:" + err.Error(); reason != h.reconcileWaiting {
			h.reconcileWaiting = reason
			h.log.Warn("watcher_engine_reload_failed",
				slog.String("profile", h.cfg.Profile),
				slog.String("error", err.Error()))
		}
	} else {
		h.reconcileWaiting = ""
		applyWatchers(eng, rows, forced, rep)
	}
	if changed, err := eng.ReloadRoutes(); err != nil {
		rep.RoutesError = err.Error()
	} else {
		rep.RoutesReloaded = changed
	}

	outcome := fmt.Sprint(rep.Started, rep.Stopped, rep.Restarted, rep.Failed, rep.RoutesReloaded, rep.RoutesError)
	if outcome == h.lastReloadLog && !forced {
		return
	}
	h.lastReloadLog = outcome
	if rep.RoutesError != "" {
		h.log.Warn("watcher_routes_reload_failed",
			slog.String("profile", h.cfg.Profile),
			slog.String("error", rep.RoutesError))
	}
	if rep.Changed() {
		h.log.Info("watcher_engine_reloaded",
			slog.String("profile", h.cfg.Profile),
			slog.Bool("requested", forced),
			slog.Any("started", rep.Started),
			slog.Any("stopped", rep.Stopped),
			slog.Any("restarted", rep.Restarted),
			slog.Any("failed", rep.Failed),
			slog.Bool("routes_reloaded", rep.RoutesReloaded))
	}
	if forced {
		h.lastReload = request
		rep.AppliedAt = time.Now()
		if err := writeReloadReport(h.cfg.Profile, rep); err != nil {
			h.log.Warn("watcher_reload_ack_failed", slog.String("error", err.Error()))
		}
	}
}

// listenGrace is how long applyWatchers gives the adapters it started to
// bind their port before it reports one whose Listen already ended.
var listenGrace = 250 * time.Millisecond

// applyWatchers makes eng run exactly the adapters of the running watchers
// in rows, with their current [source] settings: it removes the ones no
// longer running, replaces the ones whose type, name or settings changed,
// and adds the missing ones, recording each in rep. All removals come first,
// so two webhooks that swap ports can both bind. With forced it also
// replaces an adapter that is not running (its Setup failed or its Listen
// ended). A watcher.toml that cannot be read or parsed, or is empty (being
// written), is reported in rep.Failed and its adapter is left as it is.
// Others are left alone.
func applyWatchers(eng *Engine, rows []*statedb.WatcherRow, forced bool, rep *ReloadReport) {
	desired := map[string]bool{}
	for _, row := range rows {
		if row != nil && row.Status == "running" && newWatcherAdapter(row.Type) != nil {
			desired[row.ID] = true
		}
	}
	current := map[string]AdapterInfo{}
	for _, a := range eng.Adapters() {
		if !desired[a.WatcherID] {
			eng.RemoveAdapter(a.WatcherID)
			rep.Stopped = append(rep.Stopped, a.Config.Name)
			continue
		}
		current[a.WatcherID] = a
	}

	type change struct {
		row     *statedb.WatcherRow
		cfg     AdapterConfig
		restart bool
	}
	var changes []change
	for _, row := range rows {
		if row == nil || !desired[row.ID] {
			continue
		}
		settings, err := readSourceSettings(row.Name)
		if err != nil {
			rep.Failed = append(rep.Failed, row.Name+": "+err.Error())
			continue
		}
		cfg := AdapterConfig{Type: row.Type, Name: row.Name, Settings: settings}
		cur, ok := current[row.ID]
		if ok && sameAdapterConfig(cur.Config, cfg) && (cur.Running || !forced) {
			rep.Unchanged++
			continue
		}
		if ok {
			eng.RemoveAdapter(row.ID)
		}
		changes = append(changes, change{row: row, cfg: cfg, restart: ok})
	}
	if len(changes) == 0 {
		return
	}

	maxSilence := loadWatcherSettings().GetMaxSilenceMinutes()
	var added []change
	for _, c := range changes {
		err := eng.AddAdapter(c.row.ID, newWatcherAdapter(c.row.Type), c.cfg, maxSilence)
		switch {
		case errors.Is(err, ErrEngineStopped):
			return
		case err != nil:
			rep.Failed = append(rep.Failed, c.row.Name+": "+err.Error())
		default:
			added = append(added, c)
		}
	}
	if len(added) == 0 {
		return
	}
	// A webhook or github adapter binds its port in Listen, after Setup
	// succeeded: give it a moment, and report one whose Listen ended.
	time.Sleep(listenGrace)
	state := map[string]AdapterInfo{}
	for _, a := range eng.Adapters() {
		state[a.WatcherID] = a
	}
	for _, c := range added {
		if a, ok := state[c.row.ID]; ok && !a.Running {
			reason := a.ListenError
			if reason == "" {
				reason = "its adapter stopped right after it started"
			}
			rep.Failed = append(rep.Failed, c.row.Name+": "+reason)
			continue
		}
		if c.restart {
			rep.Restarted = append(rep.Restarted, c.row.Name)
		} else {
			rep.Started = append(rep.Started, c.row.Name)
		}
	}
}

// sameAdapterConfig reports whether a and b set an adapter up the same way.
func sameAdapterConfig(a, b AdapterConfig) bool {
	if a.Type != b.Type || a.Name != b.Name || len(a.Settings) != len(b.Settings) {
		return false
	}
	for k, v := range a.Settings {
		if bv, ok := b.Settings[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// newWatcherAdapter returns a new adapter of type typ, nil for a type the
// engine does not run.
func newWatcherAdapter(typ string) WatcherAdapter {
	switch typ {
	case "webhook":
		return &WebhookAdapter{}
	case "ntfy":
		return &NtfyAdapter{}
	case "slack":
		return &SlackAdapter{}
	case "github":
		return &GitHubAdapter{}
	}
	return nil
}

// loadWatcherSettings returns the [watcher] settings of the user config.
func loadWatcherSettings() session.WatcherSettings {
	cfg, _ := session.LoadUserConfig()
	if cfg == nil {
		return session.WatcherSettings{}
	}
	return cfg.Watcher
}

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
func startEngine(host HostConfig, log *slog.Logger) *Engine {
	db := host.DB
	if db == nil {
		return nil
	}
	rows, err := db.LoadWatchers()
	if err != nil || runningCount(rows) == 0 {
		return nil
	}

	watcherCfg := loadWatcherSettings()

	// Build engine config. Without a readable clients.json the router starts
	// empty, so routes added later reach it (ReloadRoutes).
	router, _ := LoadFromWatcherDir()
	if router == nil {
		router = NewRouter(nil)
	}
	healthInterval := time.Duration(watcherCfg.GetHealthCheckIntervalSeconds()) * time.Second

	engineCfg := EngineConfig{
		DB:                  db,
		Router:              router,
		MaxEventsPerWatcher: watcherCfg.GetMaxEventsPerWatcher(),
		HealthCheckInterval: healthInterval,
	}
	outbox := newHostOutbox(host, watcherCfg, log)
	if outbox != nil {
		engineCfg.Outbox = outbox
	}
	eng := NewEngine(engineCfg)

	maxSilenceMinutes := watcherCfg.GetMaxSilenceMinutes()

	// Register all running watchers as adapters.
	for _, row := range rows {
		if row.Status != "running" {
			continue
		}
		adapter := newWatcherAdapter(row.Type)
		if adapter == nil {
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
	if outbox != nil {
		// A reboot or a kill may have ended the workers of routed events
		// still queued: this process owns the profile's watchers now.
		outbox.Resume()
	}
	return eng
}

// newHostOutbox returns the outbox an engine delivers its routed events
// through: the send queue next to the profile's state database, read by the
// same `session send-worker` as `session send --queue`. Nil (and an error in
// the log) for a database with no file, which has no profile directory.
func newHostOutbox(host HostConfig, watcherCfg session.WatcherSettings, log *slog.Logger) *ConductorOutbox {
	path := host.DB.Path()
	if path == "" {
		log.Error("watcher_delivery_outbox_unavailable",
			slog.String("profile", host.Profile),
			slog.String("reason", "the state database has no file"))
		return nil
	}
	deadline, err := watcherCfg.GetDeliveryDeadline()
	if err != nil {
		log.Warn("watcher_delivery_deadline_invalid",
			slog.String("value", watcherCfg.DeliveryDeadline),
			slog.String("error", err.Error()))
	}
	return NewConductorOutbox(OutboxConfig{
		Profile:     host.Profile,
		Dir:         sendqueue.Dir(filepath.Dir(path)),
		DB:          host.DB,
		Deadline:    deadline,
		SpawnWorker: host.SpawnSendWorker,
		Logger:      log,
	})
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

// readSourceSettings is loadSourceSettings for a running engine (#2739): a
// missing watcher.toml is no settings, as at start, but one that cannot be
// read or parsed, or is empty because it is being written, is an error, so
// the adapter keeps the settings it runs with.
func readSourceSettings(name string) (map[string]string, error) {
	dir, err := session.WatcherNameDir(name)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "watcher.toml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("watcher.toml is empty; keeping the current settings")
	}
	var cfg struct {
		Source map[string]string `toml:"source"`
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, fmt.Errorf("watcher.toml: %w; keeping the current settings", err)
	}
	out := map[string]string{}
	for k, v := range cfg.Source {
		out[k] = v
	}
	return out, nil
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
