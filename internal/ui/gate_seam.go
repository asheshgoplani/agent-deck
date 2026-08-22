package ui

// SIXGATE boot seam — the door a G1 driver walks the real TUI through.
//
// WHY THIS FILE EXISTS. SIXGATE's first gate after the script is DRIVE: something
// must execute the journey against the real artifact and capture what a human
// would see. For a Bubble Tea program that means booting the real *Home and
// reading the real View(), because View() *is* the string the terminal paints —
// and the blank percentage that created this framework lived in View().
//
// A driver cannot do that from outside package ui: every field Home needs is
// unexported, and NewHomeWithProfileAndMode is the wrong constructor for a gate
// because it opens storage, talks to tmux, starts a storage watcher and spawns
// background goroutines. On this machine a stray tmux invocation is not a slow
// test, it is the failure mode that twice destroyed the live session fleet.
//
// So the seam is here, in the package that owns the fields, and it is honest
// about its two boundaries:
//
//   - It constructs Home directly, exactly as seamBNewHome does in
//     tui_eval_seam_b_test.go, so no worker, ticker, socket or database is
//     created. Everything downstream of the constructor — Update, View, the
//     hotkey table, openContextInspector, the pager — is production code,
//     unmodified.
//   - Init() is a no-op, mirroring the same seamB pattern, so teatest cannot
//     start the workers the constructor declined to start.
//
// What this seam therefore does NOT prove: that the shipped binary boots, that
// a real terminal wraps these frames the same way, that the alt-screen is
// entered and released. Those need a real binary in a real pane, which is
// Driver B's job. Stating the limit here is part of the contract: a gate that
// silently overclaims its coverage is the thing SIXGATE exists to prevent.

