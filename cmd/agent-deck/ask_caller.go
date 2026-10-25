package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ask"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// askCallerInstance is the part of a session the caller check needs.
type askCallerInstance struct {
	ID          string
	TmuxSession string
	Socket      string
}

// askPaneRef names the tmux session (on a socket) that owns a pane pid.
type askPaneRef struct {
	Socket  string
	Session string
}

// verifyAskCaller checks that the caller asks as its own session (security
// review 1, session spoofing). $AGENTDECK_INSTANCE_ID must equal the
// resolved session, and a caller whose /proc parent chain reaches the pane
// of a known session can only ask as that session. It reports whether the
// chain proved the caller is the session (caller_verified); a caller outside
// every agent-deck pane is unverified, which every UI shows.
func verifyAskCaller(profile string, inst *session.InstanceData, envID string) (bool, error) {
	if envID != "" && envID != inst.ID {
		return false, &ask.Refusal{Msg: fmt.Sprintf("--session names another session than this one ($AGENTDECK_INSTANCE_ID %s)", envID)}
	}
	ancestors := procAncestors(os.Getpid())
	if len(ancestors) == 0 {
		return false, nil
	}
	loaded, err := loadOpenSessions(profile)
	if err != nil {
		return false, nil
	}
	instances := make([]askCallerInstance, 0, len(loaded))
	for _, in := range loaded {
		if in.TmuxSession != "" {
			instances = append(instances, askCallerInstance{ID: in.ID, TmuxSession: in.TmuxSession, Socket: in.TmuxSocketName})
		}
	}
	caller := askCallerSession(ancestors, askPanePIDs(instances), instances)
	if caller != "" && caller != inst.ID {
		return false, &ask.Refusal{Msg: fmt.Sprintf("this process runs in session %s; --session names another session", caller)}
	}
	return caller != "" && caller == inst.ID, nil
}

// askPanePIDs lists every pane pid on the sockets the sessions use.
func askPanePIDs(instances []askCallerInstance) map[int]askPaneRef {
	panes := map[int]askPaneRef{}
	seen := map[string]bool{}
	for _, in := range instances {
		if seen[in.Socket] {
			continue
		}
		seen[in.Socket] = true
		out, err := tmux.OutputBounded(in.Socket, "list-panes", "-a", "-F", "#{pane_pid} #{session_name}")
		if err != nil && len(out) == 0 {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			pidStr, name, ok := strings.Cut(strings.TrimSpace(line), " ")
			if pid, err := strconv.Atoi(pidStr); ok && err == nil && pid > 0 {
				panes[pid] = askPaneRef{Socket: in.Socket, Session: name}
			}
		}
	}
	return panes
}

// askCallerSession returns the session whose pane is the nearest pane in
// the caller's ancestor chain, or "" when that pane is no agent-deck
// session's or there is none.
func askCallerSession(ancestors []int, panes map[int]askPaneRef, instances []askCallerInstance) string {
	for _, pid := range ancestors {
		ref, ok := panes[pid]
		if !ok {
			continue
		}
		for _, in := range instances {
			if in.TmuxSession == ref.Session && in.Socket == ref.Socket {
				return in.ID
			}
		}
		return ""
	}
	return ""
}

// procAncestors returns pid and its ancestors from /proc (Linux), nearest
// first; nil where /proc is missing.
func procAncestors(pid int) []int {
	var chain []int
	for i := 0; i < 64 && pid > 0; i++ {
		chain = append(chain, pid)
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			if i == 0 {
				return nil
			}
			break
		}
		// The comm field may hold spaces and parens: the ppid follows the last ")".
		end := bytes.LastIndexByte(b, ')')
		if end < 0 {
			break
		}
		fields := strings.Fields(string(b[end+1:]))
		if len(fields) < 2 {
			break
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid <= 1 {
			break
		}
		pid = ppid
	}
	return chain
}
