package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// acquireHermesConfigLock serializes mutations to a given Hermes config.yaml
// across goroutines and across processes, so TUI auto-inject cannot interleave
// its read-modify-write with `agent-deck hermes-hooks` running in another
// shell.
//
// The mechanism lives in config_file_lock.go and is shared with the Codex and
// Claude config writers. This is a naming wrapper, not a second copy — Hermes
// was the last of the three still carrying a private one.
func acquireHermesConfigLock(configPath string) (*ConfigFileLock, error) {
	return AcquireConfigFileLock(configPath)
}

// agentDeckHermesHookCommand is the exact command string we write into
// config.yaml's hooks entries. Detection uses equality, not substring, so a
// user hook that happens to mention "agent-deck hook-handler" in passing isn't
// misidentified as ours and clobbered by RemoveHermesHooks.
const agentDeckHermesHookCommand = "agent-deck hook-handler"

// isAgentDeckOwnedHook returns true iff the given hook command was written by
// us. Matches the exact injected string (with surrounding whitespace tolerated)
// rather than using substring containment.
func isAgentDeckOwnedHook(cmd string) bool {
	return strings.TrimSpace(cmd) == agentDeckHermesHookCommand
}

// hermesHookEvents are the Hermes lifecycle events we subscribe to.
// pre_llm_call/post_llm_call bracket the WHOLE turn (running/waiting): per the
// Hermes docs, pre_llm_call fires once per turn before the tool-calling loop
// begins and post_llm_call fires once per turn after the final response is
// produced. Without these, a turn that generates text without calling a tool
// (the common case) never flips to "running" — the indicator was stuck on the
// prior post_tool_call/on_session_start "waiting" state while Hermes was busy.
// pre_tool_call/post_tool_call fire around each tool call nested inside that
// bracket; both map to "running", because after a tool returns the LLM is
// still generating the next step — post_llm_call owns the waiting edge.
// pre_api_request/post_api_request fire around every LLM API call and act as
// a mid-turn "running" heartbeat, so long multi-step turns don't outlive the
// hook freshness window and fade to idle.
// on_session_start provides an initial waiting state.
// on_session_end fires after EVERY run_conversation call (once per user
// message), so it is a turn-end "waiting" edge — the only one an interrupted
// turn gets, since post_llm_call is skipped on interrupt.
// on_session_finalize is the real session end (CLI exit//reset, gateway
// session eviction) and maps to "dead".
var hermesHookEvents = []string{
	"pre_llm_call",
	"post_llm_call",
	"pre_api_request",
	"post_api_request",
	"pre_tool_call",
	"post_tool_call",
	"on_session_start",
	"on_session_end",
	"on_session_finalize",
}

var hermesLegacyHookEvents = []string{"pre_tool_call", "post_tool_call", "on_session_start", "on_session_end"}
var hermesCalendarVersion = regexp.MustCompile(`(?:v)?(20[0-9]{2})\.([0-9]{1,2})\.([0-9]{1,2})`)
var hermesVocabularyCache sync.Map // configured command -> bool

// hermesExtendedHookVocabularySupported fails closed for unknown/old Hermes
// versions: those installs receive only the legacy four keys, avoiding a
// strict-schema config break. v2026.8.3 is the pinned vocabulary floor.
var hermesExtendedHookVocabularySupported = func() bool {
	if override := strings.TrimSpace(os.Getenv("AGENTDECK_HERMES_HOOK_VOCABULARY")); override != "" {
		return override == "v2026.8.3" || override == "extended"
	}
	fields := strings.Fields(GetToolCommand("hermes"))
	if len(fields) == 0 {
		return false
	}
	cacheKey := strings.Join(fields, "\x00")
	if cached, ok := hermesVocabularyCache.Load(cacheKey); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// #nosec G204 -- the executable and argv come from the operator-configured
	// Hermes tool command and are passed directly without shell evaluation.
	cmd := exec.CommandContext(ctx, fields[0], append(fields[1:], "--version")...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		hermesVocabularyCache.Store(cacheKey, false)
		return false
	}
	m := hermesCalendarVersion.FindStringSubmatch(string(out))
	if len(m) != 4 {
		hermesVocabularyCache.Store(cacheKey, false)
		return false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	supported := year > 2026 || (year == 2026 && (month > 8 || (month == 8 && day >= 3)))
	hermesVocabularyCache.Store(cacheKey, supported)
	return supported
}

func hermesHookEventsForInstall() []string {
	if hermesExtendedHookVocabularySupported() {
		return hermesHookEvents
	}
	return hermesLegacyHookEvents
}

// HermesHookEventsForInstall returns the lifecycle events an install would
// write for the configured Hermes version. The copy is safe for callers to
// retain and is used by consent UIs to disclose the exact proposed mutation.
func HermesHookEventsForInstall() []string {
	return append([]string(nil), hermesHookEventsForInstall()...)
}

// GetHermesConfigDir returns the Hermes config directory (~/.hermes).
func GetHermesConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".hermes")
	}
	return filepath.Join(home, ".hermes")
}

