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
	"strconv"
	"strings"
	"syscall"
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

type resultSourceFingerprint struct {
	Exists bool   `json:"exists"`
	Dev    uint64 `json:"dev,omitempty"`
	Ino    uint64 `json:"ino,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Mtime  int64  `json:"mtime_ns,omitempty"`
	Ctime  int64  `json:"ctime_ns,omitempty"`
}

type resultSourceOwnership struct {
	SessionID string                  `json:"session_id"`
	TurnID    string                  `json:"turn_id,omitempty"`
	Conflict  bool                    `json:"conflict,omitempty"`
	JSON      resultSourceFingerprint `json:"result_json"`
	Markdown  resultSourceFingerprint `json:"results_md"`
}

var resultSourceCaptureHook func()

func resultSourcePath(projectPath string) string { return filepath.Join(projectPath, "RESULT.json") }

func resultSourceOwnershipPath(projectPath string) (string, error) {
	root, err := dataPath("results", "results")
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Clean(projectPath))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(root, "source-ownership", hex.EncodeToString(sum[:])+".json"), nil
}

func statResultSource(path string) (resultSourceFingerprint, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return resultSourceFingerprint{}, nil
	}
	if err != nil {
		return resultSourceFingerprint{}, err
	}
	fp := resultSourceFingerprint{Exists: true, Size: info.Size(), Mtime: info.ModTime().UnixNano()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		fp.Dev, fp.Ino = uint64(st.Dev), uint64(st.Ino)
		fp.Ctime = st.Ctim.Sec*int64(time.Second) + st.Ctim.Nsec
	}
	return fp, nil
}

// ClaimSessionResultSource records the only legacy session allowed to produce
// the next unlabelled compatibility artifact in this directory. The claim is
// made while that session is observed running, before capture, and is shared by
// all agent-deck processes. An existing claim is never stolen.
func ClaimSessionResultSource(sessionID, projectPath string) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(projectPath) == "" {
		return ErrResultNotFound
	}
	recordPath, err := resultSourceOwnershipPath(projectPath)
	if err != nil {
		return err
	}
	lock, err := AcquireConfigFileLock(recordPath)
	if err != nil {
		return err
	}
	defer lock.Release()
	var owner resultSourceOwnership
	if data, readErr := os.ReadFile(recordPath); readErr == nil && json.Unmarshal(data, &owner) == nil && owner.SessionID != "" {
		if owner.SessionID == sessionID && !owner.Conflict {
			return nil
		}
		owner.Conflict = true
		data, _ = json.Marshal(owner)
		if writeErr := writeFileDurable(recordPath, data, 0o644); writeErr != nil {
			return writeErr
		}
		return ErrResultNotFound
	}
	jsonBaseline, err := statResultSource(resultSourcePath(projectPath))
	if err != nil {
		return err
	}
	markdownBaseline, err := statResultSource(filepath.Join(projectPath, "RESULTS.md"))
	if err != nil {
		return err
	}
	owner = resultSourceOwnership{SessionID: sessionID, JSON: jsonBaseline, Markdown: markdownBaseline}
	data, _ := json.Marshal(owner)
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o755); err != nil {
		return err
	}
	return writeFileDurable(recordPath, data, 0o644)
}

// CaptureSessionResult snapshots the cwd compatibility artifact into an
// identity-scoped immutable turn artifact. It must be called at the observed
// completion edge, before another session sharing the cwd can complete.
func CaptureSessionResult(id ResultIdentity, projectPath string) (SessionResult, error) {
	if strings.TrimSpace(id.SessionID) == "" || strings.TrimSpace(id.TurnID) == "" {
		return UnknownSessionResult(id), ErrResultNotFound
	}
	// Serialize source observation, ownership assignment, and immutable copy.
	recordPath, err := resultSourceOwnershipPath(projectPath)
	if err != nil {
		return UnknownSessionResult(id), err
	}
	lock, err := AcquireConfigFileLock(recordPath)
	if err != nil {
		return UnknownSessionResult(id), err
	}
	defer lock.Release()

	result, err := readResultSource(projectPath)
	if err != nil {
		return UnknownSessionResult(id), err
	}
	sourcePath := result.Source
	before, err := statResultSource(sourcePath)
	if err != nil {
		return UnknownSessionResult(id), err
	}
	// Re-read while holding the ownership lock so replacement or mutation
	// between selection and copy cannot be promoted.
	if resultSourceCaptureHook != nil {
		resultSourceCaptureHook()
	}
	result, err = readResultSource(projectPath)
	if err != nil || result.Source != sourcePath {
		return UnknownSessionResult(id), ErrResultNotFound
	}
	after, err := statResultSource(sourcePath)
	if err != nil || before != after {
		return UnknownSessionResult(id), ErrResultNotFound
	}

	var embedded struct {
		SessionID *string `json:"session_id"`
		TurnID    *string `json:"turn_id"`
	}
	hasEmbedded := false
	if strings.HasSuffix(result.Source, "RESULT.json") && json.Unmarshal(result.Result, &embedded) == nil {
		hasEmbedded = embedded.SessionID != nil || embedded.TurnID != nil
	}
	if hasEmbedded {
		if embedded.SessionID == nil || embedded.TurnID == nil || *embedded.SessionID != id.SessionID || *embedded.TurnID != id.TurnID {
			return UnknownSessionResult(id), ErrResultNotFound
		}
	} else {
		var owner resultSourceOwnership
		data, readErr := os.ReadFile(recordPath)
		if readErr != nil || json.Unmarshal(data, &owner) != nil {
			return UnknownSessionResult(id), ErrResultNotFound
		}
		if owner.Conflict || owner.SessionID != id.SessionID {
			// A competing producer was observed. Retire the conflicted lease so a
			// later clean running edge may establish a fresh baseline.
			consumed := recordPath + ".conflict." + strconv.FormatInt(time.Now().UnixNano(), 10)
			_ = os.Rename(recordPath, consumed)
			return UnknownSessionResult(id), ErrResultNotFound
		}
		if owner.TurnID != "" && owner.TurnID != id.TurnID {
			return UnknownSessionResult(id), ErrResultNotFound
		}
		baseline := owner.JSON
		if strings.HasSuffix(sourcePath, "RESULTS.md") {
			baseline = owner.Markdown
		}
		if baseline == after {
			return UnknownSessionResult(id), ErrResultNotFound
		}
		// Bind the lease to this exact turn before copying. After a crash, only
		// this turn can retry; a later turn cannot inherit the changed source.
		if owner.TurnID == "" {
			owner.TurnID = id.TurnID
			data, _ = json.Marshal(owner)
			if err := writeFileDurable(recordPath, data, 0o644); err != nil {
				return UnknownSessionResult(id), err
			}
		}
	}
	path, err := resultArtifactPath(id)
	if err != nil {
		return UnknownSessionResult(id), err
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
	// Copy first, then consume the claim. A crash in between leaves either a
	// resolvable immutable artifact or a retryable claim; it never assigns the
	// source to another session.
	if !hasEmbedded {
		consumed := recordPath + ".consumed." + sanitizeInboxName(id.SessionID) + "." + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := os.Rename(recordPath, consumed); err != nil {
			return UnknownSessionResult(id), err
		}
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
