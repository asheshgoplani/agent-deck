package main

import (
	"encoding/json"
	"fmt"
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
// it. The footer is part of the budget, so --max-tokens is always an upper
// bound under the documented four-bytes-per-token approximation.
func prepareAgentBoundaryOutput(raw string, maxTokens int, fullPath string) (string, bool) {
	clean := tmux.StripANSI(raw)
	budget := maxTokens * outputBytesPerToken
	if len(clean) <= budget {
		return clean, false
	}

	footer := fmt.Sprintf("\n\n… output truncated to %d tokens; full output at %s\n", maxTokens, fullPath)
	contentBudget := budget - len(footer)
	if contentBudget <= 0 {
		return truncateUTF8(footer, budget), true
	}

	headBytes := (contentBudget + 1) / 2
	tailBytes := contentBudget - headBytes
	head := truncateUTF8(clean, headBytes)
	tail := truncateUTF8FromEnd(clean, tailBytes)
	return head + tail + footer, true
}

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

func outputSnapshotPath(sessionID, source string) (string, error) {
	dataDir, err := session.GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	safeID := strings.NewReplacer("/", "_", `\`, "_").Replace(sessionID)
	return filepath.Join(dataDir, "output", safeID, "latest-"+source+".txt"), nil
}

func writeOutputSnapshot(path, raw string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".output-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
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
	if err := tmp.Close(); err != nil {
		return err
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
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
