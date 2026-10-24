package ui

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/asheshgoplani/agent-deck/internal/intervalhook"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/menudata"
	"github.com/asheshgoplani/agent-deck/internal/safego"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func newNotesEditor() textarea.Model {
	editor := textarea.New()
	editor.ShowLineNumbers = false
	editor.Placeholder = "Write notes for this session..."
	editor.Prompt = ""
	editor.Blur()
	return editor
}

// NewHome creates a new home model with the default profile
func NewHome() *Home {
	return NewHomeWithProfile("")
}

// NewHomeWithProfile creates a new home model with the specified profile.
func NewHomeWithProfile(profile string) *Home {
	return NewHomeWithProfileAndMode(profile)
}

// TestMain disables eager workers for unit tests. Production keeps the default
// so status, log, pipe, and storage updates continue while the TUI is running.
var homeBackgroundWorkersEnabled = true

// startLogMaintenance runs log maintenance (truncate large logs, remove
// orphaned ones) in the background. It shells out to `tmux list-sessions`, so
// it is a background worker: in test mode a goroutine nobody waits for would
// run tmux under whatever PATH a later test has swapped in (issue 2712).
func startLogMaintenance(name string) {
	if !homeBackgroundWorkersEnabled {
		return
	}
	launchLogMaintenance(name)
}

// launchLogMaintenance is a variable so tests can observe startLogMaintenance.
var launchLogMaintenance = func(name string) {
	safego.Go(uiLog, name, func() {
		logSettings := session.GetLogSettings()
		tmux.RunLogMaintenance(logSettings.MaxSizeMB, logSettings.MaxLines, logSettings.GetRemoveOrphans())
	})
}

// shouldAutoInstallCursorHooks reports whether TUI startup should run the
// Cursor hook auto-install/watcher-start block in NewHomeWithProfileAndMode:
// background workers must be enabled, the user must not have durably opted
// out via [cursor] hooks_enabled = false (persisted by `agent-deck
// cursor-hooks uninstall`, issue #1672), and a cursor command must be
// configured. cursorCmd is expected to already be trimmed by the caller.
//
// Extracted as a pure predicate (issue #1675) so the gate itself is
// unit-testable without exercising NewHomeWithProfileAndMode's side effects
// (storage, tmux, goroutines) — mirrors why AutoInstallCursorHooks was pulled
// out of this same function in #1673.
func shouldAutoInstallCursorHooks(userConfig *session.UserConfig, cursorCmd string) bool {
	cursorHooksEnabled := userConfig == nil || userConfig.Cursor.GetHooksEnabled()
	return homeBackgroundWorkersEnabled && cursorHooksEnabled && cursorCmd != ""
}

func shouldPromptHermesHooks(installed bool, decision string) bool {
	return !installed && decision == ""
}

// claudeHooksStartup is what the TUI does about Claude hooks at startup.
type claudeHooksStartup struct {
	prompt    bool // ask the user to install
	reinstall bool // silently (re)install / repair drift
	watch     bool // start the hook status watcher
}

// claudeHooksStartupDecision is the pure gate behind the Claude hooks
// startup prompt. present: every event carries an agent-deck entry in some
// form; installed: with the current config too (no drift); prompted: the
// recorded answer ("", "accepted", "declined").
//
// Review round 3 (finding 4): an install made with `agent-deck hooks
// install` and never through the TUI has no recorded answer, and after an
// upgrade its entries drift (marker-less Stop row). That used to read as
// "not installed, never asked" and re-prompted a user who had already
// installed. Presence is consent: a present-but-drifted install is repaired
// silently and never prompted; only an ABSENT install with no answer asks.
func claudeHooksStartupDecision(present, installed bool, prompted string) claudeHooksStartup {
	switch {
	case installed:
		return claudeHooksStartup{watch: true}
	case present:
		return claudeHooksStartup{reinstall: true, watch: true}
	case prompted == "accepted":
		// User previously accepted but hooks got removed: re-install silently.
		return claudeHooksStartup{reinstall: true, watch: true}
	case prompted != "":
		// "declined": user doesn't want hooks, skip.
		return claudeHooksStartup{}
	default:
		return claudeHooksStartup{prompt: true}
	}
}

