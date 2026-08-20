package session

import (
	"fmt"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

// Oh My Pi (`omp`) adapter.
//
// github.com/can1357/oh-my-pi (npm @oh-my-pi/pi-coding-agent, MIT) is a fork
// of badlogic/pi-mono's "Pi", rewritten as a batteries-included coding-agent
// CLI. The binary is `omp`:
//
//	omp                                  # interactive TUI
//	omp -p "prompt"                      # non-interactive, prints and exits
//	omp --session-dir <dir> --continue   # resume/start the session in <dir>
//	omp --session-dir <dir> --fork <src.jsonl>  # fork <src.jsonl> into <dir>
//	omp --approval-mode always-ask|write|yolo   # permission gating
//
// Verified LIVE against the real installed binary (v17.3.8) via a PTY:
// `--session-dir <dir> --continue` on a brand-new empty <dir> starts a fresh
// session there; on a populated <dir> it resumes with full context; and
// `--session-dir <newdir> --fork <path>` forks a source JSONL's context into
// a new directory. This is a materially different on-disk layout from omp's
// own DEFAULT session store (`~/.omp/agent/sessions/<encoded-cwd>/...`),
// but --session-dir overrides it to a flat, single-purpose directory —
// exactly the same shape agent-deck already relies on for the unrelated
// "pi" tool. See docs/superpowers/plans/2026-08-20-oh-my-pi-native-support.md
// for the full evidence trail.
//
// agent-deck scopes omp's session directory to the Agent Deck instance
// (${HOME}/.omp/agent-deck/<instance-id>, a sibling of omp's own
// ~/.omp/agent/ root — never inside it, to avoid colliding with omp's
// default per-cwd session buckets) and always launches with --continue so
// restarts resume that instance without colliding with other Agent Deck omp
// sessions in the same project.

// ompAgentDeckSessionDirExpr returns a target-shell expression for the omp
// session directory Agent Deck owns for an instance. Uses target-side $HOME
// rather than resolving the Agent Deck process' home directory, keeping
// local, SSH, and sandbox launch paths consistent (mirrors
// piAgentDeckSessionDirExpr).
func ompAgentDeckSessionDirExpr(instanceID string) string {
	return "${HOME}/.omp/agent-deck/" + shellescape.Quote(instanceID)
}

// GetOMPCommand returns the configured omp command/alias.
// Mirrors GetCrushCommand: prefer the user config override, fall back to
// the bare binary name.
func GetOMPCommand() string {
	userConfig, _ := LoadUserConfig()
	if userConfig != nil && strings.TrimSpace(userConfig.OMP.Command) != "" {
		return strings.TrimSpace(userConfig.OMP.Command)
	}
	return "omp"
}

// ompApprovalModeFlag returns the ` --approval-mode <value>` suffix for the
// configured [omp].approval_mode, or "" when unset.
func ompApprovalModeFlag() string {
	config, _ := LoadUserConfig()
	if config == nil {
		return ""
	}
	mode := strings.TrimSpace(config.OMP.ApprovalMode)
	if mode == "" {
		return ""
	}
	return " --approval-mode " + shellescape.Quote(mode)
}

// buildOMPCommand builds the command for the Oh My Pi CLI.
// omp sessions are JSONL files, not externally named sessions like
// Claude/Codex. Scope omp's session directory to the Agent Deck instance and
// always launch with --continue so restarts resume that instance without
// colliding with other Agent Deck omp sessions in the same project.
func (i *Instance) buildOMPCommand(baseCommand string) string {
	if i.Tool != "omp" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()
	cmd := strings.TrimSpace(baseCommand)
	if cmd == "" {
		cmd = GetOMPCommand()
	}

	sessionDir := ompAgentDeckSessionDirExpr(i.ID)
	quotedInstanceID := shellescape.Quote(i.ID)
	quotedProfile := shellescape.Quote(sessionProfileEnvValue())

	return envPrefix + fmt.Sprintf(
		"session_dir=%s; mkdir -p \"$session_dir\" && AGENTDECK_INSTANCE_ID=%s AGENTDECK_PROFILE=%s %s --continue --session-dir \"$session_dir\"%s",
		sessionDir,
		quotedInstanceID,
		quotedProfile,
		cmd,
		ompApprovalModeFlag(),
	)
}
