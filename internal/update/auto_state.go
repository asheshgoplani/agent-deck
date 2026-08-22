package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const AutoStateFileName = "auto-update-state.json"

type AutoState struct {
	Status  string    `json:"status"`
	Version string    `json:"version,omitempty"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}

func SaveAutoState(status, version, message string) {
	dir, err := getCacheDir()
	if err != nil || os.MkdirAll(dir, 0o755) != nil {
		return
	}
	data, err := json.MarshalIndent(AutoState{Status: status, Version: version, Message: message, At: time.Now()}, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(dir, AutoStateFileName), data, 0o644)
	}
}

func LoadAutoState() (*AutoState, error) {
	dir, err := getCacheDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, AutoStateFileName))
	if err != nil {
		return nil, err
	}
	var state AutoState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}
