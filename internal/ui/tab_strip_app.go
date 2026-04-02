package ui

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// tabStripAnimTickMsg advances animation frames.
type tabStripAnimTickMsg struct{}

// tabStripRefreshMsg triggers data reload from SQLite.
type tabStripRefreshMsg struct{}

// TabStripApp is a lightweight Bubble Tea program that renders
// a standalone tab strip, designed to run in a tmux split pane.
type TabStripApp struct {
	tabStrip  *TabStripModel
	db        *statedb.StateDB
	currentID string
	tabFile   string // ~/.agent-deck/tab_current
	width     int
	height    int
	err       error
}

// NewTabStripApp creates a new standalone tab strip application.
func NewTabStripApp(dbPath, currentID string) (*TabStripApp, error) {
	// If the given path has no tables, try profiles/default/state.db
	db, err := statedb.Open(dbPath)
	if err != nil {
		return nil, err
	}
	// Check if this DB has the instances table
	rows, checkErr := db.LoadInstances()
	if checkErr != nil || len(rows) == 0 {
		_ = db.Close()
		// Try profile path
		profileDB := filepath.Join(filepath.Dir(dbPath), "profiles", "default", "state.db")
		db, err = statedb.Open(profileDB)
		if err != nil {
			return nil, err
		}
	}

	homeDir, _ := os.UserHomeDir()
	tabFile := filepath.Join(homeDir, ".agent-deck", "tab_current")

	ts := NewTabStrip("horizontal", 0, false)

	app := &TabStripApp{
		tabStrip:  ts,
		db:        db,
		currentID: currentID,
		tabFile:   tabFile,
		height:    24,
	}

	return app, nil
}

// Init implements tea.Model.
func (a *TabStripApp) Init() tea.Cmd {
	// Load initial data and start ticks
	return tea.Batch(
		a.loadInstances,
		a.animTick(),
		a.refreshTick(),
	)
}

// Update implements tea.Model.
func (a *TabStripApp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return a, tea.Quit
		}

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height

	case tabStripAnimTickMsg:
		a.tabStrip.Tick()
		return a, a.animTick()

	case tabStripRefreshMsg:
		return a, tea.Batch(
			a.loadInstances,
			a.refreshTick(),
		)

	case instancesLoadedMsg:
		a.tabStrip.UpdateInstances(msg.instances)
		// Update selection based on currentID
		a.syncSelection()
	}

	return a, nil
}

// View implements tea.Model.
func (a *TabStripApp) View() string {
	if a.err != nil {
		return "error: " + a.err.Error()
	}
	// Horizontal layout uses width, vertical uses height
	if a.tabStrip.layout == TabStripHorizontal {
		return a.tabStrip.View(a.width)
	}
	return a.tabStrip.View(a.height)
}

// Close releases resources.
func (a *TabStripApp) Close() {
	if a.db != nil {
		_ = a.db.Close()
	}
}

// --- internal messages and commands ---

type instancesLoadedMsg struct {
	instances []*session.Instance
}

func (a *TabStripApp) loadInstances() tea.Msg {
	rows, err := a.db.LoadInstances()
	if err != nil {
		return instancesLoadedMsg{}
	}

	// Also check tab_current file for selection changes
	if data, err := os.ReadFile(a.tabFile); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			a.currentID = id
		}
	}

	instances := make([]*session.Instance, 0, len(rows))
	for _, r := range rows {
		inst := &session.Instance{
			ID:          r.ID,
			Title:       r.Title,
			ProjectPath: r.ProjectPath,
			Status:      session.Status(r.Status),
			Tool:        r.Tool,
			Order:       r.Order,
			GroupPath:   r.GroupPath,
			CreatedAt:   r.CreatedAt,
		}
		instances = append(instances, inst)
	}

	return instancesLoadedMsg{instances: instances}
}

func (a *TabStripApp) syncSelection() {
	if a.currentID == "" {
		return
	}
	for i, inst := range a.tabStrip.instances {
		if inst.ID == a.currentID {
			a.tabStrip.SelectTab(i)
			return
		}
	}
}

func (a *TabStripApp) animTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return tabStripAnimTickMsg{}
	})
}

func (a *TabStripApp) refreshTick() tea.Cmd {
	return tea.Tick(1*time.Second, func(time.Time) tea.Msg {
		return tabStripRefreshMsg{}
	})
}
