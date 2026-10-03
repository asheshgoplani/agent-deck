package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

const agentDeckCursorHookCommand = "agent-deck hook-handler"

// cursorHookDef is the one field of a hook entry agent-deck reads. Entries
// are otherwise kept as raw JSON, so a user's timeout, loop_limit,
// failClosed or prompt-type hook survives every install byte for byte.
type cursorHookDef struct {
	Command string `json:"command"`
}

// cursorHookEventNames are the events every install subscribes to.
var cursorHookEventNames = []string{
	"sessionStart",
	"sessionEnd",
	"beforeSubmitPrompt",
	"preToolUse",
	"postToolUse",
	"stop",
}

// cursorCommsHookEvent carries the agent's final text (stop carries only a
// status); the Comms Ledger producer reads it (docs/comms.md). It is
// installed, and required by the installed check, only with [comms] ledger
// on, so an install made before the ledger is left exactly as it is.
const cursorCommsHookEvent = "afterAgentResponse"

// cursorHookEventsForInstall is the event set an install writes and the
// installed check requires.
func cursorHookEventsForInstall() []string {
	if CommsLedgerEnabled() {
		return append(append([]string(nil), cursorHookEventNames...), cursorCommsHookEvent)
	}
	return cursorHookEventNames
}

// cursorHookEventsEverInstalled is every event an uninstall must clean.
func cursorHookEventsEverInstalled() []string {
	return append(append([]string(nil), cursorHookEventNames...), cursorCommsHookEvent)
}

// cursorHooksFile is hooks.json with only the parts agent-deck touches
// decoded: top-level keys other than "hooks" and each hook entry are raw.
type cursorHooksFile struct {
	top   map[string]json.RawMessage
	hooks map[string][]json.RawMessage
}

func readCursorHooksFile(hooksPath string) (*cursorHooksFile, bool, error) {
	f := &cursorHooksFile{top: map[string]json.RawMessage{}, hooks: map[string][]json.RawMessage{}}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		if os.IsNotExist(err) {
			return f, false, nil
		}
		return nil, false, fmt.Errorf("read hooks.json: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return f, true, nil
	}
	if err := json.Unmarshal(data, &f.top); err != nil {
		return nil, true, fmt.Errorf("parse hooks.json: %w", err)
	}
	if raw, ok := f.top["hooks"]; ok && len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &f.hooks); err != nil {
			return nil, true, fmt.Errorf("parse hooks.json: %w", err)
		}
	}
	return f, true, nil
}

