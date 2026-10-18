package ui

import (
	"errors"
	"hash/fnv"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// Live triggers data for the panel and the ⚡ row badge (#2702).
//
// Every trigger write (a fire, a scan, a mute) bumps one metadata row,
// triggers_rev, in the profile's state DB. A background tick reads only that
// row and reloads the tables when it moved, so a fire shows up within a
// tick with no manual refresh and an idle deck costs one tiny query per
// tick. Targets are also re-resolved when the local session list changes (a
// rename can turn a target dead without any trigger write), and at least
// every triggersMaxAge so the 24 h counts and ages do not freeze.
//
// All reads happen inside tea.Cmds; Update only swaps in the result and
// render reads the cached copy.

const (
	triggersPollVisible = 2 * time.Second
	triggersPollHidden  = 30 * time.Second
	triggersMaxAge      = 5 * time.Minute
)

// triggersPollInterval is the delay before the next rev check: quick while
// the panel is open, slow when only the row badges need it.
func triggersPollInterval(panelVisible bool) time.Duration {
	if panelVisible {
		return triggersPollVisible
	}
	return triggersPollHidden
}

// triggersFeed is where the poll reads from. The default reads the
// profile's state DB; tests swap in a counting fake.
type triggersFeed interface {
	// Rev is the cheap change stamp ("" before the first trigger write).
	Rev() (string, error)
	// Load builds the views, resolving targets against sessions (this
	// profile's, snapshotted by the caller) and every other profile.
	Load(profile string, sessions []triggers.Resolved) ([]triggers.View, error)
}

var errTriggersNoDB = errors.New("state database not open")

type stateDBTriggersFeed struct{}

func (stateDBTriggersFeed) Rev() (string, error) {
	db := statedb.GetGlobal()
	if db == nil {
		return "", errTriggersNoDB
	}
	return db.TriggersRev()
}

func (stateDBTriggersFeed) Load(profile string, sessions []triggers.Resolved) ([]triggers.View, error) {
	db := statedb.GetGlobal()
	if db == nil {
		return nil, errTriggersNoDB
	}
	return triggers.Load(db, session.TriggerResolverForSessions(profile, sessions), triggers.Hostname(), time.Now())
}

type triggersTickMsg struct{ gen int }

type triggersPolledMsg struct {
	gen    int
	rev    string
	key    string
	views  []triggers.View
	loaded bool
	err    error
}

// triggerSessionsSnapshot copies the local sessions' ids and titles (on the
// Update goroutine, so the background resolve never touches an Instance)
// and fingerprints them.
func (h *Home) triggerSessionsSnapshot() ([]triggers.Resolved, string) {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	out := make([]triggers.Resolved, 0, len(h.instances))
	sum := fnv.New64a()
	for _, inst := range h.instances {
		if inst == nil {
			continue
		}
		out = append(out, triggers.Resolved{SessionID: inst.ID, Title: inst.Title})
		_, _ = sum.Write([]byte(inst.ID))
		_, _ = sum.Write([]byte{0})
		_, _ = sum.Write([]byte(inst.Title))
		_, _ = sum.Write([]byte{0})
	}
	return out, string(sum.Sum(nil))
}

// triggersPollCmd checks the rev now and reloads when it, or the session
// list, changed since the last load (or the last load is too old).
func (h *Home) triggersPollCmd() tea.Cmd {
	feed := h.triggersFeed
	if feed == nil {
		return nil
	}
	sessions, key := h.triggerSessionsSnapshot()
	gen, lastRev, profile := h.triggersPollGen, h.triggersRev, h.profile
	force := !h.triggersLoaded || key != h.triggersKey || time.Since(h.triggersLoadedAt) > triggersMaxAge
	return func() tea.Msg {
		rev, err := feed.Rev()
		if err != nil {
			return triggersPolledMsg{gen: gen, err: err}
		}
		if !force && rev == lastRev {
			return triggersPolledMsg{gen: gen, rev: rev, key: key}
		}
		views, err := feed.Load(profile, sessions)
		if err != nil {
			return triggersPolledMsg{gen: gen, err: err}
		}
		return triggersPolledMsg{gen: gen, rev: rev, key: key, views: views, loaded: true}
	}
}

func (h *Home) triggersTick() tea.Cmd {
	gen := h.triggersPollGen
	return tea.Tick(triggersPollInterval(h.triggersPanel.IsVisible()), func(time.Time) tea.Msg {
		return triggersTickMsg{gen: gen}
	})
}

// restartTriggersPoll starts a fresh poll loop now (opening the panel must
// not wait out a 30 s badge tick); the old loop's pending tick is dropped by
// its generation.
func (h *Home) restartTriggersPoll() tea.Cmd {
	if h.triggersFeed == nil {
		return nil
	}
	h.triggersPollGen++
	return h.triggersPollCmd()
}

// handleTriggersMsg is Update's arm for the poll loop messages.
func (h *Home) handleTriggersMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case triggersTickMsg:
		if msg.gen != h.triggersPollGen {
			return nil
		}
		return h.triggersPollCmd()
	case triggersPolledMsg:
		if msg.loaded {
			h.triggersRev, h.triggersKey = msg.rev, msg.key
			h.triggersLoadedAt = time.Now()
			h.applyTriggerViews(msg.views)
		} else if msg.err != nil && h.triggersPanel != nil {
			h.triggersPanel.SetViews(nil, msg.err)
		}
		if msg.gen != h.triggersPollGen {
			return nil
		}
		return h.triggersTick()
	}
	return nil
}

