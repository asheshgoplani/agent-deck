package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"al.essio.dev/pkg/shellescape"
)

const terminatedCompletionMaxAge = 30 * time.Second

func completionHookTool(tool string) bool {
	return IsClaudeCompatible(tool) || IsCodexCompatible(tool)
}

// Publish before a start/resume can signal the old process. A remote hook
// directory is not local authority, so SSH sessions retain the old fallback.
func (i *Instance) seedCompletionLaunch() error {
	if !completionHookTool(i.Tool) || i.IsSSH() {
		return nil
	}
	// Veto in-flight probes before durable publication can block on a hook writer.
	i.spawnGen.Add(1)
	if _, err := i.seedHookGeneration("starting", false); err != nil {
		return fmt.Errorf("seed completion launch: %w", err)
	}
	return nil
}

func (i *Instance) bindCompletionLaunchCommand(command string) string {
	i.mu.RLock()
	generation := i.hookLaunchGeneration
	i.mu.RUnlock()
	if completionHookTool(i.Tool) && generation != "" && !i.IsSSH() {
		return "export AGENTDECK_HOOK_GENERATION=" + shellescape.Quote(generation) + "; " + command
	}
	return command
}

func (i *Instance) completionSessionIDLocked() string {
	if IsClaudeCompatible(i.Tool) {
		return i.ClaudeSessionID
	}
	if IsCodexCompatible(i.Tool) {
		return i.CodexSessionID
	}
	return i.GenericSessionID
}

// Ordinary Stop/waiting is never completion proof. Both launch and
// conversation identities are mandatory, including after observer reload.
func validTerminatedCompletion(hook *HookStatus, tool, generation, sessionID string, started, now time.Time) bool {
	if hook == nil || !hook.TimestampKnown || generation == "" || sessionID == "" || started.IsZero() ||
		hook.HookGeneration != generation || hook.SessionID != sessionID || hook.Sequence == 0 ||
		hook.DoneStatus != "ok" || hook.Status != "waiting" {
		return false
	}
	// A newly invoked Stop must not refresh a prior launch's transcript sentinel.
	if IsClaudeCompatible(tool) && (hook.DoneAt.IsZero() || hook.DoneAt.Before(started) || hook.DoneAt.After(now)) {
		return false
	}
	if IsCodexCompatible(tool) && (hook.codexCompletionConsumed || hook.CodexStartedGeneration == "" ||
		hook.CodexStartedGeneration != hook.CodexCompletedGeneration || hook.CodexStartedSessionID != sessionID || hook.CodexCompletedSessionID != sessionID) {
		return false
	}
	return !hook.UpdatedAt.Before(started) && !hook.UpdatedAt.After(now) && now.Sub(hook.UpdatedAt) <= terminatedCompletionMaxAge
}

// Called with i.mu held: acquisition must never wait for a producer. The
// root-then-scoped order matches launch seeding; spawn ownership excludes a
// concurrent scope migration. Reject symlinked directories and lock files.
func tryCompletionWriterLocks(instanceID string) (*hermesHookLocks, bool) {
	root := GetHooksDir()
	dirs := []string{root}
	for _, dir := range []string{root, filepath.Join(root, "sandbox"), filepath.Join(root, "sandbox", instanceID)} {
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) && dir != root {
			break
		}
		if err != nil || !info.IsDir() {
			return nil, false
		}
		if dir == filepath.Join(root, "sandbox", instanceID) {
			dirs = append(dirs, dir)
		}
	}
	locks := &hermesHookLocks{}
	for _, dir := range dirs {
		f, err := os.OpenFile(filepath.Join(dir, instanceID+".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			locks.release()
			return nil, false
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = f.Close()
			locks.release()
			return nil, false
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			locks.release()
			return nil, false
		}
		locks.files = append(locks.files, f)
	}
	return locks, true
}

// All possible writer scopes must be locked. A single authoritative control
// must match the selected status scope and sequence exactly; a newer control
// with an old DONE record is a torn publication, not completion evidence.
func readSequencedCompletionLocked(instanceID string) *HookStatus {
	root := GetHooksDir()
	authorityPath, generation := "", ""
	var sequence uint64
	for _, dir := range []string{root, filepath.Join(root, "sandbox", instanceID)} {
		path := filepath.Join(dir, instanceID+".generation.json")
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || authorityPath != "" {
			return nil
		}
		data, err := readStatusFileNoFollow(path)
		var control struct {
			Generation   string  `json:"generation"`
			NextSequence *uint64 `json:"next_sequence"`
		}
		if err != nil || json.Unmarshal(data, &control) != nil || control.Generation == "" || control.NextSequence == nil || *control.NextSequence == 0 {
			return nil
		}
		authorityPath = filepath.Join(dir, instanceID+".json")
		generation, sequence = control.Generation, *control.NextSequence
	}
	if authorityPath == "" || hookStatusFilePath(instanceID) != authorityPath {
		return nil
	}
	for _, dir := range []string{root, filepath.Join(root, "sandbox", instanceID)} {
		path := filepath.Join(dir, instanceID+".json")
		if path == authorityPath {
			continue
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return nil
		}
	}
	hook := readHookStatusFile(instanceID)
	if hook == nil || hook.HookGeneration != generation || hook.Sequence != sequence {
		return nil
	}
	return hook
}