// NewHomeWithProfileAndMode creates a new Home with the specified profile.
// All instances manage the notification bar equally via shared SQLite state.
func NewHomeWithProfileAndMode(profile string) *Home {
	// Read (and unset) what a predecessor left for us before anything else
	// in this process can spawn a child (restart.go).
	restartHandoff := consumeRestartEnv()
	ctx, cancel := context.WithCancel(context.Background())
	// Probe installed CLIs for their model lists off the UI goroutine so the
	// first new-session dialog does not wait on it (#2388).
	session.WarmModelCatalog()

	var storageWarning string
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		// Log the error and set warning - sessions won't persist but app will still function
		uiLog.Warn("storage_init_failed", slog.String("error", err.Error()))
		storageWarning = fmt.Sprintf("⚠ Storage unavailable: %v (sessions won't persist)", err)
		storage = nil
	}

	// Ensure StateDB global is set for cross-package status writes.
	// Registration and election happen in main.go before NewHome is called.
	// This fallback handles CLI paths (e.g., NewHomeWithProfile) that skip main.go setup.
	if storage != nil && statedb.GetGlobal() == nil {
		if db := storage.GetDB(); db != nil {
			statedb.SetGlobal(db)
			_ = db.RegisterInstance(false)
		}
	}

	// Get the actual profile name (could be resolved from env var or config)
	actualProfile := session.DefaultProfile
	if storage != nil {
		actualProfile = storage.Profile()
	}

	var statusWorkerDone chan struct{}
	if homeBackgroundWorkersEnabled {
		statusWorkerDone = make(chan struct{})
	}

	h := &Home{
		restartHandoff:            restartHandoff,
		profile:                   actualProfile,
		storage:                   storage,
		storageWarning:            storageWarning,
		search:                    NewSearch(),
		newDialog:                 NewNewDialog(),
		groupDialog:               NewGroupDialog(),
		forkDialog:                NewForkDialog(),
		confirmDialog:             NewConfirmDialog(),
		hookTrustDialog:           NewHookTrustDialog(),
		helpOverlay:               NewHelpOverlay(),
		mcpDialog:                 NewMCPDialog(),
		pluginDialog:              NewPluginDialog(),
		editPathsDialog:           NewEditPathsDialog(),
		editSessionDialog:         NewEditSessionDialog(),
		skillDialog:               NewSkillDialog(),
		setupWizard:               NewSetupWizard(),
		settingsPanel:             NewSettingsPanel(),
		analyticsPanel:            NewAnalyticsPanel(),
		geminiModelDialog:         NewGeminiModelDialog(),
		promptInputDialog:         NewPromptInputDialog(),
		sessionPickerDialog:       NewSessionPickerDialog(),
		codeBlockDialog:           NewCodeBlockDialog(),
		sessionSwitcher:           NewSessionSwitcher(),
		scrollbackPager:           NewScrollbackPager(),
		contextPager:              NewContextPager(),
		worktreeFinishDialog:      NewWorktreeFinishDialog(),
		feedbackDialog:            NewFeedbackDialog(),
		reportDialog:              NewReportDialog(),
		telemetryDialog:           NewTelemetryDialog(),
		zoxidePicker:              NewZoxidePicker(),
		feedbackSender:            feedback.NewSender(),
		watcherPanel:              NewWatcherPanel(),
		agentsPanel:               NewAgentsPanel(),
		deadLetterPanel:           NewDeadLetterPanel(),
		triggersFeed:              stateDBTriggersFeed{},
		toolVisibilityPanel:       NewToolVisibilityPanel(),
		insertBatchDuration:       defaultInsertBatchDuration,
		insertOpenKeySender:       defaultInsertOpenKeySender,
		cursor:                    0,
		sidebarMode:               sidebarGrouped,
		compactSidebar:            false,
		embeddedLayout:            false,
		sidebarDensity:            session.DefaultSidebarDensity,
		initialLoading:            true, // Show splash until sessions load
		ctx:                       ctx,
		cancel:                    cancel,
		instances:                 []*session.Instance{},
		instanceByID:              make(map[string]*session.Instance),
		switchGenerations:         make(map[string]uint64),
		groupTree:                 session.NewGroupTree([]*session.Instance{}),
		flatItems:                 []session.Item{},
		previewCache:              make(map[string]string),
		previewCacheTime:          make(map[string]time.Time),
		analyticsCache:            make(map[string]*session.SessionAnalytics),
		geminiAnalyticsCache:      make(map[string]*session.GeminiSessionAnalytics),
		analyticsCacheTime:        make(map[string]time.Time),
		contextReportCache:        make(map[string]contextReportEntry),
		clearOnCompactSent:        make(map[string]time.Time),
		launchingSessions:         make(map[string]time.Time),
		resumingSessions:          make(map[string]time.Time),
		remoteRestarting:          make(map[string]struct{}),
		remoteForking:             make(map[string]struct{}),
		mcpLoadingSessions:        make(map[string]time.Time),
		forkingSessions:           make(map[string]time.Time),
		setupRunningSessions:      make(map[string]time.Time),
		creatingSessions:          make(map[string]*CreatingSession),
		lastLogActivity:           make(map[string]time.Time),
		windowsCollapsed:          make(map[string]bool),
		parentChildrenCollapsed:   make(map[string]bool),
		remoteGroupsCollapsed:     make(map[string]bool),
		remoteSessionOrder:        make(remoteOrder),
		worktreeDirtyCache:        make(map[string]bool),
		worktreeDirtyCacheTs:      make(map[string]time.Time),
		statusTrigger:             make(chan statusUpdateRequest, 1), // Buffered to avoid blocking
		statusWorkerDone:          statusWorkerDone,
		idleTimeoutWatcher:        session.NewIdleTimeoutWatcher(session.IdleTimeoutWatcherConfig{}),
		lastPersistedStatus:       make(map[string]string),
		lastPersistedAutoNameDesc: make(map[string]string),
		logUpdateChan:             make(chan *session.Instance, 100), // Buffered to absorb bursts
		hotkeys:                   make(map[string]string),
		hotkeyLookup:              make(map[string]string),
		blockedHotkeys:            make(map[string]bool),
		notesEditor:               newNotesEditor(),
		boundKeys:                 make(map[string]string),
		undoStack:                 make([]deletedSessionEntry, 0, 10),
		pendingTitleChanges:       make(map[string]pendingTitle),
		debugMode:                 logging.IsDebugEnabled(),
		lastClickIndex:            -1,
		mru:                       session.NewMRUHistory(session.DefaultMRUCapacity),
	}
	h.sessionRenderSnapshot.Store(make(map[string]sessionRenderState))

	h.reloadHotkeysFromConfig()

	// Fzf-like Default Path suggestions in the group dialog share the unified
	// frecency-ranked corpus (recents + group defaults) with the zoxide picker.
	h.groupDialog.SetSuggestProvider(h.groupDialogPathCandidates)

	// Cache display settings (config.toml [display]) and resolve the
	// status-bar cost-line template once. The template + hide flag are
	// reused on every render; see (*Home).renderStats.
	if cfg, _ := session.LoadUserConfig(); cfg != nil {
		// [performance] claim_polling: snapshot once at startup. Defaults to
		// false (today's behavior); stays false when config is unreadable.
		h.claimPolling = cfg.ClaimPollingEnabled()
		h.fullRepaint = cfg.Display.GetFullRepaint()
		h.defaultFilter = cfg.Display.GetDefaultFilter()
		h.activeFilterLabel = cfg.Display.ActiveFilterLabel
		h.activeFilterExcludes = cfg.Display.GetActiveFilterExcludes()
		h.hiddenToolBadge = hiddenToolBadgeFor(cfg)
		session.ConfigureTmuxDisplay(cfg.Display)
		h.showSessionTimestamps = cfg.Display.ShowSessionTimestamps
		h.showPaneTitles = cfg.Display.ShowPaneTitles
		h.sysStatsConfig = cfg.SystemStats
		h.costLineTemplate, h.costLineHideWhenZero = session.ResolveCostLineTemplate(cfg, actualProfile)
		h.previewPct = cfg.UI.GetPreviewPct()
		h.previewOrientation = cfg.UI.GetPreviewOrientation()
		h.embeddedLayout = cfg.UI.GetEmbeddedTerminal()
		h.compactSidebar = h.embeddedLayout
		h.sidebarDensity = cfg.UI.GetSidebarDensity()
		h.remoteLatencyRefreshSec = cfg.UI.GetRemoteLatencyRefreshSecs(cfg.SystemStats.GetRefreshSeconds())
		h.remoteSessionRefreshSec = cfg.UI.GetRemoteSessionRefreshSecs()
		h.footerMode = cfg.UI.GetFooter()
		h.attachOnCreate = cfg.UI.GetAttachOnCreate()
		h.viewModeOpts.ActiveIncludesIdle = cfg.UI.GetActiveIncludesIdle()
	} else {
		h.fullRepaint = (session.DisplaySettings{}).GetFullRepaint()
		h.activeFilterExcludes = (session.DisplaySettings{}).GetActiveFilterExcludes()
		h.costLineTemplate, h.costLineHideWhenZero = session.ResolveCostLineTemplate(nil, actualProfile)
		h.previewPct = session.DefaultPreviewPct
		h.previewOrientation = session.DefaultPreviewOrientation
		h.embeddedLayout = (session.UISettings{}).GetEmbeddedTerminal()
		h.compactSidebar = h.embeddedLayout
		h.remoteLatencyRefreshSec = (session.UISettings{}).GetRemoteLatencyRefreshSecs(0)
		h.remoteSessionRefreshSec = (session.UISettings{}).GetRemoteSessionRefreshSecs()
		h.footerMode = (session.UISettings{}).GetFooter()
	}
	h.remoteLatency = make(map[string]session.RemoteLatency)

	// Initialize system stats collector if enabled
	if h.sysStatsConfig.GetEnabled() {
		h.sysStatsCollector = sysinfo.NewCollector(h.sysStatsConfig.GetRefreshSeconds(), nil)
	}
	if cfg, _ := session.LoadUserConfig(); cfg != nil && slices.Contains(cfg.UI.GetHeaderFields(), session.PreviewFieldSSH) {
		h.sshCollector = sysinfo.NewSSHCollector(0)
	}
	h.accountsUsageCache = session.NewAccountUsageCache()

	// Interval-hook runner. Constructed unconditionally (cheap); Start() is a
	// no-op when no [interval_hooks] are configured, and hooks are re-read from
	// config each tick so they can be added/removed without a restart.
	// Registered globally so the signal-exit path in cmd/agent-deck/main.go
	// can stop in-flight hook runs before os.Exit (#1829); the in-app quit
	// path stops it via performFinalShutdown.
	h.intervalHookRunner = intervalhook.New(uiLog)
	intervalhook.SetGlobal(h.intervalHookRunner)

	// Keep settings panel profile-aware so profile overrides (e.g., Claude config dir)
	// are displayed and edited in the correct scope.
	h.settingsPanel.SetProfile(actualProfile)

	// Restore persisted UI state (preview mode, status filter, cursor position)
	h.loadUIState()
	h.loadRemoteSessionsCache()
	h.remoteVersions = session.LoadRemoteVersions()
	h.remotePolls = session.LoadRemotePolls()

	// Apply default_filter from config if no filter was restored from persisted state.
	// Concrete status filters fall back to All when nothing matches; the active
	// filter remains selected so stopped/error rows stay hidden across restarts.
	if h.statusFilter == "" && h.defaultFilter != "" {
		h.statusFilter = session.Status(h.defaultFilter)
	}

	tmuxSettings := session.GetTmuxSettings()
	h.manageTmuxNotifications = tmuxSettings.GetInjectStatusLine()

	// Initialize notification manager if enabled in config and tmux status injection is allowed.
	// All instances manage the notification bar (they share SQLite state, so produce identical output)
	notifSettings := session.GetNotificationsSettings()
	if notifSettings.GetEnabled() && h.manageTmuxNotifications {
		h.notificationsEnabled = true
		h.notificationManager = session.NewNotificationManager(notifSettings.MaxShown, notifSettings.ShowAll, notifSettings.Minimal)

		// Initialize tmux status bar options for proper notification display
		// Fixes truncation (default status-left-length is only 10 chars)
		_ = tmux.InitializeStatusBarOptions()

	}

	// Bind mouse click on status-right to detach (click the "ctrl+q/click detach" hint)
	// This is unconditional — the status-right always shows the detach hint
	_ = tmux.BindMouseStatusRightDetach()

	h.liveSet = newPipeLiveSet(livePipeLRUCapacity)

	if homeBackgroundWorkersEnabled {
		// Initialize event-driven status detection. The output callback is invoked
		// when PipeManager detects output from a session.
		outputCallback := func(sessionName string) {
			h.instancesMu.RLock()
			for _, inst := range h.instances {
				if inst.GetTmuxSession() != nil && inst.GetTmuxSession().Name == sessionName {
					h.logActivityMu.Lock()
					lastUpdate := h.lastLogActivity[inst.ID]
					if time.Since(lastUpdate) < logOutputDebounce {
						h.logActivityMu.Unlock()
						break
					}
					h.lastLogActivity[inst.ID] = time.Now()
					h.logActivityMu.Unlock()

					select {
					case h.logUpdateChan <- inst:
					default:
					}
					break
				}
			}
			h.instancesMu.RUnlock()
		}

		// Control mode pipes provide event-driven, zero-subprocess status detection.
		pm := tmux.NewPipeManager(h.ctx, outputCallback)
		pm.SetWindowChangeCallback(func() {
			tmux.RefreshSessionCache()
		})
		tmux.SetPipeManager(pm)
		pm.SetWantPipe(func(name string) bool { return h.liveSet.want(name) })
		pm.SetSharedViewOverrides(session.SharedViewOverrides)

		go h.livePipeReconciler()
		go h.statusWorker()
		h.startLogWorkers()
	}

	// Recall search (the G key) reads recall.db, the on-disk index that
	// `agent-deck recall` maintains (docs/recall.md). It replaced the
	// in-memory global search index, which opened one directory watcher per
	// project and loaded every transcript into memory (6+ GB, OOM-killed).
	// Opening the index is one SQLite open; nothing is parsed here, and with
	// [recall] enabled = false the key falls back to the local title search.
	h.globalSearch = NewGlobalSearch()
	if src, err := openRecallIndex(actualProfile); err != nil {
		uiLog.Warn("recall_index_open_failed", slog.String("error", err.Error()))
		h.recallOff = "Recall index unavailable: " + err.Error()
	} else if src != nil {
		h.recallSource = src
		h.globalSearch.SetSource(src)
	} else {
		h.recallOff = recallOffNotice
	}

	// Initialize MCP socket pool if enabled
	// Note: Pool initialization happens AFTER loading sessions so we can discover MCPs in use
	// Pool will be initialized in Init() after sessions are loaded

	// Initialize storage watcher for auto-reload
	// Polls SQLite metadata for external changes (CLI commands, other instances)
	// and triggers reload with state preservation
	if homeBackgroundWorkersEnabled && storage != nil {
		watcher, err := NewStorageWatcher(storage.GetDB())
		if err != nil {
			uiLog.Warn("storage_watcher_init_failed", slog.String("error", err.Error()))
		} else if watcher != nil {
			h.storageWatcher = watcher
			watcher.Start()
		}
	}

	// SSE-based status detection for OpenCode sessions (issue #1614).
	// Connections are opened lazily by backgroundStatusUpdate's Sync() call,
	// so creating the watcher here is free when no OpenCode sessions exist.
	if homeBackgroundWorkersEnabled {
		h.sseWatcher = session.NewOpenCodeSSEWatcher(nil)
	}

	// Hook-based status detection (Claude Code lifecycle hooks)
	userConfig, _ := session.LoadUserConfig()
	// Seed the account-badge gate from the same config read; the settings panel
	// refreshes it on save so adding a slot lights the badges without a restart.
	h.accountSlotsConfigured.Store(len(session.ConfiguredAccountNames(userConfig)) > 0)
	hooksEnabled := userConfig == nil || userConfig.Claude.GetHooksEnabled()
	if homeBackgroundWorkersEnabled && hooksEnabled {
		configDir := session.GetClaudeConfigDir()
		prompted := ""
		if db := statedb.GetGlobal(); db != nil {
			prompted, _ = db.GetMeta("hooks_prompted")
		}
		decision := claudeHooksStartupDecision(
			session.CheckClaudeHooksPresent(configDir),
			session.CheckClaudeHooksInstalled(configDir),
			prompted,
		)
		if decision.reinstall {
			// Hooks are present but drifted (a marker-less or dangling entry
			// after an upgrade), or the user accepted once and they were
			// removed: repair silently. A build outside the install dirs
			// keeps the pinned program (finding 2), so this never re-pins.
			if _, err := session.InjectClaudeHooks(configDir); err != nil {
				uiLog.Warn("hook_reinstall_failed", slog.String("error", err.Error()))
			} else {
				uiLog.Info("claude_hooks_reinstalled", slog.String("config_dir", configDir))
			}
		}
		if decision.reinstall || decision.watch {
			logUsageFeedResults(session.HealUsageFeeds(userConfig))
		}
		if decision.watch {
			hookWatcher, err := session.NewStatusFileWatcher(nil)
			if err != nil {
				uiLog.Warn("hook_watcher_init_failed", slog.String("error", err.Error()))
			} else {
				h.hookWatcher = hookWatcher
				go hookWatcher.Start()
			}
		}
		if decision.prompt {
			h.pendingHooksPrompt = true
		}
	}

	// Hermes shell hooks require their own consent because they mutate Hermes's
	// config.yaml. An installed hook set predates (or embodies) consent and does
	// not prompt. Any recorded decision suppresses future prompts; in particular,
	// accepted hooks that are later removed stay removed because removal revokes
	// consent rather than triggering a silent reinstall.
	if hermesCmd := strings.TrimSpace(session.GetToolCommand("hermes")); homeBackgroundWorkersEnabled && hermesCmd != "" {
		// GetToolCommand may return a full command string with arguments
		// (e.g. "hermes --gateway-url=..."). LookPath needs the binary name only.
		// Trim first because Fields("") and Fields("   ") both return [], and
		// indexing [0] on an empty slice panics.
		if hermesFields := strings.Fields(hermesCmd); len(hermesFields) > 0 {
			hermesBin := hermesFields[0]
			if _, err := exec.LookPath(hermesBin); err == nil {
				hermesConfigDir := session.GetHermesConfigDir()
				installed := session.CheckHermesHooksInstalled(hermesConfigDir)
				decision := ""
				if db := statedb.GetGlobal(); db != nil {
					decision, _ = db.GetMeta("hermes_hooks_prompted")
				}
				if shouldPromptHermesHooks(installed, decision) {
					h.pendingHermesHooksPrompt = true
				}
				if installed && h.hookWatcher == nil {
					if hookWatcher, err := session.NewStatusFileWatcher(nil); err == nil {
						h.hookWatcher = hookWatcher
						go hookWatcher.Start()
					}
				}
			}
		}
	}

	// Cursor Agent CLI hooks: auto-inject silently when the resolved Cursor CLI
	// binary (`agent` or `cursor`) is available, unless the user opted out via
	// [cursor] hooks_enabled = false (set durably by `agent-deck cursor-hooks
	// uninstall`, issue #1672). The opt-out gates the watcher too, matching the
	// [claude] hooks_enabled gate above.
	cursorCmd := strings.TrimSpace(session.GetToolCommand("cursor"))
	if shouldAutoInstallCursorHooks(userConfig, cursorCmd) {
		if cursorFields := strings.Fields(cursorCmd); len(cursorFields) > 0 {
			cursorBin := cursorFields[0]
			if _, err := exec.LookPath(cursorBin); err == nil {
				cursorConfigDir := session.GetCursorConfigDir()
				if installed, err := session.AutoInstallCursorHooks(userConfig, cursorConfigDir); err != nil {
					uiLog.Warn("cursor_hooks_inject_failed", slog.String("error", err.Error()))
				} else if installed {
					uiLog.Info("cursor_hooks_installed", slog.String("config_dir", cursorConfigDir))
				}
				if h.hookWatcher == nil {
					if hookWatcher, err := session.NewStatusFileWatcher(nil); err == nil {
						h.hookWatcher = hookWatcher
						go hookWatcher.Start()
					}
				}
			}
		}
	}

	// Start system theme watcher if configured
	if session.GetTheme() == "system" {
		h.themeWatcher = NewThemeWatcher(ctx)
	}

	// Run log maintenance at startup (non-blocking)
	// This truncates large log files and removes orphaned logs based on user config
	// Also initializes lastLogMaintenance and lastLogCheck so periodic checks start from now
	h.lastLogMaintenance = time.Now()
	h.lastLogCheck = time.Now()
	startLogMaintenance("startup_log_maintenance")

	// v1.7.60: one-shot nav-discoverability hint. Reuses the maintenance-banner
	// slot so no extra layout math is needed. Dismisses via the existing ESC
	// handler, or gets overwritten by a real maintenance event — either way
	// the sentinel file is written so the hint never reappears.
	if !navHintAlreadyShown() {
		h.maintenanceMsg = navHintText
		h.maintenanceMsgTime = time.Now()
		h.navHintActive = true
		markNavHintShown()
	}

	return h
}

