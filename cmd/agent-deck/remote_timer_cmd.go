package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// Each remote's own update timer from the controller (#2472): `remote list
// --check` shows it, `remote update --install-timer` installs or migrates it
// over SSH.

// remoteProber is what `remote list --check` asks each remote.
type remoteProber interface {
	CheckBinary(ctx context.Context) (version string, found bool)
	FetchTimerStatus(ctx context.Context) update.TimerStatus
}

// Seams so tests substitute stubs for SSH; nil means session.NewSSHRunner.
var (
	remoteProbeRunner func(name string, rc session.RemoteConfig) remoteProber
	remoteTimerRunner func(name string, rc session.RemoteConfig) session.RemoteTimerManager
)

func newRemoteProber(name string, rc session.RemoteConfig) remoteProber {
	if remoteProbeRunner != nil {
		return remoteProbeRunner(name, rc)
	}
	return session.NewSSHRunner(name, rc)
}

func newRemoteTimerManager(name string, rc session.RemoteConfig) session.RemoteTimerManager {
	if remoteTimerRunner != nil {
		return remoteTimerRunner(name, rc)
	}
	return session.NewSSHRunner(name, rc)
}

// probeRemoteTimer reads one remote's timer, after its version probe said
// whether there is an agent-deck to ask at all.
func probeRemoteTimer(ctx context.Context, p remoteProber, found bool) update.TimerStatus {
	if !found {
		return session.UnknownTimerStatus("agent-deck not found on remote or host unreachable")
	}
	return p.FetchTimerStatus(ctx)
}

// remoteTimerColumn is the timer cell of the `remote list` text table.
func remoteTimerColumn(st *update.TimerStatus) string {
	switch {
	case st == nil:
		return "timer -"
	case st.Kind == update.TimerKindUnknown:
		return "timer unknown"
	case !st.Installed:
		return "timer none (nudge only)"
	case st.Active:
		return "timer active (" + st.Kind + ")"
	}
	return "timer inactive (" + st.Kind + ")"
}

// remoteTimerInstallResult is one remote's line of `remote update
// --install-timer`.
type remoteTimerInstallResult struct {
	Name     string             `json:"name"`
	Host     string             `json:"host"`
	OK       bool               `json:"ok"`
	Action   string             `json:"action,omitempty"`
	Migrated string             `json:"migrated,omitempty"`
	Summary  string             `json:"summary,omitempty"`
	Error    string             `json:"error,omitempty"`
	Timer    update.TimerStatus `json:"timer"`
}

// runRemoteTimerInstall runs `update --install-timer` on every remote in
// name order, then reads each one's timer back and caches it. A remote
// whose binary is too old to answer in JSON still gets its install; its
// timer reads back as unknown.
func runRemoteTimerInstall(ctx context.Context, remotes map[string]session.RemoteConfig) []remoteTimerInstallResult {
	results := make([]remoteTimerInstallResult, 0, len(remotes))
	timers := make(map[string]update.TimerStatus, len(remotes))
	for _, name := range remoteNames(remotes) {
		rc := remotes[name]
		mgr := newRemoteTimerManager(name, rc)
		row := remoteTimerInstallResult{Name: name, Host: rc.Host}
		inst, err := mgr.InstallUpdateTimer(ctx, false)
		row.Action, row.Migrated, row.Summary = inst.Action, inst.Migrated, inst.Summary()
		if err != nil {
			row.Error = err.Error()
			row.Summary = ""
		} else {
			row.OK = true
		}
		row.Timer = mgr.FetchTimerStatus(ctx)
		timers[name] = row.Timer
		results = append(results, row)
	}
	_ = session.RecordRemoteTimers(timers, time.Now())
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
}

// printRemoteTimerInstall is the text form; returns the failure count.
func printRemoteTimerInstall(w io.Writer, results []remoteTimerInstallResult, asJSON bool) int {
	failed := 0
	for _, r := range results {
		if !r.OK {
			failed++
		}
	}
	if asJSON {
		_ = writeIndentedJSON(w, results)
		return failed
	}
	for _, r := range results {
		if r.OK {
			fmt.Fprintf(w, "%s: %s; now %s\n", r.Name, r.Summary, remoteTimerColumn(&r.Timer))
		} else {
			fmt.Fprintf(w, "%s: ✗ timer install failed: %s\n", r.Name, r.Error)
		}
	}
	noun := "remotes"
	if len(results) == 1 {
		noun = "remote"
	}
	fmt.Fprintf(w, "%d %s: %d timer install(s) failed\n", len(results), noun, failed)
	return failed
}
