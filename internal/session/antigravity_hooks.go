package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

const (
	agentDeckAntigravityHookBlockName = "agent-deck"
)

type antigravityFlatHookEntry struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type antigravityNamedHooks struct {
	Enabled       bool                       `json:"enabled,omitempty"`
	PreInvocation []antigravityFlatHookEntry `json:"PreInvocation,omitempty"`
	Stop          []antigravityFlatHookEntry `json:"Stop,omitempty"`
}

func antigravityAgentDeckHookEntry(event string) antigravityFlatHookEntry {
	return antigravityFlatHookEntry{
		Type:    "command",
		Command: "agent-deck antigravity-hook " + event,
		Timeout: 5,
	}
}

// InjectAntigravityHooks injects agent-deck hooks into ~/.gemini/config/hooks.json
func InjectAntigravityHooks(configDir string) (bool, error) {
	hooksPath := filepath.Join(configDir, "hooks.json")

	var raw map[string]json.RawMessage
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read hooks.json: %w", err)
		}
		raw = make(map[string]json.RawMessage)
	} else if err := json.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("parse hooks.json: %w", err)
	}

	if antigravityHooksAlreadyInstalled(raw) {
		return false, nil
	}

	block := antigravityNamedHooks{Enabled: true}
	block.PreInvocation = []antigravityFlatHookEntry{antigravityAgentDeckHookEntry("PreInvocation")}
	block.Stop = []antigravityFlatHookEntry{antigravityAgentDeckHookEntry("Stop")}

	blockRaw, err := json.Marshal(block)
	if err != nil {
		return false, fmt.Errorf("marshal agent-deck hook block: %w", err)
	}
	raw[agentDeckAntigravityHookBlockName] = blockRaw

	finalData, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal hooks.json: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}
	if err := atomicfile.WriteFile(hooksPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write hooks.json: %w", err)
	}

	sessionLog.Info("antigravity_hooks_installed", slog.String("config_dir", configDir))
	return true, nil
}

// RemoveAntigravityHooks removes agent-deck hooks from hooks.json
func RemoveAntigravityHooks(configDir string) (bool, error) {
	hooksPath := filepath.Join(configDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read hooks.json: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("parse hooks.json: %w", err)
	}

	if _, ok := raw[agentDeckAntigravityHookBlockName]; !ok {
		return false, nil
	}
	delete(raw, agentDeckAntigravityHookBlockName)

	finalData, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal hooks.json: %w", err)
	}
	if len(raw) == 0 {
		if err := os.Remove(hooksPath); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("remove empty hooks.json: %w", err)
		}
	} else if err := atomicfile.WriteFile(hooksPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write hooks.json: %w", err)
	}

	sessionLog.Info("antigravity_hooks_removed", slog.String("config_dir", configDir))
	return true, nil
}

// CheckAntigravityHooksInstalled reports whether agent-deck agy hooks are present
func CheckAntigravityHooksInstalled(configDir string) bool {
	hooksPath := filepath.Join(configDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		return false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	return antigravityHooksAlreadyInstalled(raw)
}

func antigravityHooksAlreadyInstalled(raw map[string]json.RawMessage) bool {
	blockRaw, ok := raw[agentDeckAntigravityHookBlockName]
	if !ok {
		return false
	}
	var block antigravityNamedHooks
	if err := json.Unmarshal(blockRaw, &block); err != nil {
		return false
	}
	return antigravityBlockHasAgentDeckHook(block.PreInvocation) &&
		antigravityBlockHasAgentDeckHook(block.Stop)
}

func antigravityBlockHasAgentDeckHook(entries []antigravityFlatHookEntry) bool {
	for _, e := range entries {
		if strings.Contains(e.Command, "agent-deck") && strings.Contains(e.Command, "antigravity-hook") {
			return true
		}
	}
	return false
}