// SetWebMenuData configures an optional in-memory menu sink for web mode.
func (h *Home) SetWebMenuData(menuData *menudata.MemoryMenuData) {
	h.webMenuDataMu.Lock()
	h.webMenuData = menuData
	h.webMenuDataMu.Unlock()
	if !h.initialLoading {
		h.publishWebMenuSnapshot()
	}
}

func (h *Home) getWebMenuData() *menudata.MemoryMenuData {
	h.webMenuDataMu.RLock()
	defer h.webMenuDataMu.RUnlock()
	return h.webMenuData
}

// SetCostStore sets the cost store for cost tracking display.
func (h *Home) SetCostStore(store *costs.Store) {
	h.costStore = store
}

// SetCostPricer sets the pricer for cost calculations.
func (h *Home) SetCostPricer(pricer *costs.Pricer) {
	h.costPricer = pricer
}

// SetCostBudget sets the budget checker for cost limits.
func (h *Home) SetCostBudget(budget *costs.BudgetChecker) {
	h.costBudget = budget
}

// SetGroupScope limits the TUI to sessions within the given group path.
// The path is normalized: lowercased and spaces replaced with hyphens.
func (h *Home) SetGroupScope(path string) {
	h.groupScopeMu.Lock()
	h.groupScope = strings.ToLower(strings.ReplaceAll(path, " ", "-"))
	h.groupScopeMu.Unlock()
}

