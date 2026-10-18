package session

import "github.com/asheshgoplani/agent-deck/internal/tmux"

var listStatusSessionNames = tmux.ListSessionNamesOnSocket

// CLIStatusCandidates avoids probing each historical session separately. A
// stopped or error row absent from a complete socket listing retains its stored
// status. Failed inventory retains stopped history but refreshes error rows,
// which may still have a live pane. A cached error row still gets the
// terminated-pane classification updateStatus would give it (exit receipt,
// #2091 hook evidence), computed without a tmux probe.
func CLIStatusCandidates(instances []*Instance) ([]*Instance, map[*Instance]bool) {
	refresh := make([]*Instance, 0, len(instances))
	cached := make(map[*Instance]bool)
	bySocket := make(map[string]map[string]struct{})
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if inst.Status != StatusStopped && inst.Status != StatusError {
			refresh = append(refresh, inst)
			continue
		}
		sess := inst.GetTmuxSession()
		if sess == nil {
			inst.classifyAbsentError(nil)
			cached[inst] = true
			continue
		}
		names, ok := bySocket[sess.SocketName]
		if !ok {
			var err error
			names, err = listStatusSessionNames(sess.SocketName)
			if err != nil {
				names = nil
			}
			bySocket[sess.SocketName] = names
		}
		if names == nil && inst.Status == StatusError {
			refresh = append(refresh, inst)
			continue
		}
		if _, exists := names[sess.Name]; !exists {
			inst.classifyAbsentError(sess)
			cached[inst] = true
			continue
		}
		refresh = append(refresh, inst)
	}
	return refresh, cached
}

// classifyAbsentError gives an error row whose tmux session is gone the same
// terminated-pane verdict updateStatus's absent branch gives it: the tracked
// command's exit receipt (#2597: substate process-exited plus exit_code) and
// the #2091 hook-evidence correction. An absent session holds no dead pane,
// so no tmux query is needed. A stopped row is left as stored.
func (i *Instance) classifyAbsentError(sess *tmux.Session) {
	if i.Status != StatusError {
		return
	}
	if sess != nil && sess.AbsenceIsForeignServer() {
		// Alive on the default server; keep the stored status (updateStatus
		// gives no verdict here either).
		return
	}
	if HookStatusTool(i.Tool) {
		ReloadHookStatus(i)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.Status == StatusStopped {
		return
	}
	if i.neverStarted() {
		i.Status = StatusIdle
		return
	}
	exitCode, haveExitCode := 0, false
	if path := i.trackedCommandExitReceiptPath(); path != "" {
		exitCode, haveExitCode = readCommandExitReceipt(path)
	}
	status, substate := classifyTerminatedPane(exitCode, haveExitCode, i.Tool, i.hookStatus)
	status, substate = i.classifyCustomCommandExit(status, substate, exitCode, haveExitCode)
	i.Status = status
	i.terminatedPaneSubstate = substate
	i.setTerminatedPaneExitCode(exitCode, haveExitCode)
	i.refreshAuthHoldOnDeathLocked()
}
