package ui

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// deliveryQueue returns the queue that delivers watcher health alerts to
// conductor panes, creating it on first use.
func (h *Home) deliveryQueue() *conductorQueue {
	h.conductorDeliveriesOnce.Do(func() {
		h.conductorDeliveries = newConductorQueue(h.sendToConductor)
	})
	return h.conductorDeliveries
}

// conductorHeartbeatDeliver types the /clear and the heartbeat into a
// conductor pane. Tests replace it.
var conductorHeartbeatDeliver = func(p *tmux.Session, body string) error { return deliverToConductorPane(p, body) }

// agentDeliveryOverBudget reports whether an enforced cost budget
// ([costs.budgets] enforce = true) refuses an agent-initiated delivery the
// TUI types into inst's pane itself (the heartbeat after a clear-on-compact,
// a watcher health alert), with the reason. Group limits sum the sessions of
// instances in inst's exact group.
func (h *Home) agentDeliveryOverBudget(inst *session.Instance, instances []*session.Instance) (string, bool) {
	if inst == nil || !h.costBudget.Enforced() {
		return "", false
	}
	ids := []string{inst.ID}
	for _, other := range instances {
		if other != nil && other.ID != inst.ID && other.GroupPath == inst.GroupPath {
			ids = append(ids, other.ID)
		}
	}
	r, err := h.costBudget.CheckSend(inst.ID, inst.GroupPath, ids)
	if err != nil || r.Action != costs.BudgetActionStop {
		return "", false
	}
	return r.Describe(), true
}

// budgetSessions returns the sessions the budget gate looks at: the loaded
// ones, or for a headless engine owner (which loads none) the ones in
// storage. Nil without an enforced budget, so nothing is read then.
func (h *Home) budgetSessions() []*session.Instance {
	if !h.costBudget.Enforced() {
		return nil
	}
	h.instancesMu.RLock()
	loaded := append([]*session.Instance(nil), h.instances...)
	h.instancesMu.RUnlock()
	if len(loaded) > 0 || !h.headless || h.storage == nil {
		return loaded
	}
	instances, _, err := h.storage.LoadWithGroups()
	if err != nil {
		return nil
	}
	return instances
}

// sessionByID finds id in instances, or nil.
func sessionByID(instances []*session.Instance, id string) *session.Instance {
	for _, inst := range instances {
		if inst != nil && inst.ID == id {
			return inst
		}
	}
	return nil
}

// conductorSendLockWait bounds how long a health alert waits for another
// send to the conductor to finish (session.SendTargetLockWait; a var so tests
// can shorten it).
var conductorSendLockWait = session.SendTargetLockWait

// stopConductorDeliveries starts no further health alert, waits up to wait
// for the one in flight (so a quit does not leave a pasted message without
// its Enter) and drops the rest: the next engine owner restates each
// watcher's state. The queue is created here if no alert made it yet: a relay
// callback still running past the engine's stop (a health alert reading the
// database, say) must find it stopped, not create it and start delivering
// after the owner lock is gone (#2530).
func (h *Home) stopConductorDeliveries(wait time.Duration) {
	h.deliveryQueue().stop(wait)
}

// conductorPane returns the session id and tmux session of the named
// conductor, or a nil session when that conductor is not loaded or has no
// tmux session. Alerts look it up off the UI goroutine while Update may
// rewrite h.instances, so the lookup holds the read lock for the whole scan.
// A headless Home reads the sessions from storage instead (#2530).
func (h *Home) conductorPane(conductorName string) (string, *tmux.Session) {
	if h.headless {
		return h.conductorPaneFromStorage(conductorName)
	}
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	return findConductorPane(h.instances, conductorName)
}

// conductorPaneFromStorage looks a conductor up for a headless
// (`web --no-tui`) engine owner, which runs no Bubble Tea loop to load
// h.instances (#2530). It reads the sessions from storage on every delivery
// and leaves h.instances alone: replacing them here
// (HydrateInstancesFromStorage) could swap the registry under a web mutation
// in progress (WebMutator.beginHeadlessTx), and a conductor restarted since
// the last delivery is found under its current tmux session.
func (h *Home) conductorPaneFromStorage(conductorName string) (string, *tmux.Session) {
	if h.storage == nil {
		return "", nil
	}
	instances, _, err := h.storage.LoadWithGroups()
	if err != nil {
		uiLog.Warn("watcher_delivery_conductor_lookup_failed",
			slog.String("conductor_session", session.ConductorSessionTitle(conductorName)),
			slog.String("error", err.Error()))
		return "", nil
	}
	return findConductorPane(instances, conductorName)
}

