package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// launchdServiceEnv is set by launchd on every process of a service and
// inherited by the service's children: it names the service an updater
// spawned by a com.agentdeck.* daemon runs inside.
const launchdServiceEnv = "XPC_SERVICE_NAME"

// PendingRebootstrapFileName is the cache-dir marker listing launch agents
// a run could not re-register because it ran inside them.
const PendingRebootstrapFileName = "launchd-rebootstrap-pending.json"

// Reasons a launch agent is pending.
const (
	// PendingReasonInsideService: the run that should have re-registered
	// the agent ran inside it and deferred it (nothing attempted).
	PendingReasonInsideService = "inside service"
	// PendingReasonBootstrapFailed: the agent was booted out but did not
	// come back (bootstrap never accepted, or not verified running); it is
	// unloaded until a run gets it back.
	PendingReasonBootstrapFailed = "bootstrap failed"
	// PendingReasonDisabled: the agent is on launchd's disabled list, so
	// no run re-registers it. Never written to the marker: `update --check`
	// reports an entry an older run left with it (MarkDisabledPending),
	// and the next run drops that entry.
	PendingReasonDisabled = "disabled"
)

// PendingAgent is one launch agent a run left for a later one: which
// service, why, since when, and how the retries went.
type PendingAgent struct {
	Label  string `json:"label"`
	Reason string `json:"reason,omitempty"`
	// Since is when the agent first became pending; a retry keeps it.
	Since time.Time `json:"since"`
	// Attempts counts the runs that tried to re-register it and failed.
	Attempts  int    `json:"attempts,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// Disabled is set by MarkDisabledPending when launchd has the agent on
	// its disabled list; never stored in the marker.
	Disabled bool `json:"disabled,omitempty"`
}

// pendingRebootstrap is the on-disk shape of the marker. Labels is what
// releases before the per-agent record read (and wrote): it always lists
// every agent in Agents, and an agent only in Labels (older writer) reads
// as pending since UpdatedAt with no reason.
type pendingRebootstrap struct {
	Labels    []string       `json:"labels"`
	UpdatedAt time.Time      `json:"updated_at"`
	Agents    []PendingAgent `json:"agents,omitempty"`
}

// agents merges Labels and Agents into one sorted list.
func (m pendingRebootstrap) agents() []PendingAgent {
	byLabel := map[string]PendingAgent{}
	for _, a := range m.Agents {
		byLabel[a.Label] = a
	}
	for _, l := range m.Labels {
		if _, ok := byLabel[l]; !ok {
			byLabel[l] = PendingAgent{Label: l, Since: m.UpdatedAt}
		}
	}
	out := make([]PendingAgent, 0, len(byLabel))
	for _, a := range byLabel {
		out = append(out, a)
	}
	sortPendingAgents(out)
	return out
}

func sortPendingAgents(agents []PendingAgent) {
	sort.Slice(agents, func(i, j int) bool { return agents[i].Label < agents[j].Label })
}

// pendingLabels is the label of every agent, in order.
func pendingLabels(agents []PendingAgent) []string {
	labels := make([]string, 0, len(agents))
	for _, a := range agents {
		labels = append(labels, a.Label)
	}
	return labels
}

// insideLaunchdService reports whether this process runs inside the launchd
// service label (service is XPC_SERVICE_NAME, "" outside launchd).
func insideLaunchdService(service, label string) bool {
	return service != "" && service == label
}

// readPendingRebootstrap reads the marker at path; an absent file is an
// empty marker.
func readPendingRebootstrap(path string) (pendingRebootstrap, error) {
	var m pendingRebootstrap
	if path == "" {
		return m, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// PendingRebootstrapAgents returns every agent the marker at path holds,
// sorted by label; none when the file does not exist.
func PendingRebootstrapAgents(path string) ([]PendingAgent, error) {
	m, err := readPendingRebootstrap(path)
	if err != nil {
		return nil, err
	}
	return m.agents(), nil
}

// PendingRebootstrap returns the labels the marker at path holds, sorted;
// none when the file does not exist.
func PendingRebootstrap(path string) ([]string, error) {
	agents, err := PendingRebootstrapAgents(path)
	if err != nil {
		return nil, err
	}
	return pendingLabels(agents), nil
}

// defaultPendingPath is the marker in the cache dir ("" when the cache dir
// cannot be resolved: then nothing is ever recorded or drained).
func defaultPendingPath() string {
	dir, err := getCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, PendingRebootstrapFileName)
}

// ListPendingRebootstrap returns the agents the default marker holds; an
// unreadable marker reads as none (the callers report, never fail, on it).
func ListPendingRebootstrap() []PendingAgent {
	agents, err := PendingRebootstrapAgents(defaultPendingPath())
	if err != nil {
		return nil
	}
	return agents
}

// HasPendingRebootstrap reports whether the default marker names any agent.
func HasPendingRebootstrap() bool {
	return len(ListPendingRebootstrap()) > 0
}

// DescribePendingAgent is the one-line human form of a pending agent.
func DescribePendingAgent(p PendingAgent) string {
	since := p.Since.Local().Format("15:04")
	switch p.Reason {
	case PendingReasonBootstrapFailed:
		times := fmt.Sprintf("%d times", p.Attempts)
		if p.Attempts == 1 {
			times = "once"
		}
		s := fmt.Sprintf("%s: bootstrap failed %s since %s", p.Label, times, since)
		if p.LastError != "" {
			s += " (last: " + p.LastError + ")"
		}
		return s + "; every update run retries it"
	case PendingReasonInsideService:
		return fmt.Sprintf("%s: deferred since %s, the updater ran inside it; the next update run outside it re-registers it", p.Label, since)
	case PendingReasonDisabled:
		return fmt.Sprintf("%s: disabled in launchd, left alone; the next update run drops it from this list", p.Label)
	}
	return fmt.Sprintf("%s: pending since %s", p.Label, since)
}

// addPendingRebootstrap records label as deferred because the run is
// inside it; an agent already pending keeps its record.
func addPendingRebootstrap(path, label string, now time.Time) error {
	return editPendingRebootstrap(path, label, func(p *PendingAgent, found bool) bool {
		if !found {
			*p = PendingAgent{Label: label, Reason: PendingReasonInsideService, Since: now}
		}
		return true
	})
}

// notePendingFailure records that a run booted label out and could not
// get it back: first failure stamps since, every one counts an attempt
// and keeps the last error.
func notePendingFailure(path, label string, cause error, now time.Time) error {
	return editPendingRebootstrap(path, label, func(p *PendingAgent, found bool) bool {
		if !found || p.Reason != PendingReasonBootstrapFailed {
			*p = PendingAgent{Label: label, Reason: PendingReasonBootstrapFailed, Since: now}
		}
		p.Attempts++
		p.LastError = firstLine(cause.Error())
		return true
	})
}

func removePendingRebootstrap(path, label string) error {
	return editPendingRebootstrap(path, label, func(*PendingAgent, bool) bool { return false })
}

// dropPendingRebootstrap removes label from the marker and reports whether
// it was there.
func dropPendingRebootstrap(path, label string) (dropped bool, err error) {
	err = editPendingRebootstrap(path, label, func(_ *PendingAgent, found bool) bool {
		dropped = found
		return false
	})
	return dropped, err
}

// editPendingRebootstrap rewrites the marker with label's record passed
// through edit (found says whether it was there; edit returns whether to
// keep it). An empty marker is removed.
func editPendingRebootstrap(path, label string, edit func(p *PendingAgent, found bool) (keep bool)) error {
	if path == "" {
		return errors.New("pending marker path unknown")
	}
	m, err := readPendingRebootstrap(path)
	if err != nil {
		return err
	}
	var agents []PendingAgent
	var rec PendingAgent
	found := false
	for _, a := range m.agents() {
		if a.Label == label {
			rec, found = a, true
			continue
		}
		agents = append(agents, a)
	}
	if edit(&rec, found) {
		agents = append(agents, rec)
	}
	if len(agents) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	sortPendingAgents(agents)
	data, err := json.MarshalIndent(pendingRebootstrap{Labels: pendingLabels(agents), UpdatedAt: time.Now(), Agents: agents}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o644)
}

// DrainPendingRebootstrap re-registers every agent the marker names whose
// service this process is not inside, clearing each one from the marker as
// it comes back. A label whose plist is gone is dropped from the marker and
// reported in Skipped; one launchd has disabled is dropped and reported in
// Disabled. Not darwin, or no marker: nothing to do.
func DrainPendingRebootstrap(opts RebootstrapOptions) (RebootstrapResult, error) {
	res := RebootstrapResult{Skipped: map[string]string{}}
	if err := opts.fill(); err != nil {
		return res, err
	}
	if opts.GOOS != "darwin" {
		return res, nil
	}
	labels, err := PendingRebootstrap(opts.PendingPath)
	if err != nil || len(labels) == 0 {
		return res, err
	}
	opts.Logger.Info("launchagent_pending_drain", slog.Any("labels", labels), slog.String("pending", opts.PendingPath))
	disabled := &launchdDisabled{opts: opts}
	for _, label := range labels {
		if insideLaunchdService(opts.ServiceLabel, label) {
			opts.Logger.Info("launchagent_pending_kept", slog.String("label", label), slog.String("reason", "this process runs inside it"))
			res.Deferred = append(res.Deferred, label)
			continue
		}
		agent, err := loadPendingAgent(opts.LaunchAgentsDir, label)
		if err != nil {
			res.Skipped[label] = err.Error()
			opts.Logger.Warn("launchagent_pending_dropped", slog.String("label", label), slog.String("err", err.Error()))
			_ = removePendingRebootstrap(opts.PendingPath, label)
			continue
		}
		if disabled.has(label) {
			leaveDisabled(opts, label, &res)
			continue
		}
		if err := rebootstrapOne(opts, agent); err != nil {
			fmt.Fprintf(opts.Out, "  ✗ %s: %v\n", agent.Label, err)
			return res, err
		}
		res.Restarted = append(res.Restarted, agent.Label)
		fmt.Fprintf(opts.Out, "  ↻ %s re-registered with launchd (was pending)\n", agent.Label)
		if err := removePendingRebootstrap(opts.PendingPath, label); err != nil {
			opts.Logger.Warn("launchagent_pending_clear_failed", slog.String("label", label), slog.String("err", err.Error()))
		}
	}
	return res, nil
}

// SettleRespawnedService drops the pending entry of the launchd service
// this process runs inside (opts.ServiceLabel) when that entry is a
// deferral (PendingReasonInsideService) recorded before startedAt, this
// process's start. Call it only from the service's own main process,
// never from a child that inherits XPC_SERVICE_NAME: launchd starting the
// service from the replaced file after the deferral is exactly what the
// deferred re-registration was for (it refuses an agent BTM no longer
// recognises), so nothing is left to do. Without this, a run inside the
// service keeps the entry forever and only a run outside it (the daily
// timer, a ticking TUI) clears it (2026-10-10: the notifier sat
// "pending" while the only TUI was attached all night). Returns whether
// an entry was dropped. Not darwin, outside launchd, or nothing pending:
// nothing to do.
func SettleRespawnedService(opts RebootstrapOptions, startedAt time.Time) (bool, error) {
	if err := opts.fill(); err != nil {
		return false, err
	}
	if opts.GOOS != "darwin" || opts.ServiceLabel == "" {
		return false, nil
	}
	agents, err := PendingRebootstrapAgents(opts.PendingPath)
	if err != nil {
		return false, err
	}
	for _, a := range agents {
		if a.Label != opts.ServiceLabel || a.Reason != PendingReasonInsideService || !a.Since.Before(startedAt) {
			continue
		}
		dropped, err := dropPendingRebootstrap(opts.PendingPath, a.Label)
		if err != nil {
			return false, err
		}
		if dropped {
			opts.Logger.Info("launchagent_pending_settled",
				slog.String("label", a.Label),
				slog.Time("deferred_at", a.Since),
				slog.Time("respawned_at", startedAt),
				slog.String("reason", "launchd started the service again on the updated binary"))
		}
		return dropped, nil
	}
	return false, nil
}

// loadPendingAgent reads and parses <dir>/<label>.plist.
func loadPendingAgent(dir, label string) (LaunchAgent, error) {
	path := filepath.Join(dir, label+".plist")
	data, err := os.ReadFile(path) // #nosec G304 -- label comes from our own marker file
	if err != nil {
		return LaunchAgent{}, fmt.Errorf("plist unreadable: %w", err)
	}
	agent, err := ParseLaunchAgentPlist(data)
	if err != nil {
		return LaunchAgent{}, fmt.Errorf("plist unparsable: %w", err)
	}
	agent.Path = path
	return agent, nil
}

// MarkDisabledPending flags every agent in agents that launchd has on its
// disabled list (Disabled, reason PendingReasonDisabled), so `update
// --check` says so instead of "bootstrap failed"; the next run drops such
// an entry. Nothing pending, or not darwin: agents unchanged and launchctl
// is not asked.
func MarkDisabledPending(agents []PendingAgent, opts RebootstrapOptions) []PendingAgent {
	if len(agents) == 0 {
		return agents
	}
	if err := opts.fill(); err != nil || opts.GOOS != "darwin" {
		return agents
	}
	disabled := readDisabledLabels(opts)
	for i := range agents {
		if disabled[agents[i].Label] {
			agents[i].Disabled = true
			agents[i].Reason = PendingReasonDisabled
		}
	}
	return agents
}