// applyTriggerViews installs a loaded set: the panel's rows and the badge
// map the session rows read.
func (h *Home) applyTriggerViews(views []triggers.View) {
	h.triggerViews = views
	h.triggerBadges = triggers.Badges(views)
	h.triggersLoaded = true
	if h.triggersPanel != nil {
		h.triggersPanel.SetViews(views, nil)
	}
}

// openTriggersPanel is the hotkey: on a local session row that something
// wakes (it shows a ⚡ badge) it opens that session's "what wakes me" view,
// otherwise the full list.
func (h *Home) openTriggersPanel() tea.Cmd {
	if h.triggersPanel == nil {
		h.triggersPanel = NewTriggersPanel()
		h.triggersPanel.SetHost(triggers.Hostname())
	}
	p := h.triggersPanel
	p.SetSize(h.width, h.height)
	if h.triggersLoaded {
		p.SetViews(h.triggerViews, nil)
	}
	if sel := h.getSelectedSession(); sel != nil && h.triggerBadges[sel.ID].Count > 0 {
		p.ShowReverse(sel.ID, sel.Title)
	} else {
		p.Show()
	}
	return h.restartTriggersPoll()
}

// triggersBadgeGlyph is the row badge glyph (U+26A1, two cells wide in
// every width table the deck measures with).
const triggersBadgeGlyph = "⚡"

// fitTriggersBadge renders the ⚡N row badge within budget cells (negative
// means unbounded), following the viewers badge rules (#2201): hidden at 0,
// shortened to the glyph alone when the count does not fit, then dropped so
// the title keeps its floor. Red when one of the session's triggers is
// failing, dim otherwise.
func fitTriggersBadge(b triggers.Badge, selected bool, budget int) (string, int) {
	if b.Count <= 0 {
		return "", 0
	}
	badge := " " + triggersBadgeGlyph + strconv.Itoa(b.Count)
	if budget >= 0 && cellWidth(badge) > budget {
		badge = " " + triggersBadgeGlyph
		if cellWidth(badge) > budget {
			return "", 0
		}
	}
	style := DimStyle
	switch {
	case b.Bad:
		style = lipgloss.NewStyle().Foreground(ColorRed)
		if selected {
			style = style.Background(ColorAccent)
		}
	case selected:
		style = SessionStatusSelStyle
	}
	return style.Render(badge), cellWidth(badge)
}