func (f *cursorHooksFile) write(hooksPath string) error {
	hooksRaw, err := json.Marshal(f.hooks)
	if err != nil {
		return fmt.Errorf("marshal hooks.json: %w", err)
	}
	if len(f.hooks) == 0 {
		delete(f.top, "hooks")
	} else {
		f.top["hooks"] = hooksRaw
	}
	if _, ok := f.top["version"]; !ok {
		f.top["version"] = json.RawMessage("1")
	}
	finalData, err := json.MarshalIndent(f.top, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hooks.json: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := atomicfile.WriteFile(hooksPath, finalData, 0644); err != nil {
		return fmt.Errorf("write hooks.json: %w", err)
	}
	return nil
}

// InjectCursorHooks injects agent-deck hook entries into ~/.cursor/hooks.json.
// Read-preserve-modify-write: only the agent-deck entries are added, every
// other byte of the file is written back as it was.
// Returns true if hooks were newly installed, false if already present.
func InjectCursorHooks(configDir string) (bool, error) {
	hooksPath := filepath.Join(configDir, "hooks.json")
	f, _, err := readCursorHooksFile(hooksPath)
	if err != nil {
		return false, err
	}
	events := cursorHookEventsForInstall()
	if cursorHooksAlreadyInstalled(f.hooks, events) {
		return false, nil
	}
	for _, event := range events {
		f.hooks[event] = mergeCursorHookEvent(f.hooks[event])
	}
	if err := f.write(hooksPath); err != nil {
		return false, err
	}
	sessionLog.Info("cursor_hooks_installed", slog.String("config_dir", configDir))
	return true, nil
}

// AutoInstallCursorHooks is the TUI-startup entry point for silent Cursor hook
// injection. Unlike InjectCursorHooks (the explicit `cursor-hooks install`
// path), it honors the user's durable opt-out: [cursor] hooks_enabled = false,
// which `cursor-hooks uninstall` persists. Without this gate, TUI startup
// silently re-created the hooks on every launch after an uninstall (issue #1672).
// Returns true if hooks were newly installed.
func AutoInstallCursorHooks(cfg *UserConfig, configDir string) (bool, error) {
	if cfg != nil && !cfg.Cursor.GetHooksEnabled() {
		return false, nil
	}
	if CheckCursorHooksInstalled(configDir) {
		return false, nil
	}
	return InjectCursorHooks(configDir)
}

// SetCursorHooksEnabled persists the Cursor hooks opt-in/opt-out to config.toml.
// enabled=false writes [cursor] hooks_enabled = false (durable opt-out honored
// by AutoInstallCursorHooks); enabled=true removes the key, restoring the
// default. Works on a shallow copy so the LoadUserConfig cache is not mutated
// before the save lands.
func SetCursorHooksEnabled(enabled bool) error {
	cfg, err := LoadUserConfig()
	if err != nil {
		return err
	}
	// No-op when the effective state already matches: skips a full
	// config.toml rewrite (which drops comments/formatting).
	if cfg.Cursor.GetHooksEnabled() == enabled {
		return nil
	}
	updated := *cfg
	if enabled {
		updated.Cursor.HooksEnabled = nil
	} else {
		disabled := false
		updated.Cursor.HooksEnabled = &disabled
	}
	return SaveUserConfig(&updated)
}

// RemoveCursorHooks removes agent-deck hook entries from ~/.cursor/hooks.json.
// Returns true if hooks were removed, false if none found.
func RemoveCursorHooks(configDir string) (bool, error) {
	hooksPath := filepath.Join(configDir, "hooks.json")
	f, exists, err := readCursorHooksFile(hooksPath)
	if err != nil {
		return false, err
	}
	if !exists || len(f.hooks) == 0 {
		return false, nil
	}
	removed := false
	for _, event := range cursorHookEventsEverInstalled() {
		cleaned, didRemove := removeAgentDeckFromCursorEvent(f.hooks[event])
		if !didRemove {
			continue
		}
		removed = true
		if len(cleaned) == 0 {
			delete(f.hooks, event)
		} else {
			f.hooks[event] = cleaned
		}
	}
	if !removed {
		return false, nil
	}
	if err := f.write(hooksPath); err != nil {
		return false, err
	}
	sessionLog.Info("cursor_hooks_removed", slog.String("config_dir", configDir))
	return true, nil
}

// CheckCursorHooksInstalled reports whether required agent-deck Cursor hooks are installed.
func CheckCursorHooksInstalled(configDir string) bool {
	f, exists, err := readCursorHooksFile(filepath.Join(configDir, "hooks.json"))
	if err != nil || !exists {
		return false
	}
	return cursorHooksAlreadyInstalled(f.hooks, cursorHookEventsForInstall())
}

func cursorHooksAlreadyInstalled(hooks map[string][]json.RawMessage, events []string) bool {
	for _, event := range events {
		if !cursorEventHasAgentDeckHook(hooks[event]) {
			return false
		}
	}
	return true
}

func cursorEntryCommand(raw json.RawMessage) string {
	var d cursorHookDef
	if json.Unmarshal(raw, &d) != nil {
		return ""
	}
	return d.Command
}

func cursorEventHasAgentDeckHook(defs []json.RawMessage) bool {
	for _, d := range defs {
		if strings.Contains(cursorEntryCommand(d), agentDeckCursorHookCommand) {
			return true
		}
	}
	return false
}

func mergeCursorHookEvent(existing []json.RawMessage) []json.RawMessage {
	if cursorEventHasAgentDeckHook(existing) {
		return existing
	}
	entry, _ := json.Marshal(cursorHookDef{Command: agentDeckCursorHookCommand})
	return append(existing, entry)
}

func removeAgentDeckFromCursorEvent(defs []json.RawMessage) ([]json.RawMessage, bool) {
	removed := false
	var cleaned []json.RawMessage
	for _, d := range defs {
		if strings.Contains(cursorEntryCommand(d), agentDeckCursorHookCommand) {
			removed = true
			continue
		}
		cleaned = append(cleaned, d)
	}
	return cleaned, removed
}
