package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// installMissingCodexHooks only fills an absent notify setting. An existing
// setting, including an empty array or a legacy table, belongs to the user;
// creation must not replace it or silently undo an explicit disablement.
// Remote add runs add/launch on the owning host. SSH wrappers and sandboxes
// must not install a hook in this host's Codex config on their behalf.
func installMissingCodexHooks(inst *session.Instance) error {
	if inst == nil || inst.IsSandboxed() {
		return nil
	}
	if cfg, err := session.LoadUserConfig(); err != nil {
		return fmt.Errorf("reading config for Codex hook installation: %w", err)
	} else if cfg != nil && strings.TrimSpace(os.Getenv("CODEX_HOME")) == "" && (len(cfg.Shell.EnvFiles) > 0 || cfg.Shell.InitScript != "" || cfg.Shell.GetLaunchShell() || cfg.Codex.EnvFile != "") {
		return fmt.Errorf("shell startup can change the Codex home; set CODEX_HOME explicitly or install hooks manually")
	}
	if session.IsCodexCompatible(inst.Tool) && (inst.Wrapper != "" || inst.Tool != "codex" || (strings.TrimSpace(inst.Command) != "" && strings.TrimSpace(inst.Command) != "codex") || session.GetCodexCommand() != "codex") {
		return fmt.Errorf("cannot safely resolve a custom Codex command home; install hooks manually")
	}
	home := inst.ResolvedCodexHome()
	if home == "" {
		return nil
	}
	if !filepath.IsAbs(home) {
		return fmt.Errorf("Codex home %q is relative; install hooks manually from the session's working directory", home)
	}
	configPath := filepath.Join(home, "config.toml")
	lock, err := session.AcquireConfigFileLockTimeout(configPath, time.Second)
	if err != nil {
		return fmt.Errorf("locking Codex config: %w", err)
	}
	defer lock.Release()

	mode := os.FileMode(0o600)
	if info, err := os.Lstat(configPath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Codex config %s is not a regular file; install hooks manually", configPath)
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking Codex config: %w", err)
	}
	content, err := readFileOrEmpty(configPath)
	if err != nil {
		return fmt.Errorf("reading Codex config: %w", err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal([]byte(content), &cfg); err != nil {
		return fmt.Errorf("parsing Codex config: %w", err)
	}
	if _, exists := cfg["notify"]; exists {
		return nil
	}

	// Prepend the root key without re-encoding or trimming the user's file.
	// Validate the result too: e.g. a leading BOM must not become an interior
	// BOM in a file that Codex can no longer parse.
	updated := codexNotifyMarkerBegin + "\n" + codexNotifyLine + "\n" + codexNotifyMarkerEnd + "\n\n" + content
	if err := toml.Unmarshal([]byte(updated), &cfg); err != nil {
		return fmt.Errorf("validating Codex notify config: %w", err)
	}
	if _, err := os.Stat(configPath); err == nil {
		// An atomic rename alone would bypass a read-only file's permissions.
		f, err := os.OpenFile(configPath, os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("opening Codex config for writing: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("closing Codex config: %w", err)
		}
	}
	if err := atomicfile.WriteFileDurable(configPath, []byte(updated), mode); err != nil {
		return fmt.Errorf("installing Codex notify hook: %w", err)
	}
	return nil
}
