package session

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// parsePiLastAssistantMessage resolves the last persisted branch, not Pi's
// in-memory leaf pointer, which can change without an appended JSONL entry.
func parsePiLastAssistantMessage(data []byte) (*ResponseOutput, error) {
	type entry struct {
		Type     json.RawMessage `json:"type"`
		ID       json.RawMessage `json:"id"`
		ParentID json.RawMessage `json:"parentId"`
		Version  json.RawMessage `json:"version"`
		raw      []byte
		kind     string
	}
	var entries []entry
	var header entry
	var tree, malformed, invalidHeader bool
	headerCount, version := 0, 1
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			malformed = true
			continue
		}
		e.raw = line
		if err := json.Unmarshal(e.Type, &e.kind); err != nil {
			malformed = true
		}
		if e.kind == "session" {
			headerCount++
			if headerCount != 1 || len(entries) != 0 {
				invalidHeader = true
			}
			header = e
			version = 1
			if len(e.Version) != 0 {
				// A present null must not inherit the omitted-field v1 default.
				version = 0
				if err := json.Unmarshal(e.Version, &version); err != nil || version < 1 || version > 3 {
					invalidHeader = true
				}
			}
			if version >= 2 {
				tree = true
			}
			continue
		}
		if len(e.ID) != 0 || len(e.ParentID) != 0 {
			tree = true
		}
		entries = append(entries, e)
	}
	// Legacy records have no tree identities. Preserve their linear selection
	// and partial-tail tolerance, but never downgrade a mixed/tree transcript.
	if !tree && !invalidHeader {
		return parsePiLastAssistantMessageLinear(data)
	}
	if malformed || invalidHeader || headerCount != 1 || version < 2 {
		return nil, fmt.Errorf("invalid Pi tree transcript: incomplete records or session header")
	}
	var sessionID string
	if err := json.Unmarshal(header.ID, &sessionID); err != nil || sessionID == "" {
		return nil, fmt.Errorf("invalid Pi tree session identity")
	}
	byID := make(map[string]int, len(entries))
	parents := make([]*string, len(entries))
	for n, e := range entries {
		var id string
		if err := json.Unmarshal(e.ID, &id); err != nil || id == "" || e.kind == "" {
			return nil, fmt.Errorf("invalid Pi tree entry identity at record %d", n+1)
		}
		if _, duplicate := byID[id]; duplicate {
			return nil, fmt.Errorf("duplicate Pi tree entry identity %q", id)
		}
		byID[id] = n
		if len(e.ParentID) == 0 {
			return nil, fmt.Errorf("missing Pi tree parent for %q", id)
		}
		if err := json.Unmarshal(e.ParentID, &parents[n]); err != nil || (parents[n] != nil && *parents[n] == "") {
			return nil, fmt.Errorf("invalid Pi tree parent for %q", id)
		}
	}
	var response *ResponseOutput
	visited := make(map[int]bool)
	for n := len(entries) - 1; n >= 0; {
		if visited[n] {
			return nil, fmt.Errorf("cycle in Pi tree ancestry")
		}
		visited[n] = true
		if response == nil {
			// Reuse the existing assistant text-block decoder; generic user and
			// metadata payloads cannot prevent their parent links being followed.
			if got, err := parsePiLastAssistantMessageLinear(entries[n].raw); err == nil {
				got.SessionID = sessionID
				response = got
			}
		}
		if parents[n] == nil {
			break
		}
		parent, found := byID[*parents[n]]
		if !found {
			return nil, fmt.Errorf("missing Pi tree ancestor %q", *parents[n])
		}
		n = parent
	}
	// Validate the entire chain before returning even a qualifying leaf reply.
	if response == nil {
		return nil, fmt.Errorf("no assistant response found in Pi session")
	}
	return response, nil
}