// SetInitialSelection queues a session to preselect on first render (#709).
// The value may be a session ID or a title. Preselection runs AFTER
// rebuildFlatItems so it respects any active group scope: if the session is
// outside the scope, applyInitialSelection returns false and the caller may
// warn. Crucially, SetInitialSelection does NOT hide any groups — every group
// configured by the user stays visible in the sidebar.
func (h *Home) SetInitialSelection(idOrTitle string) {
	h.initialSelect = strings.TrimSpace(idOrTitle)
	h.initialSelectDone = false
}

// applyInitialSelection positions the cursor on the session matching
// h.initialSelect, if any. Returns true if a match was found and the cursor
// was moved, false otherwise. Idempotent — after one successful apply, further
// calls are no-ops so normal cursor navigation is not overridden.
//
// Unlike SelectSessionByID this method does NOT clear group scope or status
// filters — the caller may have set a scope intentionally via -g/--group, and
// --select must respect that constraint. Only sessions visible in the current
// flat view are eligible.
func (h *Home) applyInitialSelection() bool {
	if h.initialSelectDone || h.initialSelect == "" {
		return false
	}
	// Try id-path first: searches only the visible flat view, so group scope
	// and status filters are naturally honoured (no reveal behaviour here).
	if idx := h.flatItemIndexByID(h.initialSelect); idx >= 0 {
		h.cursor = idx
		h.initialSelectDone = true
		h.syncViewport()
		return true
	}
	// Fall back to a title match — initialSelect may be a title, not an id.
	wanted := strings.ToLower(strings.TrimSpace(h.initialSelect))
	for i, fi := range h.flatItems {
		if fi.Type != session.ItemTypeSession || fi.Session == nil {
			continue
		}
		if strings.EqualFold(fi.Session.Title, h.initialSelect) ||
			strings.ToLower(fi.Session.Title) == wanted {
			h.cursor = i
			h.initialSelectDone = true
			h.syncViewport()
			return true
		}
	}
	return false
}