// findConductorPane returns the id and tmux session of the non-archived
// conductor named conductorName, or a nil session when there is none or it
// has no tmux session. The stored conductor name wins over the title, so a
// decorated or renamed conductor still receives its deliveries. An archived
// session keeps the conductor's identity but is not the conductor any more,
// and it may come before the live one.
func findConductorPane(instances []*session.Instance, conductorName string) (string, *tmux.Session) {
	for _, inst := range instances {
		// Title and ArchivedAt are written under the instance's own lock
		// (SetTitleThreadSafe, SetArchivedAtThreadSafe), not instancesMu:
		// renames, title sync, archive and restart run off the UI goroutine.
		if !inst.IsConductorNamed(conductorName, inst.GetTitleThreadSafe()) || inst.IsArchived() {
			continue
		}
		if ts := inst.GetTmuxSession(); ts != nil && ts.TmuxName() != "" {
			return inst.ID, ts
		}
		return "", nil
	}
	return "", nil
}

// deliverToConductorPane sends msg into a conductor's tmux pane and verifies it
// was actually submitted, then returns. A raw single
// `send-keys <msg> Enter` races the inner agent's input handler: tmux 3.2+ wraps the
// literal text in bracketed-paste markers (\e[200~…\e[201~) and the trailing
// Enter lands in the same PTY buffer as the paste-end marker, so async TUIs
// (Ink/Node.js, curses, ratatui) can swallow it — the message is typed into the composer but never sent. That
// is exactly how routed messages get silently dropped once this native
// send-keys path is the sole delivery route (no external bridge).
//
// Two layers, agent-agnostic where possible:
//   - SendKeysAndEnter separates the literal text from a delayed Enter, which
//     fixes the paste/Enter race for any bracketed-paste TUI.
//   - A verify loop confirms submission. The tool-agnostic signal is the
//     session status flipping to "active" (the status detector handles both
//     claude and codex), proving the agent accepted the input and began work.
//     For an introspectable composer (claude's prompt marker) it additionally
//     re-presses Enter while the message is still shown unsent and treats a
//     cleared composer as submitted; agents without composer introspection get
//     a small bounded number of fallback Enters in case the delayed Enter was
//     dropped.
//
// Best-effort: returns an error only when no submission signal is seen within
// the budget, so the caller can log the drop instead of failing silently.
// Intended to run inside a goroutine.
//
// Issue #1409: delivery is composer-guarded — a pane whose composer holds a
// half-typed operator draft is held briefly, then refused without modifying it.
func deliverToConductorPane(p guardableConductorPane, msg string) error {
	return deliverToConductorPaneGuarded(p, msg, conductorComposerGuardOptions(), 40, 250*time.Millisecond)
}

// clearConductorAndHeartbeat rechecks the composer independently for each
// delivery. A failed clear must never be followed by a heartbeat.
func clearConductorAndHeartbeat(deliver func(string) error, heartbeat string, settle time.Duration) error {
	if err := deliver("/clear"); err != nil {
		return err
	}
	if settle > 0 {
		time.Sleep(settle)
	}
	return deliver(heartbeat)
}

// conductorComposerGuardOptions are the production bounds of the watcher/
// health-alert composer guard. Dispatch runs in a goroutine, so a generous
// 5s hold costs nothing on the UI thread; the guard is a single pane capture
// when the composer is empty.
func conductorComposerGuardOptions() send.ComposerGuardOptions {
	return send.ComposerGuardOptions{
		HoldWait:     5 * time.Second,
		PollInterval: 250 * time.Millisecond,
		ClearWait:    time.Second,
		Strip:        tmux.StripANSI,
	}
}

// deliverToConductorPaneGuarded waits for operator input to clear naturally.
// If it remains or cannot be read, refuse before sending any input.
func deliverToConductorPaneGuarded(p guardableConductorPane, msg string, guardOpts send.ComposerGuardOptions, maxChecks int, checkDelay time.Duration) error {
	guard := send.GuardComposerDraft(p, guardOpts)
	if guard.Refused {
		return fmt.Errorf("message not sent: composer is occupied or unreadable; existing draft preserved")
	}
	// guard.ComposerPasteMarkerFree is the #1777 provenance the verify loop
	// needs: the guard's pre-send capture saw a composer with no
	// "[Pasted text …]" marker, so a marker seen during verification is the
	// collapsed rendering of OUR framed multi-line payload (issue #1855).
	return deliverToConductorPaneAttributed(p, msg, guard.ComposerPasteMarkerFree, maxChecks, checkDelay)
}

