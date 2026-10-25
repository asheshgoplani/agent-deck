package ui

import (
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/safego"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// Init initializes the model
func (h *Home) Init() tea.Cmd {
	// Check for first run (no config.toml exists)
	configPath, _ := session.GetUserConfigPath()
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		h.setupWizard.SetReportKey(h.actionKey(hotkeyReportProblem))
		h.setupWizard.Show()
		h.setupWizard.SetSize(h.width, h.height)
	}

	// Start system stats collection
	if h.sshCollector != nil {
		h.sshCollector.Start()
	}
	if h.sysStatsCollector != nil {
		h.sysStatsCollector.Start()
	}

	// Start interval hooks (no-op if none configured).
	if h.intervalHookRunner != nil {
		h.intervalHookRunner.Start()
	}

	// Fingerprint the running executable so the tick loop can tell when an
	// update lands on disk while the TUI is open.
	if exe, err := os.Executable(); err == nil {
		h.startBinaryWatch(exe, Version)
	}
	h.homebrewManaged = detectHomebrewManaged()
	h.applyAutoUpdateSuppression()
	if h.autoUpdateSuppressedReason == "" {
		if dir, err := agentpaths.CacheDir(); err == nil {
			h.heartbeatDir = dir
			h.startAttachedHeartbeat()
		}
	}

	cmds := []tea.Cmd{
		h.sessionLoadCmd(nil, true),

		h.tick(),
		h.reviverTick(),
		h.triggersPollCmd(), // ⚡ badges and the Triggers panel (triggers_live.go)
		h.requestUpdateCheck(time.Now()),
		h.fetchRemoteSessions,
		h.waitRemoteChange,
		// Opt-in telemetry upload check, now and hourly. MaybeUpload re-reads
		// consent from disk and the kill switches from env, so this is a
		// no-op for everyone who has not said yes.
		telemetryUploadCmd(),
		telemetryUploadTick(),
	}

	// Start listening for storage changes
	if h.storageWatcher != nil {
		cmds = append(cmds, listenForReloads(h.storageWatcher))
	}

	// Start listening for OS theme changes
	if h.themeWatcher != nil {
		cmds = append(cmds, listenForThemeChange(h.themeWatcher))
	}

	// Join the profile's watcher engine election (#2530): this TUI runs the
	// engine if it wins the owner lock, or takes it over when the owner exits.
	cmds = append(cmds, h.startWatcherEngine())

	return tea.Batch(cmds...)
}

// listenForReloads waits for storage change notification
func listenForReloads(sw *StorageWatcher) tea.Cmd {
	return func() tea.Msg {
		if sw == nil {
			return nil
		}
		<-sw.ReloadChannel()
		return storageChangedMsg{}
	}
}

// listenForThemeChange waits for the next OS theme change.
// MUST be re-issued in the Update handler for systemThemeMsg to keep listening.
func listenForThemeChange(tw *ThemeWatcher) tea.Cmd {
	if tw == nil {
		return nil
	}
	return func() tea.Msg {
		isDark, ok := <-tw.ChangeChannel()
		if !ok {
			return nil
		}
		return systemThemeMsg{dark: isDark}
	}
}

// listenForWatcherEvent waits for the next event from the engine's EventCh.
// Must be re-issued after each event to keep listening (Bubble Tea cmd pattern).
func listenForWatcherEvent(ch <-chan watcher.Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return nil
		}
		return watcherEventMsg{event: evt}
	}
}

// listenForWatcherHealth waits for the next health state from the engine's HealthCh.
// Must be re-issued after each state to keep listening.
func listenForWatcherHealth(ch <-chan watcher.HealthState) tea.Cmd {
	return func() tea.Msg {
		state, ok := <-ch
		if !ok {
			return nil
		}
		return watcherHealthMsg{state: state}
	}
}

func (h *Home) stopThemeWatcher() {
	if h.themeWatcher != nil {
		h.themeWatcher.Close()
		h.themeWatcher = nil
	}
}

func (h *Home) startThemeWatcher() tea.Cmd {
	h.stopThemeWatcher()
	h.themeWatcher = NewThemeWatcher(h.ctx)
	if h.themeWatcher == nil {
		return nil
	}
	return listenForThemeChange(h.themeWatcher)
}

// startWatcherEngine starts the watcher host (StartWatcherEngine) for the
// TUI and returns the commands that feed its watcher panel. While another
// process owns the engine the panel feed stays quiet, and the panel refreshes
// from statedb as before (#2530).
func (h *Home) startWatcherEngine() tea.Cmd {
	host := h.StartWatcherEngine()
	if host == nil {
		return nil
	}
	h.watcherPanelEvents, h.watcherPanelHealth = host.PanelEvents(), host.PanelHealth()
	return tea.Batch(
		listenForWatcherEvent(h.watcherPanelEvents),
		listenForWatcherHealth(h.watcherPanelHealth),
	)
}

