package session

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/docker"
)

const codexTrustLevelTrusted = "trusted"

// codexConfigMu serializes mutations to a given Codex config.toml within this
// process. Cross-process serialization uses advisory flock on a sibling
// `.lock` file (see acquireCodexConfigLock), matching hermes_hooks.go.
var codexConfigMu sync.Map // map[string]*sync.Mutex

type codexConfigLock struct {
	inProc *sync.Mutex
	file   *os.File
}

func (l *codexConfigLock) Release() {
	if l.file != nil {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
	}
	if l.inProc != nil {
		l.inProc.Unlock()
	}
}

func acquireCodexConfigLock(configPath string) (*codexConfigLock, error) {
	mIface, _ := codexConfigMu.LoadOrStore(configPath, &sync.Mutex{})
	m := mIface.(*sync.Mutex)
	m.Lock()

	lockPath := configPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		m.Unlock()
		return nil, fmt.Errorf("ensure codex config lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		m.Unlock()
		return nil, fmt.Errorf("open codex config lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		m.Unlock()
		return nil, fmt.Errorf("flock codex config: %w", err)
	}
	return &codexConfigLock{inProc: m, file: f}, nil
}

// GetCodexConfigPath returns the path to Codex's user-level config.toml under codexHome.
func GetCodexConfigPath(codexHome string) string {
	return filepath.Join(codexHome, "config.toml")
}

// PreAcceptCodexTrust adds `projects[projectDir].trust_level = "trusted"` to the
// Codex config at codexConfigPath, preserving all existing top-level fields and
// project entries.
//
// Why this exists: Codex prompts "do you trust the files in this folder?" on first
// launch in a directory, blocking unattended TUI/headless session starts. The
// trust state is keyed by the literal projectDir string in ~/.codex/config.toml —
// pre-seeding the entry skips the prompt the same way accepting it in the UI would.
//
// projectDir must be the workspace root Codex will see (absolute on the host for
// normal sessions, /workspace inside sandbox containers).
func PreAcceptCodexTrust(codexConfigPath, projectDir string) error {
	if codexConfigPath == "" {
		return fmt.Errorf("codexConfigPath is empty")
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return fmt.Errorf("projectDir is empty")
	}
	if !filepath.IsAbs(projectDir) {
		abs, err := filepath.Abs(ExpandPath(projectDir))
		if err != nil {
			return fmt.Errorf("resolve absolute projectDir: %w", err)
		}
		projectDir = abs
	}

	lock, err := acquireCodexConfigLock(codexConfigPath)
	if err != nil {
		return err
	}
	defer lock.Release()

	cfg := map[string]any{}
	if data, err := os.ReadFile(codexConfigPath); err == nil {
		if len(data) > 0 {
			if err := toml.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("parse %s: %w", codexConfigPath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", codexConfigPath, err)
	}

	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	if entry, ok := projects[projectDir].(map[string]any); ok {
		if trust, _ := entry["trust_level"].(string); trust == codexTrustLevelTrusted {
			return nil
		}
	}
	entry, _ := projects[projectDir].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["trust_level"] = codexTrustLevelTrusted
	projects[projectDir] = entry
	cfg["projects"] = projects

	if err := os.MkdirAll(filepath.Dir(codexConfigPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", codexConfigPath, err)
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("marshal codex config: %w", err)
	}
	if err := atomicfile.WriteFile(codexConfigPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", codexConfigPath, err)
	}
	return nil
}

// ApplyMultiRepoCodexContext pre-accepts the Codex workspace-trust dialog for a
// multi-repo parent directory. Only acts when tool is Codex-compatible AND
// multiRepoEnabled is true. For sandboxed multi-repo sessions, trust for
// /workspace is seeded separately during sandbox config sync.
func ApplyMultiRepoCodexContext(tool string, multiRepoEnabled bool, parentDir string) error {
	if !IsCodexCompatible(tool) || !multiRepoEnabled {
		return nil
	}
	return PreAcceptCodexTrust(GetCodexConfigPath(getCodexHomeDir()), parentDir)
}

func (i *Instance) preAcceptCodexWorkspaceTrust() {
	if i == nil || !IsCodexCompatible(i.Tool) {
		return
	}
	if i.IsSandboxed() {
		// Sandbox trust is seeded after agent config sync in ensureSandboxContainer
		// so a host config.toml copy does not clobber the trust entry.
		return
	}
	projectDir := i.EffectiveWorkingDir()
	configPath := GetCodexConfigPath(i.getCodexHomeDir())
	if err := PreAcceptCodexTrust(configPath, projectDir); err != nil {
		sessionLog.Warn("codex_preaccept_trust_failed",
			slog.String("instance_id", i.ID),
			slog.String("path", projectDir),
			slog.String("config", configPath),
			slog.String("error", err.Error()))
	}
}

// PreAcceptCodexSandboxWorkspaceTrust seeds trust for the sandbox workspace in
// the host-side sandbox copy of ~/.codex/config.toml. Call after
// docker.RefreshAgentConfigs so host file sync does not overwrite the entry.
func PreAcceptCodexSandboxWorkspaceTrust(homeDir string) error {
	if homeDir == "" {
		return nil
	}
	configPath := filepath.Join(docker.SandboxDir(homeDir, ".codex"), "config.toml")
	return PreAcceptCodexTrust(configPath, docker.ContainerWorkDir())
}
