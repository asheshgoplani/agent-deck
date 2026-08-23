package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	ctxregistry "github.com/asheshgoplani/agent-deck/internal/ctxinspect/registry"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/sessionhost"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// contextInspectTimeout bounds one inspection. Everything on the path is a
// bounded head-read of a local file, so this is a backstop against a
// pathological or networked filesystem rather than an expected wait.
const contextInspectTimeout = 20 * time.Second

// contextReportCacheTTL mirrors analyticsCacheTTL: long enough that re-opening
// the overlay is instant, short enough that a session which just restarted is
// not described by a stale report.
const contextReportCacheTTL = analyticsCacheTTL

// contextReportEntry is one cached inspection result.
type contextReportEntry struct {
	report   *ctxinspect.Report
	warnings []string
	err      error
	at       time.Time
}

// contextReportMsg carries an inspection result back to the pager. It is
// stale-guarded by sessionID so a result that completes after the user closed
// the overlay, or re-opened it on a different session, is never shown.
type contextReportMsg struct {
	sessionID string
	report    *ctxinspect.Report
	warnings  []string
	err       error
}

// contextLeverCopiedMsg reports the outcome of copying an item's lever.
type contextLeverCopiedMsg struct {
	payload string
	err     error
}

// statusLine renders the message for the pager's footer.
func (m contextLeverCopiedMsg) statusLine() string {
	if m.err != nil {
		return "could not copy the lever: " + m.err.Error()
	}
	return "copied to clipboard: " + m.payload
}

// openContextInspector opens the context overlay for a session and returns the
// command that inspects it.
//
// The pager opens immediately in a loading state; the inspection runs in a
// tea.Cmd because it reads files, and View must stay pure. A cached result
// inside the TTL is installed synchronously, so re-opening the overlay costs
// nothing.
func (h *Home) openContextInspector(inst *session.Instance) tea.Cmd {
	if inst == nil || h.contextPager == nil {
		return nil
	}
	sessionID := inst.ID
	h.contextPager.Show(strings.TrimSpace(inst.Title), sessionID, inst.GetToolThreadSafe(), h.width, h.height)

	if entry, ok := h.cachedContextReport(sessionID); ok {
		h.applyContextEntry(entry)
		return nil
	}
	return h.inspectContextCmd(inst)
}

// refreshContextInspector re-runs the inspection for the session the overlay is
// already bound to, bypassing the cache. It is what the `r` key does.
func (h *Home) refreshContextInspector() tea.Cmd {
	if h.contextPager == nil || !h.contextPager.IsVisible() {
		return nil
	}
	sessionID := h.contextPager.SessionID()
	if sessionID == "" {
		return nil
	}
	h.instancesMu.RLock()
	inst := h.instanceByID[sessionID]
	h.instancesMu.RUnlock()
	if inst == nil {
		h.contextPager.SetError("this session no longer exists")
		return nil
	}
	h.invalidateContextReport(sessionID)
	h.contextPager.SetLoading()
	return h.inspectContextCmd(inst)
}

// inspectContextCmd builds the async inspection command for one session.
//
// The peer list is snapshotted here, on the UI goroutine, because the Claude
// adapter's transcript resolution is collision-checked against it: two live
// sessions sharing one harness session id must not both resolve a transcript,
// or the overlay silently shows another session's context (#1349/#1400).
func (h *Home) inspectContextCmd(inst *session.Instance) tea.Cmd {
	sessionID := inst.ID
	ref := strings.TrimSpace(inst.Title)
	if ref == "" {
		ref = sessionID
	}
	peers := h.snapshotInstances()

	return func() tea.Msg {
		req, warnings, err := sessionhost.BuildRequest(inst, peers, sessionhost.RequestOptions{SessionRef: ref})
		if err != nil {
			return contextReportMsg{sessionID: sessionID, err: err}
		}

		ctx, cancel := context.WithTimeout(context.Background(), contextInspectTimeout)
		defer cancel()

		rep, err := ctxregistry.Default().Inspect(ctx, req)
		if err != nil {
			if errors.Is(err, ctxinspect.ErrUnsupported) {
				// No adapter claims this tool, not even the fallback. Say so
				// plainly; agent-deck has nothing true to report, and inventing
				// a screen would be worse than admitting that.
				return contextReportMsg{sessionID: sessionID, err: fmt.Errorf(
					"context inspection is unsupported for %q: no adapter claims this tool, and agent-deck will not guess", req.Tool)}
			}
			// A parse failure or an unreadable transcript is a real failure and
			// is reported as one. It must never degrade into a screen of zeroes.
			return contextReportMsg{sessionID: sessionID, err: err}
		}
		return contextReportMsg{sessionID: sessionID, report: rep, warnings: warnings}
	}
}

