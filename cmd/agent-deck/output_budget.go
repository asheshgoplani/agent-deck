package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

const (
	defaultOutputMaxTokens = 25_000
	outputBytesPerToken    = 4
)

const outputOmissionMarker = "\n\n… output omitted …\n\n"

type outputReadEvent struct {
	SessionID string `json:"session_id"`
	Profile   string `json:"profile"`
	Source    string `json:"source"`
	Truncated bool   `json:"truncated"`
	MaxTokens int    `json:"max_tokens"`
	Timestamp int64  `json:"ts"`
}

// prepareAgentBoundaryOutput strips terminal control sequences and applies a
// conservative UTF-8 byte budget. Keeping both ends preserves the command or
// question that started a long response and the final result/error that ended
// it. The footer is part of the budget unless the requested budget is too
// small to preserve the complete recovery location.
func prepareAgentBoundaryOutput(raw string, maxTokens int, fullPath string) (string, bool) {
	clean := tmux.StripANSI(raw)
	budget := math.MaxInt
	if maxTokens <= math.MaxInt/outputBytesPerToken {
		budget = maxTokens * outputBytesPerToken
	}
	if len(clean) <= budget {
		return clean, false
	}

	footer := fmt.Sprintf("\n\n… output truncated to %d tokens; full output at %s\n", maxTokens, fullPath)
	minimumBudget := len(footer)
	if budget < minimumBudget {
		budget = minimumBudget
	}
	contentBudget := budget - len(footer) - len(outputOmissionMarker)
	if contentBudget <= 0 {
		return footer, true
	}

	headBytes := (contentBudget + 1) / 2
	tailBytes := contentBudget - headBytes
	head := truncateUTF8(clean, headBytes)
	tail := truncateUTF8FromEnd(clean, tailBytes)
	return head + outputOmissionMarker + tail + footer, true
}

// shouldBoundAgentOutput identifies the human-readable agent boundary. JSON,
// quiet, and clipboard modes are compatibility or internal transport surfaces
// and must retain the complete source data.
func shouldBoundAgentOutput(jsonOutput, quietMode, copyMode bool) bool {
	return !jsonOutput && !quietMode && !copyMode
}

// truncateUTF8 returns the largest valid UTF-8 prefix within maxBytes.
func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// truncateUTF8FromEnd returns the largest valid UTF-8 suffix within maxBytes.
func truncateUTF8FromEnd(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// outputSnapshotPath resolves a session-scoped snapshot beneath the effective
// agent-deck data directory.
func outputSnapshotPath(sessionID, source string) (string, error) {
	dataDir, err := session.GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	safeID := strings.NewReplacer("/", "_", `\`, "_").Replace(sessionID)
	if safeID == "" || strings.Trim(safeID, ".") == "" {
		return "", fmt.Errorf("invalid session id for snapshot path: %q", sessionID)
	}
	return filepath.Join(dataDir, "output", safeID, "latest-"+source+".txt"), nil
}

// writeOutputSnapshot atomically persists raw output with private permissions.
func writeOutputSnapshot(path, raw string) (retErr error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".output-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	closed := false
	defer func() {
		if !closed {
			retErr = errors.Join(retErr, tmp.Close())
		}
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.WriteString(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	closeErr := tmp.Close()
	closed = true
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// recordOutputRead makes the spec's re-fetch guardrail queryable: a re-fetch
// is a second event for the same child session_id in the evaluation window.
func recordOutputRead(profile string, event outputReadEvent) error {
	dataDir, err := session.GetAgentDeckDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, "logs", "session-output-reads.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	event.Profile = profile
	if event.Timestamp == 0 {
		event.Timestamp = time.Now().Unix()
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return writeOutputReadLine(f, append(line, '\n'))
}

type outputReadWriteCloser interface {
	io.Writer
	io.Closer
}

// writeOutputReadLine reports both write and close failures so a buffered
// filesystem error cannot be mistaken for a durable read event.
func writeOutputReadLine(dst outputReadWriteCloser, line []byte) error {
	_, writeErr := dst.Write(line)
	closeErr := dst.Close()
	return errors.Join(writeErr, closeErr)
}