// InjectHermesHooks injects agent-deck hook entries into Hermes's config.yaml.
// Uses read-preserve-modify-write to keep all existing config keys intact.
// Serialized via per-config in-process mutex + cross-process flock so two
// concurrent writers (e.g. TUI auto-inject + CLI `agent-deck hermes-hooks`)
// can't tear each other's merge.
// Returns true if hooks were newly installed, false if already present.
func InjectHermesHooks(configDir string) (bool, error) {
	configPath := filepath.Join(configDir, "config.yaml")

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}

	lock, err := acquireHermesConfigLock(configPath)
	if err != nil {
		return false, err
	}
	defer lock.Release()

	var raw map[string]interface{}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read config.yaml: %w", err)
		}
		raw = make(map[string]interface{})
	} else {
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return false, fmt.Errorf("parse config.yaml: %w", err)
		}
		if raw == nil {
			raw = make(map[string]interface{})
		}
	}

	events := hermesHookEventsForInstall()
	if hermesHooksAlreadyInstalled(raw, events) {
		return false, nil
	}

	mergeHermesHookEntries(raw, events)

	out, err := yaml.Marshal(raw)
	if err != nil {
		return false, fmt.Errorf("marshal config.yaml: %w", err)
	}

	if err := atomicfile.WriteFile(configPath, out, 0600); err != nil {
		return false, fmt.Errorf("write config.yaml: %w", err)
	}

	sessionLog.Info("hermes_hooks_installed", slog.String("config_dir", configDir))
	return true, nil
}

// RemoveHermesHooks removes agent-deck hook entries from Hermes's config.yaml.
// Serialized via per-config in-process mutex + cross-process flock (see
// InjectHermesHooks).
// Returns true if hooks were removed, false if none found.
func RemoveHermesHooks(configDir string) (bool, error) {
	configPath := filepath.Join(configDir, "config.yaml")

	// Fast path: if config.yaml doesn't exist, there is nothing to remove.
	// Skip acquireHermesConfigLock so a no-op uninstall on a fresh machine
	// does not create ~/.hermes/ or the sibling .lock file just to discover
	// the config is absent.
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat config.yaml: %w", err)
	}

	lock, err := acquireHermesConfigLock(configPath)
	if err != nil {
		return false, err
	}
	defer lock.Release()

	data, err := os.ReadFile(configPath)
	if err != nil {
		// Race: file existed at stat time, then was removed before we read it.
		// Treat as no-op rather than error.
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read config.yaml: %w", err)
	}

	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("parse config.yaml: %w", err)
	}
	if raw == nil {
		return false, nil
	}

	hooksSection, _ := raw["hooks"].(map[string]interface{})
	if hooksSection == nil {
		return false, nil
	}

	removed := false
	for _, event := range hermesHookEvents {
		eventHooks, _ := hooksSection[event].([]interface{})
		// Per-event flag so we only rewrite/delete entries this event actually
		// changed. If a prior event removed our hook, `removed` would still be
		// true on later iterations and could silently drop an unrelated event
		// whose value isn't a list (type assertion fails → eventHooks==nil →
		// kept==nil → `if removed` would trigger delete on user config).
		eventRemoved := false
		var kept []interface{}
		for _, h := range eventHooks {
			hm, ok := h.(map[string]interface{})
			if !ok {
				kept = append(kept, h)
				continue
			}
			cmd, _ := hm["command"].(string)
			if isAgentDeckOwnedHook(cmd) {
				removed = true
				eventRemoved = true
				continue
			}
			kept = append(kept, h)
		}
		if eventRemoved {
			if len(kept) == 0 {
				delete(hooksSection, event)
			} else {
				hooksSection[event] = kept
			}
		}
	}

	if !removed {
		return false, nil
	}

	if len(hooksSection) == 0 {
		delete(raw, "hooks")
	} else {
		raw["hooks"] = hooksSection
	}

	out, err := yaml.Marshal(raw)
	if err != nil {
		return false, fmt.Errorf("marshal config.yaml: %w", err)
	}

	if err := atomicfile.WriteFile(configPath, out, 0600); err != nil {
		return false, fmt.Errorf("write config.yaml: %w", err)
	}

	sessionLog.Info("hermes_hooks_removed", slog.String("config_dir", configDir))
	return true, nil
}

// CheckHermesHooksInstalled returns true if all agent-deck hook entries are
// present in Hermes's config.yaml.
func CheckHermesHooksInstalled(configDir string) bool {
	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		return false
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false
	}
	return hermesHooksAlreadyInstalled(raw, hermesHookEventsForInstall())
}

// hermesHooksAlreadyInstalled checks that every required event has an
// agent-deck hook entry.
func hermesHooksAlreadyInstalled(raw map[string]interface{}, events []string) bool {
	hooksSection, _ := raw["hooks"].(map[string]interface{})
	if hooksSection == nil {
		return false
	}
	for _, event := range events {
		eventHooks, _ := hooksSection[event].([]interface{})
		found := false
		for _, h := range eventHooks {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			cmd, _ := hm["command"].(string)
			if isAgentDeckOwnedHook(cmd) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// mergeHermesHookEntries appends agent-deck hook entries for any missing events.
func mergeHermesHookEntries(raw map[string]interface{}, events []string) {
	hooksSection, _ := raw["hooks"].(map[string]interface{})
	if hooksSection == nil {
		hooksSection = make(map[string]interface{})
	}

	for _, event := range events {
		eventHooks, _ := hooksSection[event].([]interface{})
		alreadyPresent := false
		for _, h := range eventHooks {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			cmd, _ := hm["command"].(string)
			if isAgentDeckOwnedHook(cmd) {
				alreadyPresent = true
				break
			}
		}
		if !alreadyPresent {
			eventHooks = append(eventHooks, map[string]interface{}{
				"command": agentDeckHermesHookCommand,
			})
			hooksSection[event] = eventHooks
		}
	}

	raw["hooks"] = hooksSection
}