// snapshotInstances copies the instance list under the read lock so a tea.Cmd
// can use it without holding the UI's mutex across disk I/O.
func (h *Home) snapshotInstances() []*session.Instance {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	out := make([]*session.Instance, len(h.instances))
	copy(out, h.instances)
	return out
}

// cachedContextReport returns a cached result while it is inside the TTL.
func (h *Home) cachedContextReport(sessionID string) (contextReportEntry, bool) {
	if sessionID == "" {
		return contextReportEntry{}, false
	}
	h.contextReportMu.RLock()
	entry, ok := h.contextReportCache[sessionID]
	h.contextReportMu.RUnlock()
	if !ok || time.Since(entry.at) >= contextReportCacheTTL {
		return contextReportEntry{}, false
	}
	return entry, true
}

// storeContextReport caches an inspection result and drops expired entries, so
// the cache cannot grow without bound as sessions come and go.
func (h *Home) storeContextReport(msg contextReportMsg) {
	if msg.sessionID == "" {
		return
	}
	now := time.Now()
	h.contextReportMu.Lock()
	defer h.contextReportMu.Unlock()
	if h.contextReportCache == nil {
		h.contextReportCache = make(map[string]contextReportEntry)
	}
	for id, entry := range h.contextReportCache {
		if now.Sub(entry.at) >= contextReportCacheTTL {
			delete(h.contextReportCache, id)
		}
	}
	h.contextReportCache[msg.sessionID] = contextReportEntry{
		report:   msg.report,
		warnings: msg.warnings,
		err:      msg.err,
		at:       now,
	}
}

// invalidateContextReport drops one session's cached result.
func (h *Home) invalidateContextReport(sessionID string) {
	if sessionID == "" {
		return
	}
	h.contextReportMu.Lock()
	delete(h.contextReportCache, sessionID)
	h.contextReportMu.Unlock()
}

// applyContextEntry installs a cached result into the pager.
func (h *Home) applyContextEntry(entry contextReportEntry) {
	if entry.err != nil {
		h.contextPager.SetError(entry.err.Error())
		return
	}
	h.contextPager.SetReport(entry.report, entry.warnings)
}

// handleContextPagerKey handles key events while the context inspector is
// visible.
//
// Navigation is level- and tab-aware: Enter/→ descends (category → items →
// verbatim text), Esc/← ascends and closes at a tab's root, and Tab or 1/2/3
// switch tabs while keeping each tab's drill position.
func (h *Home) handleContextPagerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := h.contextPager
	// Any keypress clears the transient footer line, which is why the status
	// needs no timer and View can stay pure.
	p.ClearStatus()

	switch msg.String() {
	case "up", "k":
		p.MoveCursor(-1)
	case "down", "j":
		p.MoveCursor(1)
	case "pgup", "b":
		p.PageUp()
	case "pgdown", " ", "f":
		p.PageDown()
	case "home", "g":
		p.Top()
	case "end", "G":
		p.Bottom()
	case "tab":
		p.NextTab()
	case "shift+tab":
		p.PrevTab()
	case "1":
		p.SetTab(contextPagerTabOverview)
	case "2":
		p.SetTab(contextPagerTabBreakdown)
	case "3":
		p.SetTab(contextPagerTabVerify)
	case "enter", "right", "l":
		if !p.Descend() {
			p.SetStatus("nothing to open here")
		}
	case "esc", "left", "h":
		if !p.Ascend() {
			p.Hide()
		}
	case "y":
		return h, h.copyContextLever()
	case "r":
		return h, h.refreshContextInspector()
	case "q", "ctrl+q":
		p.Hide()
	}
	return h, nil
}

// copyContextLever copies the selected item's lever — the file to edit, the
// directory to delete, or the exact command to run — to the clipboard.
//
// The overlay is read-only in v1: it hands the user the action, it never
// performs it. Mutating a live session's context configuration from a viewer is
// exactly the class of action that deserves a deliberate, separate decision.
func (h *Home) copyContextLever() tea.Cmd {
	p := h.contextPager
	lever, ok := p.SelectedLever()
	if !ok {
		if it, has := p.SelectedItem(); has && !it.Lever.Kind.Actionable() {
			p.SetStatus("no lever: this item is part of the harness and cannot be changed")
		} else {
			p.SetStatus("no lever on this row — open an item to copy its path or command")
		}
		return nil
	}
	payload := lever.Payload()
	return func() tea.Msg {
		termInfo := tmux.GetTerminalInfo()
		if _, err := clipboard.Copy(payload, termInfo.SupportsOSC52); err != nil {
			return contextLeverCopiedMsg{payload: payload, err: err}
		}
		return contextLeverCopiedMsg{payload: payload}
	}
}
