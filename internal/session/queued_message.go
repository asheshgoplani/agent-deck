package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Pending initial messages for sessions parked at a group's max_concurrent cap.
//
// `agent-deck launch --message-file task.md` into a group that is at its cap
// does not start the session — it records it as StatusQueued and returns. The
// prompt was resolved by then (resolveMessageInput has already read the file,
// or drained stdin, so the caller cannot re-supply it) and was simply dropped
// on the floor: nothing on the queued Instance could carry it. Whenever the
// session later started — `session start` after raising the cap, or the
// automatic drain in handleSessionStop — the agent came up on an empty
// composer, 0 tokens, reporting `waiting`, with the operator's only clue being
// a worker that appeared to have nothing to do. The prompt had to be re-sent by
// hand, and for a `--message-file` piped from stdin it was gone for good.
//
// The message is kept beside the other per-session runtime state rather than in
// a new instances column: it is transient by nature (consumed exactly once, at
// start) and this keeps the queue fix off the state-DB schema, which carries
// local-vs-upstream version divergence.

func queuedMessageDir() (string, error) {
	return runtimeDataPath("queued-message")
}

func queuedMessagePath(instanceID string) (string, error) {
	dir, err := queuedMessageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, safeRecordName(instanceID)+".txt"), nil
}

// SaveQueuedMessage records the initial prompt a queued session must receive
// when it starts. An empty message clears any stored one, so re-queuing without
// a prompt cannot resurrect an older one.
func SaveQueuedMessage(instanceID, message string) error {
	if strings.TrimSpace(instanceID) == "" {
		return errors.New("queued message: empty instance id")
	}
	path, err := queuedMessagePath(instanceID)
	if err != nil {
		return err
	}
	if message == "" {
		DiscardQueuedMessage(instanceID)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Per-write temp file + rename: two launches racing on the same id must not
	// leave a half-written prompt behind a fixed ".tmp" name.
	f, err := os.CreateTemp(filepath.Dir(path), safeRecordName(instanceID)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString(message); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// TakeQueuedMessage returns and CONSUMES the pending prompt for a session.
// Consuming is the point: the prompt must be delivered exactly once, so a
// second start (a restart, a re-drain after the first attempt already sent it)
// does not replay it into a session mid-conversation.
func TakeQueuedMessage(instanceID string) (string, bool) {
	path, err := queuedMessagePath(instanceID)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is derived from a sanitized instance id
	if err != nil || len(data) == 0 {
		// Remove an empty leftover so it cannot linger as a false pending.
		if err == nil {
			_ = os.Remove(path)
		}
		return "", false
	}
	_ = os.Remove(path)
	return string(data), true
}

// PeekQueuedMessage reports whether a session has a pending prompt without
// consuming it — for status/inspection paths that must not disturb delivery.
func PeekQueuedMessage(instanceID string) (string, bool) {
	path, err := queuedMessagePath(instanceID)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is derived from a sanitized instance id
	if err != nil || len(data) == 0 {
		return "", false
	}
	return string(data), true
}

// DiscardQueuedMessage drops any pending prompt for a session. Called when the
// session is removed, so a deleted session's prompt cannot be delivered to a
// later session that reuses the id.
func DiscardQueuedMessage(instanceID string) {
	path, err := queuedMessagePath(instanceID)
	if err != nil {
		return
	}
	_ = os.Remove(path)
}
