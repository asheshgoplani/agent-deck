package ui

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// healthAlertEventLookback is how many of a watcher's newest stored events
// are searched for the conductor they were routed to.
const healthAlertEventLookback = 50

// watcherHealthMemo remembers, per watcher, the last health status the relay
// saw and the conductor that was told about the current warning or error, so
// a watcher that stays down is reported once rather than on every health tick
// (#2531). The engine reports every adapter's state on every tick.
type watcherHealthMemo struct {
	mu   sync.Mutex
	seen map[string]watcherHealthSeen
}

type watcherHealthSeen struct {
	status watcher.HealthStatus
	// alertedTo is the conductor that got an alert for this status, or ""
	// when none could be named yet.
	alertedTo string
}

func (m *watcherHealthMemo) get(name string) watcherHealthSeen {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[name]
}

func (m *watcherHealthMemo) set(name string, s watcherHealthSeen) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = make(map[string]watcherHealthSeen)
	}
	m.seen[name] = s
}

// isTriageRoute reports whether routedTo names the triage path rather than a
// conductor ("triage", or a "triage-..." marker such as triage-req-dropped).
func isTriageRoute(routedTo string) bool {
	return routedTo == "triage" || strings.HasPrefix(routedTo, "triage-")
}

// dispatchHealthAlert tells a watcher's conductor when the watcher moves into
// warning or error (D-22, D-23, TUI-03), and once more when it is healthy
// again. A watcher that stays in the same state sends nothing further, and a
// change between warning and error is a new alert. If no conductor can be
// named, the alert is retried on later reports, so it goes out once routing
// names one. Called by relayWatcherEngine, off the Bubble Tea loop (#2524).
func (h *Home) dispatchHealthAlert(state watcher.HealthState) {
	prev := h.watcherHealth.get(state.WatcherName)

	switch state.Status {
	case watcher.HealthStatusWarning, watcher.HealthStatusError:
		if prev.status == state.Status && prev.alertedTo != "" {
			return // its conductor already knows
		}
		conductorName, reason := healthAlertConductor(state.WatcherName)
		h.watcherHealth.set(state.WatcherName, watcherHealthSeen{status: state.Status, alertedTo: conductorName})
		if conductorName == "" {
			if prev.status != state.Status {
				uiLog.Warn("watcher_health_alert_no_conductor",
					slog.String("watcher", state.WatcherName),
					slog.String("status", string(state.Status)),
					slog.String("reason", reason))
			}
			return
		}
		// Build alert message (D-23): include name, status, reason, and suggested action.
		h.deliveryQueue().enqueue(conductorDelivery{
			Conductor: conductorName,
			Text: fmt.Sprintf("[WATCHER HEALTH ALERT] Watcher %q transitioned to %s: %s. Suggested action: check watcher configuration and source connectivity.",
				state.WatcherName, state.Status, state.Message),
			QueuedAt: time.Now(),
			Alert:    state.WatcherName,
		})

	case watcher.HealthStatusHealthy:
		h.watcherHealth.set(state.WatcherName, watcherHealthSeen{status: state.Status})
		if prev.alertedTo == "" {
			return // nobody was told it was down
		}
		// The recovery notice goes to whoever got the alert. As an Alert for
		// the same watcher it replaces that alert if it is still queued.
		h.deliveryQueue().enqueue(conductorDelivery{
			Conductor: prev.alertedTo,
			Text: fmt.Sprintf("[WATCHER HEALTH RECOVERED] Watcher %q is healthy again (was %s).",
				state.WatcherName, prev.status),
			QueuedAt: time.Now(),
			Alert:    state.WatcherName,
		})
	}
}

// healthAlertConductor names the conductor that gets a watcher's health
// alerts, using the routing its events already follow: the conductor set on
// the watcher row if any, else the conductor its newest routed event went to,
// else the conductor clients.json routes to when it names exactly one. When
// none can be named it returns "" and why.
func healthAlertConductor(watcherName string) (conductorName, reason string) {
	db := statedb.GetGlobal()
	if db == nil {
		return "", "no state database"
	}
	row, err := db.LoadWatcherByName(watcherName)
	if err != nil || row == nil {
		return "", "watcher not found"
	}
	if row.Conductor != "" {
		return row.Conductor, ""
	}

	events, err := db.LoadWatcherEvents(row.ID, healthAlertEventLookback)
	if err == nil {
		for _, ev := range events { // newest first
			if ev.RoutedTo != "" && !isTriageRoute(ev.RoutedTo) {
				return ev.RoutedTo, ""
			}
		}
	}

	dir, err := session.WatcherDir()
	if err != nil {
		return "", "watcher directory unavailable: " + err.Error()
	}
	clients, err := watcher.LoadClientsJSON(filepath.Join(dir, "clients.json"))
	if err != nil {
		return "", "no routed events yet and no clients.json"
	}
	set := map[string]bool{}
	for _, c := range clients {
		if c.Conductor != "" {
			set[c.Conductor] = true
		}
	}
	switch len(set) {
	case 0:
		return "", "no routed events yet and clients.json names no conductor"
	case 1:
		for c := range set {
			return c, ""
		}
	}
	names := make([]string, 0, len(set))
	for c := range set {
		names = append(names, c)
	}
	sort.Strings(names)
	return "", "no routed events yet and clients.json routes to several conductors (" + strings.Join(names, ", ") + ")"
}
