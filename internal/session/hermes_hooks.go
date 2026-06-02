package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// hermesConfigMu serializes mutations to a given Hermes config.yaml within
// this process. Keyed by absolute config-file path so two writers in the same
// process wait for each other instead of racing the read-modify-write.
//
// Cross-process serialization is provided by advisory flock on a sibling
// `.lock` file (see acquireHermesConfigLock). Together they cover both cases
// that matter: TUI auto-inject racing with `agent-deck hermes-hooks` in
// another shell, or `-race` tests inside one binary.
var hermesConfigMu sync.Map // map[string]*sync.Mutex

// hermesConfigLock holds both lock layers; Release() unwinds them in reverse.
type hermesConfigLock struct {
	inProc *sync.Mutex
	file   *os.File
}

func (l *hermesConfigLock) Release() {
	if l.file != nil {
		// Best-effort: LOCK_UN errors are non-actionable; Close drops the fd
		// either way, which also releases the lock.
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
	}
	if l.inProc != nil {
		l.inProc.Unlock()
	}
}

// acquireHermesConfigLock takes the in-process mutex for this config path,
// then an exclusive advisory file lock on `<configPath>.lock`. Both must
// release before another writer can proceed.
func acquireHermesConfigLock(configPath string) (*hermesConfigLock, error) {
	mIface, _ := hermesConfigMu.LoadOrStore(configPath, &sync.Mutex{})
	m := mIface.(*sync.Mutex)
	m.Lock()

	lockPath := configPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		m.Unlock()
		return nil, fmt.Errorf("ensure hermes config lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		m.Unlock()
		return nil, fmt.Errorf("open hermes config lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		m.Unlock()
		return nil, fmt.Errorf("flock hermes config: %w", err)
	}
	return &hermesConfigLock{inProc: m, file: f}, nil
}

// uniqueHermesConfigTmpPath returns a per-writer temp filename so two
// concurrent writers can't rename-clobber each other's in-flight payload even
// if the locking layer above is bypassed for any reason. PID + nanosecond
// timestamp is sufficient: only one writer in any given process can be at this
// point at a time (we hold the in-process mutex), and nanosecond timestamps
// don't collide across processes within a 1ns window in practice.
func uniqueHermesConfigTmpPath(target string) string {
	return fmt.Sprintf("%s.%d.%d.tmp", target, os.Getpid(), time.Now().UnixNano())
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
// pre_tool_call/post_tool_call bracket each tool call (running/waiting).
// on_session_start provides an initial waiting state.
// on_session_end signals the session is dead.
var hermesHookEvents = []string{
	"pre_tool_call",
	"post_tool_call",
	"on_session_start",
	"on_session_end",
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

	if hermesHooksAlreadyInstalled(raw) {
		return false, nil
	}

	mergeHermesHookEntries(raw)

	out, err := yaml.Marshal(raw)
	if err != nil {
		return false, fmt.Errorf("marshal config.yaml: %w", err)
	}

	tmpPath := uniqueHermesConfigTmpPath(configPath)
	if err := os.WriteFile(tmpPath, out, 0600); err != nil {
		return false, fmt.Errorf("write config.yaml tmp: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename config.yaml: %w", err)
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

	tmpPath := uniqueHermesConfigTmpPath(configPath)
	if err := os.WriteFile(tmpPath, out, 0600); err != nil {
		return false, fmt.Errorf("write config.yaml tmp: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename config.yaml: %w", err)
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
	return hermesHooksAlreadyInstalled(raw)
}

// hermesHooksAlreadyInstalled checks that every required event has an
// agent-deck hook entry.
func hermesHooksAlreadyInstalled(raw map[string]interface{}) bool {
	hooksSection, _ := raw["hooks"].(map[string]interface{})
	if hooksSection == nil {
		return false
	}
	for _, event := range hermesHookEvents {
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
func mergeHermesHookEntries(raw map[string]interface{}) {
	hooksSection, _ := raw["hooks"].(map[string]interface{})
	if hooksSection == nil {
		hooksSection = make(map[string]interface{})
	}

	for _, event := range hermesHookEvents {
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
