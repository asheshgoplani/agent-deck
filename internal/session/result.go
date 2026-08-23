package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrResultNotFound = errors.New("no result yet")

const (
	ResultStateKnown   = "known"
	ResultStateUnknown = "unknown"
)

// ResultIdentity is the immutable identity of one agent turn. ProjectPath is
// intentionally absent: working directories are shared and are not identity.
type ResultIdentity struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
}

// SessionResult is the single semantic object rendered by CLI and TUI.
type SessionResult struct {
	State     string          `json:"state"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id"`
	Result    json.RawMessage `json:"result,omitempty"`
	Verdict   string          `json:"last_verdict,omitempty"`
	Source    string          `json:"source,omitempty"`
}

func UnknownSessionResult(id ResultIdentity) SessionResult {
	return SessionResult{State: ResultStateUnknown, SessionID: id.SessionID, TurnID: id.TurnID}
}

func resultArtifactPath(id ResultIdentity) (string, error) {
	root, err := dataPath("results", "results")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, sanitizeInboxName(id.SessionID), sanitizeInboxName(id.TurnID), "RESULT.json"), nil
}

// CaptureSessionResult snapshots the cwd compatibility artifact into an
// identity-scoped immutable turn artifact. It must be called at the observed
// completion edge, before another session sharing the cwd can complete.
func CaptureSessionResult(id ResultIdentity, projectPath string) (SessionResult, error) {
	if strings.TrimSpace(id.SessionID) == "" || strings.TrimSpace(id.TurnID) == "" {
		return UnknownSessionResult(id), ErrResultNotFound
	}
	result, err := readResultSource(projectPath)
	if err != nil {
		return UnknownSessionResult(id), err
	}
	path, err := resultArtifactPath(id)
	if err != nil {
		return UnknownSessionResult(id), err
	}
	sourceSum := sha256.Sum256(result.Result)
	sourceHash := hex.EncodeToString(sourceSum[:])
	markerPath := filepath.Join(filepath.Dir(filepath.Dir(path)), "source.json")
	var marker struct {
		TurnID string `json:"turn_id"`
		Hash   string `json:"hash"`
	}
	if markerData, readErr := os.ReadFile(markerPath); readErr == nil && json.Unmarshal(markerData, &marker) == nil &&
		marker.TurnID != id.TurnID && marker.Hash == sourceHash {
		return UnknownSessionResult(id), ErrResultNotFound
	}
	result.State, result.SessionID, result.TurnID = ResultStateKnown, id.SessionID, id.TurnID
	result.Source = path
	encoded, err := json.Marshal(result)
	if err != nil {
		return UnknownSessionResult(id), err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return UnknownSessionResult(id), err
	}
	if err := writeFileDurable(path, encoded, 0o644); err != nil {
		return UnknownSessionResult(id), err
	}
	marker.TurnID, marker.Hash = id.TurnID, sourceHash
	markerData, _ := json.Marshal(marker)
	if err := writeFileDurable(markerPath, markerData, 0o644); err != nil {
		return UnknownSessionResult(id), err
	}
	return result, nil
}

// ResolveSessionResult reads only the artifact for this exact session and turn.
func ResolveSessionResult(id ResultIdentity) SessionResult {
	unknown := UnknownSessionResult(id)
	if strings.TrimSpace(id.SessionID) == "" || strings.TrimSpace(id.TurnID) == "" {
		return unknown
	}
	path, err := resultArtifactPath(id)
	if err != nil {
		return unknown
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return unknown
	}
	var result SessionResult
	if json.Unmarshal(data, &result) != nil || result.State != ResultStateKnown ||
		result.SessionID != id.SessionID || result.TurnID != id.TurnID || !json.Valid(result.Result) {
		return unknown
	}
	return result
}

// ReadSessionResult is retained only for source capture and tests. Consumers
// must use ResolveSessionResult with an immutable identity.
func ReadSessionResult(sessionPath string) (SessionResult, error) {
	return readResultSource(sessionPath)
}

func readResultSource(sessionPath string) (SessionResult, error) {
	resultPath := filepath.Join(sessionPath, "RESULT.json")
	data, err := os.ReadFile(resultPath)
	if err == nil {
		data = bytes.TrimSpace(data)
		if !json.Valid(data) {
			return SessionResult{}, fmt.Errorf("invalid RESULT.json: malformed JSON")
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(data, &fields)
		return SessionResult{State: ResultStateKnown, Result: data, Verdict: jsonString(fields["verdict"]), Source: resultPath}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return SessionResult{}, err
	}
	markdownPath := filepath.Join(sessionPath, "RESULTS.md")
	data, err = os.ReadFile(markdownPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SessionResult{}, ErrResultNotFound
		}
		return SessionResult{}, err
	}
	header, status := resultMarkdownSummary(data)
	if header == "" || status == "" {
		return SessionResult{}, ErrResultNotFound
	}
	fallback, _ := json.Marshal(map[string]string{"header": header, "status": status})
	return SessionResult{State: ResultStateKnown, Result: fallback, Verdict: status, Source: markdownPath}, nil
}

func FormatSessionResult(result SessionResult) string {
	if result.State != ResultStateKnown {
		return "no result for current turn"
	}
	text := string(result.Result)
	if result.Verdict != "" {
		text += "\nVERDICT: " + result.Verdict
	}
	return text
}

func jsonString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return strings.TrimSpace(value)
}

func resultMarkdownSummary(data []byte) (header, status string) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if header == "" && strings.HasPrefix(line, "#") {
			header = strings.TrimSpace(strings.TrimLeft(line, "#"))
			if fields := strings.Fields(header); len(fields) >= 2 && strings.EqualFold(fields[0], "verdict") {
				status = strings.Join(fields[1:], " ")
			}
		}
		line = strings.Trim(line, "*_ ")
		if strings.HasPrefix(strings.ToUpper(line), "STATUS:") {
			status = strings.TrimSpace(line[len("STATUS:"):])
		}
	}
	return header, status
}

func resultFileSignal(sessionPath string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(sessionPath, "RESULT.json"))
	if err != nil || !json.Valid(bytes.TrimSpace(data)) {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
}

func resultTurnID(inst *Instance) string {
	if inst == nil {
		return ""
	}
	if signal := strings.TrimSpace(transitionEventOutputHash(inst)); signal != "" {
		return signal
	}
	// A hook timestamp is the immutable completion identity for tools whose
	// transcript/pane cannot provide a signal.
	if at, ok := inst.LastHookActivityTime(); ok && !at.IsZero() {
		return at.UTC().Format(time.RFC3339Nano)
	}
	return ""
}

func SessionResultForInstance(inst *Instance) SessionResult {
	if inst == nil {
		return UnknownSessionResult(ResultIdentity{})
	}
	return ResolveSessionResult(ResultIdentity{SessionID: inst.ID, TurnID: resultTurnID(inst)})
}