import (
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// GateHomeOptions configures the headless Home a driver boots.
type GateHomeOptions struct {
	// Width and Height are the terminal geometry declared by the G0 script.
	Width, Height int
	// Instances populate the session list. They are used as given: a driver
	// materializes its own fixture and owns every path on them.
	Instances []*session.Instance
	// Cursor is the initially selected row in the flattened list.
	Cursor int
	// Profile is the label rendered in the chrome. It never selects storage:
	// this Home has none.
	Profile string
}

// GateHome is a tea.Model wrapping the real *Home with a no-op Init.
//
// It is the same wrapper the repository's own teatest seam uses; the difference
// is that this one is exported and lives in a non-test file, so a standalone
// gate binary can drive it. Update and View delegate to production code.
type GateHome struct {
	mu   sync.Mutex
	home *Home
}

// NewGateHome builds a headless Home for a SIXGATE driver.
//
// Every map and sub-model Home's View or Update dereferences unconditionally is
// initialised here. When a field is added to Home that View touches without a
// nil check, it must be added here too — the failure mode is a driver panicking
// on frame one, which is loud, so this stays a maintenance cost rather than a
// silent hole.
func NewGateHome(opts GateHomeOptions) *GateHome {
	if opts.Width <= 0 {
		opts.Width = 140
	}
	if opts.Height <= 0 {
		opts.Height = 50
	}

	h := &Home{
		profile:              opts.Profile,
		search:               NewSearch(),
		newDialog:            NewNewDialog(),
		groupDialog:          NewGroupDialog(),
		forkDialog:           NewForkDialog(),
		confirmDialog:        NewConfirmDialog(),
		helpOverlay:          NewHelpOverlay(),
		mcpDialog:            NewMCPDialog(),
		pluginDialog:         NewPluginDialog(),
		editPathsDialog:      NewEditPathsDialog(),
		editSessionDialog:    NewEditSessionDialog(),
		skillDialog:          NewSkillDialog(),
		setupWizard:          NewSetupWizard(),
		settingsPanel:        NewSettingsPanel(),
		analyticsPanel:       NewAnalyticsPanel(),
		geminiModelDialog:    NewGeminiModelDialog(),
		promptInputDialog:    NewPromptInputDialog(),
		sessionPickerDialog:  NewSessionPickerDialog(),
		codeBlockDialog:      NewCodeBlockDialog(),
		sessionSwitcher:      NewSessionSwitcher(),
		scrollbackPager:      NewScrollbackPager(),
		contextPager:         NewContextPager(),
		worktreeFinishDialog: NewWorktreeFinishDialog(),
		feedbackDialog:       NewFeedbackDialog(),
		zoxidePicker:         NewZoxidePicker(),
		globalSearch:         NewGlobalSearch(),
		watcherPanel:         NewWatcherPanel(),
		toolVisibilityPanel:  NewToolVisibilityPanel(),
		notesEditor:          newNotesEditor(),

		instances:    append([]*session.Instance(nil), opts.Instances...),
		instanceByID: make(map[string]*session.Instance, len(opts.Instances)),
		groupTree:    session.NewGroupTree(opts.Instances),
		flatItems:    []session.Item{},

		previewCache:              make(map[string]string),
		previewCacheTime:          make(map[string]time.Time),
		analyticsCache:            make(map[string]*session.SessionAnalytics),
		geminiAnalyticsCache:      make(map[string]*session.GeminiSessionAnalytics),
		analyticsCacheTime:        make(map[string]time.Time),
		contextReportCache:        make(map[string]contextReportEntry),
		clearOnCompactSent:        make(map[string]time.Time),
		launchingSessions:         make(map[string]time.Time),
		resumingSessions:          make(map[string]time.Time),
		mcpLoadingSessions:        make(map[string]time.Time),
		forkingSessions:           make(map[string]time.Time),
		setupRunningSessions:      make(map[string]time.Time),
		creatingSessions:          make(map[string]*CreatingSession),
		lastLogActivity:           make(map[string]time.Time),
		windowsCollapsed:          make(map[string]bool),
		worktreeDirtyCache:        make(map[string]bool),
		worktreeDirtyCacheTs:      make(map[string]time.Time),
		lastPersistedStatus:       make(map[string]string),
		lastPersistedAutoNameDesc: make(map[string]string),
		hotkeys:                   make(map[string]string),
		hotkeyLookup:              make(map[string]string),
		blockedHotkeys:            make(map[string]bool),
		boundKeys:                 make(map[string]string),
		pendingTitleChanges:       make(map[string]pendingTitle),
		remoteLatency:             make(map[string]session.RemoteLatency),

		width:  opts.Width,
		height: opts.Height,
		// The splash screen is shown until the first load message arrives. A
		// driver's fixture IS the loaded state, so the splash would be the only
		// frame it ever captured.
		initialLoading: false,
		cursor:         opts.Cursor,
		lastClickIndex: -1,
	}
	h.sessionRenderSnapshot.Store(make(map[string]sessionRenderState))
	h.liveSet = newPipeLiveSet(livePipeLRUCapacity)

	for _, inst := range opts.Instances {
		h.instanceByID[inst.ID] = inst
	}

	// The header's status counters read the render snapshot, which in the
	// running app is kept current by the background status worker this seam
	// deliberately does not start. Populating it here is not decoration: the
	// first drive of this seam recorded a header reading "no sessions" above a
	// list showing one, which is precisely the "feels broken on arrival"
	// signature the framework hunts — and it was the harness, not the product.
	// A driver that leaves its own world half-built manufactures the very
	// findings it exists to catch.
	//
	// The refresh reads tmux's pane-title cache only for instances that own a
	// tmux session; a fixture instance owns none, so no tmux call is made.
	h.refreshSessionRenderSnapshot(opts.Instances)

	// Hotkeys come from the shipped defaults rather than from config: the gate
	// asserts the journey a user gets out of the box, and reading config would
	// make the frames depend on the machine the driver ran on.
	h.setHotkeys(resolveHotkeys(nil))

	h.previewPct = session.DefaultPreviewPct
	h.previewOrientation = session.DefaultPreviewOrientation
	h.costLineTemplate, h.costLineHideWhenZero = session.ResolveCostLineTemplate(nil, opts.Profile)
	h.activeFilterExcludes = (session.DisplaySettings{}).GetActiveFilterExcludes()
	h.footerMode = (session.UISettings{}).GetFooter()

	h.rebuildFlatItems()
	if opts.Cursor >= 0 && opts.Cursor < len(h.flatItems) {
		h.cursor = opts.Cursor
	}

	return &GateHome{home: h}
}

// Init is deliberately a no-op: see the package comment. Returning a real
// command here would start the storage watcher, the status worker and the
// tmux pipe manager that NewGateHome exists to avoid.
func (g *GateHome) Init() tea.Cmd { return nil }

// Update delegates to the production Update.
func (g *GateHome) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, cmd := g.home.Update(msg)
	if h, ok := m.(*Home); ok {
		g.home = h
	}
	return g, cmd
}

// View delegates to the production View. This string is what a driver records
// as the frame a human would see.
func (g *GateHome) View() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.home.View()
}

// Snapshot renders the current frame without going through the tea runtime's
// output buffer.
//
// A driver needs the frame that corresponds to a specific step, and the
// runtime's cumulative output is a stream of partial redraws with no reliable
// per-step boundary. Calling View directly is both exact and honest: it is the
// same method the runtime calls, on the same model, after the same messages.
func (g *GateHome) Snapshot() string { return g.View() }

// ContextPagerVisible reports whether the context overlay currently owns the
// screen. It exists for a driver's run.json, never for an assertion: G2 reads
// the rendered frame and nothing else.
func (g *GateHome) ContextPagerVisible() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.home.contextPager.IsVisible()
}
