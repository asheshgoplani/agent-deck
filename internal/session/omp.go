package session

import (
	"fmt"
	"os"
	"path/filepath"
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

// resolveOMPCommand treats the bare built-in name as the default-command
// sentinel. Session creation surfaces persist that name, so consulting the
// configured command only for an empty string would silently ignore
// [omp].command. Any other stored command is an explicit per-session override.
func resolveOMPCommand(baseCommand string) string {
	cmd := strings.TrimSpace(baseCommand)
	if cmd == "" || cmd == "omp" {
		return GetOMPCommand()
	}
	return cmd
}

// ompArgsSuffix shell-quotes a structured OMP argument list for the target.
func ompArgsSuffix(args []string) string {
	if len(args) == 0 {
		return ""
	}
	quoted := make([]string, len(args))
	for idx, arg := range args {
		quoted[idx] = shellescape.Quote(arg)
	}
	return " " + strings.Join(quoted, " ")
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
	cmd := resolveOMPCommand(baseCommand)

	sessionDir := ompAgentDeckSessionDirExpr(i.ID)
	quotedInstanceID := shellescape.Quote(i.ID)
	quotedProfile := shellescape.Quote(sessionProfileEnvValue())
	opts := i.GetOMPOptions()
	if opts == nil {
		config, _ := LoadUserConfig()
		opts = NewOMPOptions(config)
	}
	harnessSuffix := ompArgsSuffix(opts.harnessArgs())
	lifecycleSuffix := ompArgsSuffix(opts.sessionArgs())

	return envPrefix + fmt.Sprintf(
		"session_dir=%s; mkdir -p \"$session_dir\" && AGENTDECK_INSTANCE_ID=%s AGENTDECK_PROFILE=%s %s%s%s --session-dir \"$session_dir\"",
		sessionDir,
		quotedInstanceID,
		quotedProfile,
		cmd,
		harnessSuffix,
		lifecycleSuffix,
	)
}

func (i *Instance) buildOMPForkCommandForTarget(target *Instance, baseCommand string) (string, error) {
	if target == nil {
		return "", fmt.Errorf("cannot build omp fork command: target instance is nil")
	}
	if !i.CanForkOMP() {
		return "", fmt.Errorf("cannot fork: no Agent Deck omp session directory")
	}

	envPrefix := target.buildEnvSourceCommand()
	cmd := resolveOMPCommand(baseCommand)

	parentSessionDir := ompAgentDeckSessionDirExpr(i.ID)
	sessionDir := ompAgentDeckSessionDirExpr(target.ID)
	quotedInstanceID := shellescape.Quote(target.ID)
	quotedProfile := shellescape.Quote(sessionProfileEnvValue())
	opts := target.GetOMPOptions()
	if opts == nil {
		config, _ := LoadUserConfig()
		opts = NewOMPOptions(config)
	}
	argsSuffix := ompArgsSuffix(opts.harnessArgs())

	return envPrefix + fmt.Sprintf(
		"parent_session_dir=%s; session_dir=%s; mkdir -p \"$session_dir\" && source_file=$(find \"$parent_session_dir\" -type f -name '*.jsonl' -exec ls -t {} + 2>/dev/null | head -n 1); if [ -z \"$source_file\" ]; then echo \"No omp session file found in $parent_session_dir\" >&2; exit 1; fi; AGENTDECK_INSTANCE_ID=%s AGENTDECK_PROFILE=%s %s --fork \"$source_file\" --session-dir \"$session_dir\"%s",
		parentSessionDir,
		sessionDir,
		quotedInstanceID,
		quotedProfile,
		cmd,
		argsSuffix,
	), nil
}

// CanForkOMP returns true if this omp session can be forked by Agent Deck.
func (i *Instance) CanForkOMP() bool {
	if i.Tool != "omp" || i.ID == "" {
		return false
	}
	// For local non-sandboxed omp sessions, require an actual source JSONL so
	// CLI/TUI fork attempts fail before creating an immediately-dead child tmux
	// pane. Remote/sandboxed sessions use target-side $HOME, which this process
	// cannot inspect, so the launch command performs the runtime validation.
	if i.SSHHost == "" && !i.IsSandboxed() {
		return i.hasLocalOMPSessionFile()
	}
	return true
}

func (i *Instance) hasLocalOMPSessionFile() bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	sessionDir := filepath.Join(home, ".omp", "agent-deck", i.ID)
	found := false
	_ = filepath.WalkDir(sessionDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".jsonl") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// CreateForkedOMPInstanceWithOptions creates a new Instance configured for
// forking an omp session. The opts parameter is accepted for the shared fork
// worktree flow; only WorkDir and Worktree* fields are consumed for omp.
func (i *Instance) CreateForkedOMPInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *ClaudeOptions,
) (*Instance, string, error) {
	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}

	forked := NewInstance(newTitle, projectPath)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.Tool = "omp"
	forked.Wrapper = i.Wrapper

	baseCommand := resolveOMPCommand(i.Command)
	forked.Command = baseCommand
	if parentOpts := i.GetOMPOptions(); parentOpts != nil {
		copied := *parentOpts
		copied.Models = append([]string(nil), parentOpts.Models...)
		if err := forked.SetOMPOptions(&copied); err != nil {
			return nil, "", err
		}
	}

	cmd, err := i.buildOMPForkCommandForTarget(forked, baseCommand)
	if err != nil {
		return nil, "", err
	}
	forked.ForkStartCommand = cmd
	forked.IsForkAwaitingStart = true

	if opts != nil && opts.WorktreePath != "" {
		forked.WorktreePath = opts.WorktreePath
		forked.WorktreeRepoRoot = opts.WorktreeRepoRoot
		forked.WorktreeBranch = opts.WorktreeBranch
	}

	return forked, cmd, nil
}
