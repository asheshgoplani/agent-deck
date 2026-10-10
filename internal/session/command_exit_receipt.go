package session

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A tracked shell command can finish before tmux publishes pane_dead_status.
// Its parent shell waits for the command and writes this small receipt before
// it exits. The path is stable across CLI processes and cleared before spawn.
func commandExitReceiptPath(id string) string {
	dir, err := runtimeDataPath("command-exit")
	if err != nil {
		dir = tempAgentDeckPath("runtime", "command-exit")
	}
	return filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(id)))+".status")
}

func (i *Instance) wrapTrackedCommandExit(command string) (string, error) {
	if !i.tracksCommandExit() {
		return command, nil
	}
	path := commandExitReceiptPath(i.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("prepare command exit receipt: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("clear command exit receipt: %w", err)
	}
	return "bash -c " + shellescape.Quote(command) + "; code=$?; " +
		"(umask 077; printf '%s\\n' \"$code\" > " + shellescape.Quote(path) + "); exit \"$code\"", nil
}

// trackedCommandExitReceiptPath is the receipt path for a tracked command,
// or "" when the session does not track its command's exit.
func (i *Instance) trackedCommandExitReceiptPath() string {
	if !i.tracksCommandExit() {
		return ""
	}
	return commandExitReceiptPath(i.ID)
}

// trackedCommandExitObserved reports whether a tracked command's pane holds
// an observed exit: tmux kept a dead pane with its status, or the wrapper
// wrote its receipt. receiptPath "" means the session is not tracked.
func trackedCommandExitObserved(sess *tmux.Session, receiptPath string) bool {
	if receiptPath == "" || sess == nil || !sess.Exists() || !sess.IsPaneDead() {
		return false
	}
	if _, ok := sess.PaneDeadExitStatus(); ok {
		return true
	}
	_, ok := readCommandExitReceipt(receiptPath)
	return ok
}

func (i *Instance) trackedCommandExitReceipt() (int, bool) {
	if !i.tracksCommandExit() {
		return 0, false
	}
	return readCommandExitReceipt(commandExitReceiptPath(i.ID))
}

func readCommandExitReceipt(path string) (int, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(content)))
	return code, err == nil && code >= 0 && code <= 255
}
