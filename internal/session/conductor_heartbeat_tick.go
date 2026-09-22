package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Issue #2348: a heartbeat is a new user turn on the conductor's long-lived
// session, so every delivered tick pays a cache read of the WHOLE conversation
// (the reporter measured ~885k tokens per request, 221M tokens a day). The old
// OS heartbeat sent the same "check everything, re-read the rules file" prompt
// every tick, whether or not anything had changed, so the cost tracked uptime.
//
// BuildHeartbeatTick makes a tick delta-only: it returns an empty message (the
// caller sends nothing, so the conversation is never touched) unless something
// actionable CHANGED since the last delivered tick, and it asks for the rules
// file to be re-read only when that file changed.

// heartbeatTickStateFile is written next to meta.json in the conductor's home.
const heartbeatTickStateFile = "heartbeat-tick.json"

// HeartbeatSessionView is the slice of a child session a tick looks at.
type HeartbeatSessionView struct {
	Title  string
	Status Status
	Path   string
}

// HeartbeatTickInput is everything one tick observes. Nothing in it grows with
// the conductor's conversation, which is what bounds the tick's size.
type HeartbeatTickInput struct {
	Name         string
	Sessions     []HeartbeatSessionView // already scoped to the conductor's group
	InboxPending int                    // undrained records in the conductor's inbox
	InboxDigest  string                 // distinguishes replacement records at the same count
	InboxError   bool                   // unreadable inbox must not silently suppress a tick
	RemoteError  bool                   // poll health is logged, never an actionable fingerprint input
	RulesPath    string                 // resolved HEARTBEAT_RULES.md, "" if none
	RulesStamp   string                 // size+mtime of RulesPath, "" if none
}

// HeartbeatTickState is what the previous delivered tick left behind.
type HeartbeatTickState struct {
	Fingerprint  string `json:"fingerprint,omitempty"`
	RulesStamp   string `json:"rules_stamp,omitempty"`
	RemoteFailed bool   `json:"remote_failed,omitempty"`
}

// BuildHeartbeatTick returns the message to send ("" = skip this tick) and the
// state to persist. A tick is delivered only when there is something to act on
// (a waiting or errored session, or undrained inbox records) AND that set
// differs from the last delivered tick.
func BuildHeartbeatTick(in HeartbeatTickInput, prev HeartbeatTickState) (string, HeartbeatTickState) {
	var waiting, errored []string
	counts := map[Status]int{}
	for _, s := range in.Sessions {
		counts[s.Status]++
		entry := fmt.Sprintf("%s (project: %s)", s.Title, s.Path)
		switch s.Status {
		case StatusWaiting:
			waiting = append(waiting, entry)
		case StatusError:
			errored = append(errored, entry)
		}
	}
	sort.Strings(waiting)
	sort.Strings(errored)

	next := HeartbeatTickState{RulesStamp: prev.RulesStamp, RemoteFailed: prev.RemoteFailed}
	if len(waiting) == 0 && len(errored) == 0 && in.InboxPending == 0 && !in.InboxError {
		// Nothing to act on. Forget the fingerprint so the same set is
		// delivered again if it comes back after being resolved.
		return "", next
	}

	next.Fingerprint = fmt.Sprintf("w=%s|e=%s|inbox=%d:%s",
		strings.Join(waiting, ";"), strings.Join(errored, ";"), in.InboxPending, in.InboxDigest)
	if !in.InboxError && next.Fingerprint == prev.Fingerprint {
		return "", next
	}

	parts := []string{fmt.Sprintf(
		"%s [%s] Status: %d waiting, %d running, %d idle, %d error, %d stopped.",
		ConductorBridgeHeartbeatPrefix, in.Name,
		counts[StatusWaiting], counts[StatusRunning], counts[StatusIdle],
		counts[StatusError], counts[StatusStopped])}
	if len(waiting) > 0 {
		parts = append(parts, "Waiting sessions: "+strings.Join(waiting, ", ")+".")
	}
	if len(errored) > 0 {
		parts = append(parts, "Error sessions: "+strings.Join(errored, ", ")+".")
	}
	if in.InboxPending > 0 {
		parts = append(parts, fmt.Sprintf("Inbox: %d pending, run `agent-deck inbox drain self` first.", in.InboxPending))
	}
	if in.InboxError {
		parts = append(parts, "Inbox unreadable; inspect the conductor inbox before continuing.")
	}
	switch {
	case in.RulesPath != "" && in.RulesStamp != prev.RulesStamp:
		parts = append(parts, fmt.Sprintf("Read heartbeat rules from %s.", in.RulesPath))
		next.RulesStamp = in.RulesStamp
	case in.RulesPath != "":
		parts = append(parts, fmt.Sprintf("Heartbeat rules unchanged (%s); re-read only if not in context.", in.RulesPath))
	default:
		parts = append(parts, "Check if any need auto-response or user attention.")
	}
	return strings.Join(parts, " "), next
}

// HeartbeatRulesStamp identifies a rules file version without reading it.
func HeartbeatRulesStamp(path string) string {
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

func heartbeatTickStatePath(name string) (string, error) {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, heartbeatTickStateFile), nil
}

// LoadHeartbeatTickState returns the zero state when none was saved yet or the
// file is unreadable, so a broken state file costs one extra tick, never a
// silenced heartbeat.
func LoadHeartbeatTickState(name string) HeartbeatTickState {
	var st HeartbeatTickState
	path, err := heartbeatTickStatePath(name)
	if err != nil {
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

// SaveHeartbeatTickState persists the state for the next tick.
func SaveHeartbeatTickState(name string, st HeartbeatTickState) error {
	path, err := heartbeatTickStatePath(name)
	if err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// InboxSnapshot returns the pending count and content identity in one read.
func InboxSnapshot(sessionID string) (int, string, error) {
	data, err := os.ReadFile(InboxPathFor(sessionID))
	if os.IsNotExist(err) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	count := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	if count == 0 {
		return 0, "", nil
	}
	digest := sha256.Sum256(data)
	return count, fmt.Sprintf("%x", digest), nil
}