// flatItemIndexByID returns the flatItems index of the session row with the
// given id, or -1 if it is not present in the current flat view.
func (h *Home) flatItemIndexByID(id string) int {
	for i, fi := range h.flatItems {
		if fi.Type == session.ItemTypeSession && fi.Session != nil && fi.Session.ID == id {
			return i
		}
	}
	return -1
}

// SelectSessionByID reveals and selects the session with the given id within
// the active (non-archived) view: if the target is hidden by a status filter or
// group scope it clears them, and if its group is collapsed it expands the
// group (and parents), then moves the cursor. Returns true if the session was
// found and selected. Archived sessions and unknown/foreign ids return false
// and leave the cursor unchanged.
func (h *Home) SelectSessionByID(id string) bool {
	if id == "" {
		return false
	}

	// Fast path: already visible in the current flat view.
	if idx := h.flatItemIndexByID(id); idx >= 0 {
		h.cursor = idx
		h.syncViewport()
		return true
	}

	// Confirm the target exists in this profile, is not archived, and learn its
	// group path for expansion.
	var target *session.Instance
	h.instancesMu.RLock()
	for _, inst := range h.instances {
		if inst.ID == id {
			target = inst
			break
		}
	}
	h.instancesMu.RUnlock()
	if target == nil || target.IsArchived() {
		return false
	}

	// Reveal within the active view: drop filters that could hide the target and
	// expand its containing group, then rebuild and locate it.
	h.statusFilter = ""
	h.groupScopeMu.Lock()
	h.groupScope = ""
	h.groupScopeMu.Unlock()
	if target.GroupPath != "" {
		h.groupTree.ExpandGroupWithParents(target.GroupPath)
	}
	h.revealParentFold(target)
	h.rebuildFlatItems()

	if idx := h.flatItemIndexByID(id); idx >= 0 {
		h.cursor = idx
		h.syncViewport()
		return true
	}
	return false
}