// conductorPane is the slice of *tmux.Session that reliable delivery needs.
// Declaring it as an interface keeps the verify-retry loop unit-testable with a
// fake pane (mirrors the sendRetryTarget interface used by the CLI send path).
type conductorPane interface {
	SendKeysAndEnter(string) error
	SendEnter() error
	CapturePaneFresh() (string, error)
	GetStatus() (string, error)
}

// guardableConductorPane extends conductorPane with the surfaces the #1409
// composer guard needs. *tmux.Session satisfies it.
type guardableConductorPane interface {
	conductorPane
}

// blindEnterCap bounds the fallback Enter presses for agents whose composer is
// not introspectable, so a message that was actually delivered (and the agent
// has since gone idle) is not spammed with empty submissions.
const blindEnterCap = 3

// deliverToConductorPaneTuned is deliverToConductorPaneAttributed without
// pre-send provenance: a paste marker in the composer counts as foreign —
// never nudged, never read as submitted. Kept for callers with no guard
// capture to attribute against.
func deliverToConductorPaneTuned(p conductorPane, msg string, maxChecks int, checkDelay time.Duration) error {
	return deliverToConductorPaneAttributed(p, msg, false, maxChecks, checkDelay)
}

// deliverToConductorPaneAttributed is deliverToConductorPane with the verify
// budget exposed for tests and the #1777 paste-marker provenance explicit;
// production callers use the default budget (~10s). ownPasteMarker carries
// the caller's pre-send observation that the composer held no "[Pasted text …]"
// marker, which is what makes a marker seen during verification attributable
// to this delivery's own bracketed-paste collapse (issue #1855) rather than
// to a foreign paste parked in the composer.
func deliverToConductorPaneAttributed(p conductorPane, msg string, ownPasteMarker bool, maxChecks int, checkDelay time.Duration) error {
	if err := p.SendKeysAndEnter(msg); err != nil {
		return err
	}
	attrib := send.EnterAttribution{Message: msg, OwnPasteMarker: ownPasteMarker}
	sawUnsent := false
	blindEnters := 0
	for i := 0; i < maxChecks; i++ {
		if checkDelay > 0 {
			time.Sleep(checkDelay)
		}

		// Tool-agnostic success: the status detector recognizes claude and codex
		// "active", so a transition to active proves the Enter was accepted and
		// the agent began processing the message.
		if status, err := p.GetStatus(); err == nil && status == "active" {
			return nil
		}

		raw, err := p.CapturePaneFresh()
		if err != nil {
			continue
		}
		content := tmux.StripANSI(raw)

		switch {
		case send.ComposerHoldsUnsentMessage(raw, msg, tmux.StripANSI):
			// Introspectable composer still holds the message: the trailing
			// Enter was swallowed, so re-press it. Surface a tmux rejection
			// immediately rather than letting it masquerade as a timeout. A
			// prompt suggestion that echoes msg is not our stuck message: it
			// falls through to the "composer rendered" arm, never an Enter.
			sawUnsent = true
			if err := p.SendEnter(); err != nil {
				return fmt.Errorf("retry enter: %w", err)
			}
		case send.ComposerHoldsPasteMarker(raw, tmux.StripANSI):
			// The composer holds a "[Pasted text …]" marker instead of the
			// message body. Since the transport frames every multi-line
			// payload as a bracketed paste (issue #1855), this is the NORMAL
			// delivered-but-unsubmitted shape of our own send — but only
			// provenance can say so, because the marker hides the content.
			// NudgeEnter presses Enter only when the collapse is attributable
			// (ownPasteMarker, or a pane with no composer introspection);
			// otherwise it withholds, this case confirms nothing, and the
			// loop times out honestly instead of the old behavior of falling
			// through to "a composer is rendered without our message" and
			// reporting an unsent message as submitted.
			if attrib.NudgeEnter(p, send.Captured(raw), tmux.StripANSI) {
				sawUnsent = true
			}
		case sawUnsent || send.HasCurrentComposerPrompt(content):
			// The composer previously held the message and is now clear, or a
			// composer is rendered without our message: submitted.
			return nil
		case blindEnters < blindEnterCap:
			// No composer introspection (e.g. codex/cursor) and not yet active.
			// Re-press Enter a bounded number of times in case the delayed Enter
			// was dropped, then defer to the status signal above. A visible
			// composer holding foreign content never reaches this arm — the
			// HasCurrentComposerPrompt case above returns first — so this blind
			// Enter cannot submit text nobody authored (#1777 audit).
			blindEnters++
			if err := p.SendEnter(); err != nil {
				return fmt.Errorf("retry enter: %w", err)
			}
		}
	}
	return fmt.Errorf("watcher dispatch not confirmed submitted (status never active, composer still pending) after %s", time.Duration(maxChecks)*checkDelay)
}
