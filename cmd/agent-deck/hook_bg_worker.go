package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2683: Claude Code's agent-view supervisor and its pre-spawned
// bg-spare workers keep the environment of the pane that first started them,
// AGENTDECK_INSTANCE_ID included. A conversation later backgrounded from a
// DIFFERENT pane runs in that worker, so its hooks name the wrong instance.
// Such hooks cannot be attributed reliably and are dropped: the status file,
// the session-id binding and every other side effect belong to the pane's
// own Claude process only.

// maxBgWorkerAncestorHops bounds the walk from the hook process up to the
// Claude process that fired it (the hook may run under an intermediate shell).
const maxBgWorkerAncestorHops = 3

// hookFromClaudeBackgroundWorker reports whether this hook was fired by a
// Claude background/supervisor worker.
//
// The nearest ancestor with a ~/.claude/sessions/<pid>.json record that names
// a kind is authoritative: kind "bg" drops the hook, any other kind (the
// pane's own foreground Claude) credits it, even when CLAUDE_JOB_DIR is set.
// CLAUDE_JOB_DIR alone can leak into a pane through its tmux server's
// environment, and treating it as proof would drop every hook on that server.
// It is only consulted when no such record is found (e.g. /proc is missing,
// or the worker wrote none).
func hookFromClaudeBackgroundWorker() bool {
	if kind, ok := nearestClaudeAncestorKind(os.Getppid()); ok {
		return kind == "bg"
	}
	return strings.TrimSpace(os.Getenv("CLAUDE_JOB_DIR")) != ""
}

// nearestClaudeAncestorKind walks up from pid (at most
// maxBgWorkerAncestorHops processes) and returns the kind recorded in the first
// ~/.claude/sessions/<pid>.json that carries one.
func nearestClaudeAncestorKind(pid int) (string, bool) {
	claudeDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		claudeDir = filepath.Join(home, ".claude")
	}
	for hop := 0; hop < maxBgWorkerAncestorHops && pid > 1; hop++ {
		data, err := os.ReadFile(filepath.Join(claudeDir, "sessions", strconv.Itoa(pid)+".json"))
		if err == nil {
			var rec struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(data, &rec) == nil && strings.TrimSpace(rec.Kind) != "" {
				return strings.TrimSpace(rec.Kind), true
			}
			return "", false
		}
		pid = parentPIDOf(pid)
	}
	return "", false
}

// scrubClaudeJobDirForCommand removes CLAUDE_JOB_DIR from this process's
// environment before any tmux command can run (#2683), so a tmux server that
// agent-deck starts from inside a Claude background job never carries it into
// its panes. The hook handler keeps it: it is one of its bg-worker signals,
// and every tmux exec it makes goes through the filtered tmux spawn env.
func scrubClaudeJobDirForCommand(args []string) {
	if len(args) > 0 && args[0] == "hook-handler" {
		return
	}
	_ = os.Unsetenv("CLAUDE_JOB_DIR")
}

// parentPIDOf returns pid's parent via /proc, or 0 where /proc is absent
// (macOS: only the direct parent is checked there).
func parentPIDOf(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// Format: pid (comm) state ppid ...; comm may contain spaces/parens.
	s := string(data)
	idx := strings.LastIndexByte(s, ')')
	if idx < 0 {
		return 0
	}
	fields := strings.Fields(s[idx+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// logBgWorkerHookDropOnce records one session-id lifecycle reject per
// (instance, worker session), so a busy worker does not flood the log.
func logBgWorkerHookDropOnce(instanceID, sessionID, event string) {
	hooksDir := getHooksDir()
	markerPath := filepath.Join(hooksDir, filepath.Base(instanceID)+".bg-worker-dropped")
	if existing, err := os.ReadFile(markerPath); err == nil && strings.TrimSpace(string(existing)) == sessionID {
		return
	}
	_ = session.WriteSessionIDLifecycleEvent(session.SessionIDLifecycleEvent{
		InstanceID: instanceID,
		Tool:       "claude",
		Action:     "reject",
		Source:     "hook_payload",
		Candidate:  sessionID,
		Reason:     "hook_from_background_worker",
		HookEvent:  event,
	})
	if err := os.MkdirAll(hooksDir, 0o700); err == nil {
		_ = os.WriteFile(markerPath, []byte(sessionID), 0o600)
	}
}