// consumeFocusRequest honors a pending `agent-deck session focus <id>` request.
// It is called once per tick. The row is cleared unconditionally (consume-once)
// so an unknown, stale, or foreign id never re-fires on a later tick or lingers
// past its purpose. It returns a non-nil tea.Cmd only when the request asked to
// --attach the session and that session is live: the caller runs the cmd to
// open it (the same path as pressing Enter). A select-only request returns nil.
func (h *Home) consumeFocusRequest(db *statedb.StateDB) tea.Cmd {
	if db == nil {
		return nil
	}
	// Atomic read-and-clear: a separate read-then-clear has a window where a
	// concurrent CLI `session focus` write lands between them and gets wiped.
	// Consume-once still holds — a stale/unknown id is cleared by the take below.
	raw, err := session.TakeFocusRequest(db)
	if err != nil || raw == "" {
		return nil
	}

	fr, ok, fresh := session.ParseFocusRequest(raw, time.Now().UnixNano(), session.FocusRequestTTL)
	if !ok || !fresh {
		return nil
	}
	if fr.Remote != "" {
		// #2704: a session on a remote, selected under Remotes.
		return h.consumeRemoteFocus(fr)
	}
	// A newer local request replaces a pending remote one.
	h.pendingRemoteFocus = nil
	id, attach := fr.ID, fr.Attach
	if !h.SelectSessionByID(id) {
		return nil
	}
	if !attach {
		return nil
	}
	// Attach intent: open the session as if the user pressed Enter on it. Skip
	// when the session has no live tmux pane (attachSession would no-op anyway).
	inst := h.getInstanceByID(id)
	if inst == nil || !inst.Exists() {
		return nil
	}
	// attachSession returns nil when there's no local tmux pane to attach (e.g.
	// GetTmuxSession()==nil on a cross-socket session) and sets h.isAttaching
	// itself on the real attach path. Don't pre-set it here: a premature set
	// followed by a nil return would leave isAttaching stuck true and suppress
	// View() forever. Mirror the attach_on_create path and guard on the cmd.
	return h.attachSession(inst)
}

// isInGroupScope returns true if the given path is within the active group scope.
// Returns true for all paths when no scope is set.
func (h *Home) isInGroupScope(path string) bool {
	return pathInScope(path, h.groupScope)
}

// scopedGroupPaths returns group paths filtered to the active scope.
// Returns all paths when no scope is set.
func (h *Home) scopedGroupPaths() []string {
	allPaths := h.groupTree.GetGroupPaths()
	if h.groupScope == "" {
		return allPaths
	}
	var scoped []string
	for _, p := range allPaths {
		if h.isInGroupScope(p) {
			scoped = append(scoped, p)
		}
	}
	return scoped
}

// groupScopeDisplayName returns the human-readable name for the active group scope.
// Falls back to the raw scope path if the group is not found in the tree.
func (h *Home) groupScopeDisplayName() string {
	if h.groupScope == "" {
		return ""
	}
	if group, exists := h.groupTree.Groups[h.groupScope]; exists {
		return group.Name
	}
	return h.groupScope
}