// StartWatcherEngine joins the profile's watcher engine election (#2530). The
// process that takes the engine owner lock runs the engine; any other TUI,
// `web` or `web --no-tui` process of the profile waits and takes over when
// the owner exits. The engine queues each routed event for its conductor in
// the profile's send queue (#2537); health alerts go to this Home's conductor
// dispatcher. Init calls it for the TUI and the headless web server calls it
// directly, since it runs no Bubble Tea loop. Returns nil without a state
// database; a second call returns the running host.
func (h *Home) StartWatcherEngine() *watcher.EngineHost {
	if host := h.watcherHost.Load(); host != nil {
		return host
	}
	db := statedb.GetGlobal()
	if db == nil {
		return nil
	}
	host := watcher.NewEngineHost(watcher.HostConfig{
		Profile:         h.profile,
		DB:              db,
		DeliverHealth:   h.dispatchHealthAlert,
		SpawnSendWorker: h.spawnSendWorker,
		Logger:          watcherHostLog,
	})
	if !h.watcherHost.CompareAndSwap(nil, host) {
		return h.watcherHost.Load()
	}
	host.Start()
	return host
}

// StopWatcherEngine leaves the engine election on a signal exit. It does not
// wait for the health alert in flight, and an owner keeps the lock until that
// alert returns or the process exits, so another process cannot type into the
// same pane next to it (#2530). Routed events need nothing here: they wait in
// the send queue, whose workers outlive this process (#2537).
func (h *Home) StopWatcherEngine() {
	h.StopWatcherEngineAndDeliveries(0)
}

// StopWatcherEngineAndDeliveries is the clean-exit variant used by quit and
// by the headless server: it also waits up to wait for the health alert in
// flight (stopConductorDeliveries). The owner lock is released once no alert
// is still being sent, so a successor never delivers next to one.
func (h *Home) StopWatcherEngineAndDeliveries(wait time.Duration) {
	h.stopWatcherEngine(func() <-chan struct{} {
		h.stopConductorDeliveries(wait)
		return h.deliveryQueue().finished()
	})
}

func (h *Home) stopWatcherEngine(drain func() <-chan struct{}) {
	if host := h.watcherHost.Load(); host != nil {
		host.Stop(drain)
		return
	}
	drain()
}

// propagateThemeToSessions updates COLORFGBG in all running tmux sessions
// so that terminal-aware tools pick up the new light/dark setting.
func (h *Home) propagateThemeToSessions() {
	colorfgbg := session.ThemeColorFGBG()
	if colorfgbg == "" {
		return
	}
	h.instancesMu.RLock()
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)
	h.instancesMu.RUnlock()

	safego.Go(uiLog, "apply_theme_to_sessions", func() {
		for _, inst := range instances {
			// ExistsCached() (cache/pipe only): iterating every instance with
			// Exists() would subprocess-probe each uncached/dead session, storming
			// the tmux server on a theme toggle. This runs off the UI goroutine so
			// it can't freeze the main loop, but the storm class is the same one
			// the tickMsg fix eliminates. A live-but-uncached session keeps its
			// stale COLORFGBG until the next theme change — cosmetic only.
			if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil && tmuxSess.ExistsCached() {
				_ = tmuxSess.SetEnvironment("COLORFGBG", colorfgbg)
				_ = tmuxSess.ApplyThemeOptions()
			}
		}
	})
}

// tick returns a command that sends a tick message at regular intervals
// Status updates use time-based cooldown to prevent flickering
func (h *Home) tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// reviverTick fires every 60s to sweep the session list for instances whose
// tmux server survived an SSH scope cleanup but whose control pipe got
// reaped. See .planning/v178-ssh-reviver/PLAN.md (REPORT-D).
func (h *Home) reviverTick() tea.Cmd {
	return tea.Tick(60*time.Second, func(_ time.Time) tea.Msg {
		return reviverTickMsg{}
	})
}

// refreshWatcherPanel loads watcher and event data from statedb and updates the panel.
// Safe to call when watcherPanel is hidden; data is preloaded for when the panel opens.
func (h *Home) refreshWatcherPanel() {
	db := statedb.GetGlobal()
	if db == nil {
		return
	}

	watchers, err := db.LoadWatchers()
	if err != nil {
		return
	}

	items := make([]WatcherDisplayItem, len(watchers))
	for i, w := range watchers {
		healthStatus := w.Status // default: same as status
		if w.Status == "running" {
			healthStatus = "healthy"
		}
		items[i] = WatcherDisplayItem{
			ID:           w.ID,
			Name:         w.Name,
			Type:         w.Type,
			Status:       w.Status,
			HealthStatus: healthStatus,
			Conductor:    w.Conductor,
		}

		// Count events in the last hour to compute events-per-hour rate.
		events, evtErr := db.LoadWatcherEvents(w.ID, 100)
		if evtErr == nil {
			cutoff := time.Now().Add(-time.Hour)
			count := 0
			for _, e := range events {
				if e.CreatedAt.After(cutoff) {
					count++
				}
			}
			items[i].EventsPerHour = float64(count)
		}
	}
	h.watcherPanel.SetWatchers(items)

	// If a watcher is selected in detail mode, load its recent events.
	if sel := h.watcherPanel.SelectedWatcher(); sel != nil {
		events, evtErr := db.LoadWatcherEvents(sel.ID, 10)
		if evtErr == nil {
			displayEvents := make([]WatcherEventDisplay, len(events))
			for i, e := range events {
				displayEvents[i] = WatcherEventDisplay{
					Timestamp: e.CreatedAt,
					Sender:    e.Sender,
					Subject:   e.Subject,
					RoutedTo:  e.RoutedTo,
					SessionID: e.SessionID,
				}
			}
			h.watcherPanel.SetEvents(displayEvents)
		}
	}
}
