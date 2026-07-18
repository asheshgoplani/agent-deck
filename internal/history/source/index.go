package source

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
)

type IndexEntry struct {
	LastSessionID  string
	LastModifiedMs int64
}

func ReadClaudeIndex(path string) (map[string]IndexEntry, error) {
	out := map[string]IndexEntry{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var doc struct {
		Projects map[string]struct {
			LastSessionID       string `json:"lastSessionId"`
			LastSessionModified string `json:"lastSessionModified"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return out, err
	}
	for p, e := range doc.Projects {
		ms, _ := strconv.ParseInt(e.LastSessionModified, 10, 64)
		out[p] = IndexEntry{LastSessionID: e.LastSessionID, LastModifiedMs: ms}
	}
	return out, nil
}
