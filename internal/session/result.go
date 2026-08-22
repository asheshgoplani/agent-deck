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
)

var ErrResultNotFound = errors.New("no result yet")

// SessionResult is the bounded result surface used by parent sessions. Result
// contains the original RESULT.json object, or the small markdown fallback.
type SessionResult struct {
	Result  json.RawMessage `json:"result"`
	Verdict string          `json:"last_verdict"`
	Source  string          `json:"source"`
}

// ReadSessionResult reads only result artifacts from a session directory. It
// deliberately never consults the transcript.
func ReadSessionResult(sessionPath string) (SessionResult, error) {
	resultPath := filepath.Join(sessionPath, "RESULT.json")
	data, err := os.ReadFile(resultPath)
	if err == nil {
		data = bytes.TrimSpace(data)
		if !json.Valid(data) {
			return SessionResult{}, fmt.Errorf("invalid RESULT.json: malformed JSON")
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(data, &fields)
		verdict := jsonString(fields["verdict"])
		return SessionResult{Result: data, Verdict: verdict, Source: resultPath}, nil
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
	return SessionResult{Result: fallback, Verdict: status, Source: markdownPath}, nil
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
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "STATUS:") {
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
