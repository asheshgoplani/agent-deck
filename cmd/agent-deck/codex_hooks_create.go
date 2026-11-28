package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// installMissingCodexHooks preserves every existing root notify setting.
func installMissingCodexHooks(inst *session.Instance) error {
	path, reason, err := resolveCodexHooksEligibility(inst)
	if err != nil {
		return err
	}
	if reason != "" {
		fmt.Fprintf(os.Stderr, "Note: Codex hooks: %s\n", reason)
		return nil
	}
	result, err := installCodexHooks(path, codexHooksInstallMissingOnly)
	if err != nil {
		return err
	}
	if result.Outcome == codexHooksOutcomeSkipped || result.Outcome == codexHooksOutcomeAlreadyInstalled {
		fmt.Fprintf(os.Stderr, "Note: Codex hooks: %s (%q)\n", result.Reason, path)
	}
	if result.Outcome != codexHooksOutcomeSkipped && path != getCodexConfigPath() {
		home := shellescape.Quote(filepath.Dir(path))
		fmt.Fprintf(os.Stderr, "Codex hooks config: %q\n", path)
		fmt.Fprintf(os.Stderr, "Check with: CODEX_HOME=%s agent-deck codex-hooks status\n", home)
		fmt.Fprintf(os.Stderr, "Remove with: CODEX_HOME=%s agent-deck codex-hooks uninstall\n", home)
	}
	return nil
}

// resolveCodexHooksEligibility checks session ownership before local config.
func resolveCodexHooksEligibility(inst *session.Instance) (configPath, reason string, err error) {
	switch {
	case inst == nil:
		return "", "no session was provided", nil
	case !session.IsCodexCompatible(inst.Tool):
		return "", "session does not use Codex", nil
	case inst.IsSSH():
		return "", "SSH sessions use the remote host's Codex config", nil
	case inst.IsSandboxed():
		return "", "sandbox sessions use the sandbox's Codex config", nil
	}
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return "", "", fmt.Errorf("reading config for Codex hook installation: %w", err)
	}
	if cfg != nil && strings.TrimSpace(os.Getenv("CODEX_HOME")) == "" && codexHooksHasShellEnvironment(cfg) {
		return "", "shell startup can change the Codex home; set CODEX_HOME explicitly or install hooks manually", nil
	}
	if codexHooksHasCustomCommand(inst) {
		return "", "cannot safely resolve a custom Codex command home; install hooks manually", nil
	}
	home := inst.ResolvedCodexHome()
	if home == "" {
		return "", "cannot resolve the Codex home; install hooks manually", nil
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Sprintf("Codex home %q is relative; install hooks manually from the session's working directory", home), nil
	}
	return filepath.Join(home, "config.toml"), "", nil
}
func codexHooksHasShellEnvironment(cfg *session.UserConfig) bool {
	return len(cfg.Shell.EnvFiles) > 0 || cfg.Shell.InitScript != "" || cfg.Shell.GetLaunchShell() || cfg.Codex.EnvFile != ""
}
func codexHooksHasCustomCommand(inst *session.Instance) bool {
	command := strings.TrimSpace(inst.Command)
	return inst.Wrapper != "" || inst.Tool != "codex" || (command != "" && command != "codex") || session.GetCodexCommand() != "codex"
}
