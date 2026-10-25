package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
)

func (i *Instance) buildPiForkCommandForTarget(target *Instance, baseCommand string) (string, error) {
	if target == nil {
		return "", fmt.Errorf("cannot build Pi fork command: target instance is nil")
	}
	if !i.CanForkPi() {
		return "", fmt.Errorf("cannot fork: no Agent Deck Pi session directory")
	}

	envPrefix := target.buildEnvSourceCommand()
	cmd := strings.TrimSpace(baseCommand)
	if cmd == "" {
		cmd = "pi"
	}

	parentSessionDir := piAgentDeckSessionDirExpr(i.ID)
	sessionDir := piAgentDeckSessionDirExpr(target.ID)
	quotedInstanceID := shellescape.Quote(target.ID)
	quotedProfile := shellescape.Quote(sessionProfileEnvValue())

	return envPrefix + fmt.Sprintf(
		"parent_session_dir=%s; session_dir=%s; mkdir -p \"$session_dir\" && source_file=$(find \"$parent_session_dir\" -type f -name '*.jsonl' -exec ls -t {} + 2>/dev/null | head -n 1); if [ -z \"$source_file\" ]; then echo \"No Pi session file found in $parent_session_dir\" >&2; exit 1; fi; AGENTDECK_INSTANCE_ID=%s AGENTDECK_PROFILE=%s %s --fork \"$source_file\" --session-dir \"$session_dir\"%s",
		parentSessionDir,
		sessionDir,
		quotedInstanceID,
		quotedProfile,
		cmd,
		target.piIdentityFlag(),
	), nil
}

// SupportsNativeFork reports whether tool has a concrete native fork
// dispatcher. Keep CLI/API capability gates routed through this helper so a
// newly supported tool cannot be admitted by Instance.CanFork while remaining
// rejected or hidden at another surface.
func SupportsNativeFork(tool string) bool {
	tool = CanonicalToolName(tool)
	return IsClaudeCompatible(tool) || tool == "pi" || tool == "opencode" ||
		IsCodexCompatible(tool) || tool == "omp"
}

// CanFork returns true if this session currently has the source state needed
// by its native fork implementation.
func (i *Instance) CanFork() bool {
	// Gemini CLI doesn't support forking
	if i.Tool == "gemini" {
		return false
	}

	// DeepSeek Harness 0.1.0-rc.6 has no fork/branch command: the launcher owns
	// --profile/--patch/the dumps, and neither shipped app exposes one. Falling
	// through to the Claude branch below would also return false, but only by
	// accident (empty ClaudeSessionID) — say it explicitly so a future Claude
	// change cannot silently start offering a fork dsh cannot perform.
	if i.Tool == "deepseek" {
		return false
	}

	// OpenCode sessions can fork if session ID is recent
	if i.Tool == "opencode" {
		return i.CanForkOpenCode()
	}

	// Pi sessions fork by source JSONL path under Agent Deck's per-instance
	// Pi session directory. The launch command validates that a JSONL exists.
	if i.Tool == "pi" {
		return i.CanForkPi()
	}

	// omp sessions fork by source JSONL path under Agent Deck's per-instance
	// omp session directory, identical shape to Pi's fork gate.
	if i.Tool == "omp" {
		return i.CanForkOMP()
	}

	// Codex-compatible sessions fork via `codex fork <sid>`, gated on a
	// flushed on-disk rollout (same invariant as `codex resume`).
	if IsCodexCompatible(i.Tool) {
		return i.CanForkCodex()
	}

	// Claude sessions can fork if session ID is recent
	if i.ClaudeSessionID == "" {
		return false
	}
	return time.Since(i.ClaudeDetectedAt) < 5*time.Minute
}

// CanForkPi returns true if this Pi session can be forked by Agent Deck.
func (i *Instance) CanForkPi() bool {
	if i.Tool != "pi" || i.ID == "" {
		return false
	}
	// For local non-sandboxed Pi sessions, require an actual source JSONL so
	// CLI/TUI fork attempts fail before creating an immediately-dead child tmux
	// pane. Remote/sandboxed sessions use target-side $HOME, which this process
	// cannot inspect, so the launch command performs the runtime validation.
	if i.SSHHost == "" && !i.IsSandboxed() {
		return i.hasLocalPiSessionFile()
	}
	return true
}

func (i *Instance) hasLocalPiSessionFile() bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	sessionDir := sandboxguard.MustHome("Pi instance session dir (~/.pi/agent-deck)", filepath.Join(home, ".pi", "agent-deck", i.ID))
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

// Fork returns the command to create a forked Claude session
// Uses capture-resume pattern: starts fork in print mode to get new session ID,
// stores in tmux environment, then resumes interactively
// Deprecated: Use ForkWithOptions instead
func (i *Instance) Fork(newTitle, newGroupPath string) (string, error) {
	return i.ForkWithOptions(newTitle, newGroupPath, nil)
}

// ForkWithOptions returns the command to create a forked Claude session with custom options
// Uses capture-resume pattern: starts fork in print mode to get new session ID,
// stores in tmux environment, then resumes interactively
func (i *Instance) ForkWithOptions(newTitle, newGroupPath string, opts *ClaudeOptions) (string, error) {
	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}
	target := NewInstance(newTitle, projectPath)
	target.Account = i.Account
	if newGroupPath != "" {
		target.GroupPath = newGroupPath
	} else {
		target.GroupPath = i.GroupPath
	}
	target.tmuxSession.SetGroupPath(target.GroupPath)
	target.Tool = "claude"

	return i.buildClaudeForkCommandForTarget(target, opts)
}

func (i *Instance) buildClaudeForkCommandForTarget(target *Instance, opts *ClaudeOptions) (string, error) {
	if target == nil {
		return "", fmt.Errorf("cannot build fork command: target instance is nil")
	}

	if !i.CanFork() {
		return "", fmt.Errorf("cannot fork: no active Claude session")
	}

	// #1815: a fork is a `--resume <source id> --fork-session`, so it needs
	// the same identity verification as any other resume — forking a
	// transcript this session does not own would clone another session's
	// context (and its authority) into a new pane.
	if decision := i.resumeIdentityAllowed(i.ClaudeSessionID); !decision.Allow {
		i.logResumeRefusal(i.ClaudeSessionID, "fork_"+decision.Reason)
		return "", fmt.Errorf("cannot fork: conversation id %q is not verified as this session's own (%s)",
			i.ClaudeSessionID, decision.Reason)
	}

	workDir := target.ProjectPath

	// IMPORTANT: For capture-resume commands (which contain $(...) syntax), we MUST use
	// "claude" binary + explicit env exports, NOT a custom command alias like "cdw".
	// Reason: Commands with $(...) get wrapped in `bash -c` for fish compatibility (#47),
	// and shell aliases are not available in non-interactive bash shells. This always
	// execs the literal "claude" binary below regardless of GetClaudeCommandForInstance,
	// so the #1822 F3 custom-command gate does not apply here: pass false.
	bashExportPrefix := target.buildBashExportPrefix(false)

	// If no options provided, use defaults from config
	if opts == nil {
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}

	// Build extra flags from options (for fork, we use ToArgsForFork which excludes session mode)
	launchName := target.ClaudeLaunchName()
	if extraArgsSupplyName(i.ExtraArgs) || extraArgsSelectSession(i.ExtraArgs) {
		launchName = ""
	}
	extraFlags := i.buildClaudeExtraFlagsWithName(opts, launchName)
	// The flags above are the parent's; the fork must carry its OWN identity
	// block (its id, title and parent), not the parent's, and an SSH or
	// sandboxed parent's inline flag means nothing in the local fork.
	if parentFlag := i.claudeIdentityFlag(); parentFlag != "" {
		extraFlags = strings.Replace(extraFlags, " "+parentFlag, "", 1)
	}
	if targetFlag := target.claudeIdentityFlag(); targetFlag != "" {
		extraFlags += " " + targetFlag
	}
	// An SSH/sandboxed fork's inline flag expands the block variable that
	// its env export sets; the fork command has no env prefix of its own.
	if target.identityRunsElsewhere() {
		if export := target.identityEnvExport(); export != "" {
			bashExportPrefix = export + " && " + bashExportPrefix
		}
	}

	// Pre-generate UUID for forked session to avoid shell uuidgen dependency.
	// CLAUDE_SESSION_ID is propagated via host-side SetEnvironment after tmux start.
	// Use `exec` before claude so that when this compound command is wrapped
	// in `bash -c` (for fish compatibility), claude replaces the bash process,
	// enabling proper job control (Ctrl+Z suspend / fg resume).
	forkUUID := generateUUID()
	target.ClaudeSessionID = forkUUID
	// #1815: minted for the fork target — vouched ownership.
	target.markClaudeSessionIDVerified()
	// LOC-7: workDir (a project or branch-derived worktree path) and the parent
	// id are quoted, not spliced into the shell: a single quote in either used
	// to break out and run shell. shellQuote keeps the `cd '<path>'` form for
	// ordinary paths; shellescape leaves an ordinary id bare.
	cmd := fmt.Sprintf(
		`cd %s && `+
			`%sexec claude --session-id "%s" --resume %s --fork-session%s`,
		shellQuote(workDir),
		bashExportPrefix, forkUUID, shellescape.Quote(i.ClaudeSessionID), extraFlags)
	cmd, err := i.applyWrapper(cmd)
	if err != nil {
		return "", err
	}

	return cmd, nil
}

// GetActualWorkDir returns the actual working directory from tmux, or falls back to ProjectPath
func (i *Instance) GetActualWorkDir() string {
	if i.tmuxSession != nil {
		if workDir := i.tmuxSession.GetWorkDir(); workDir != "" {
			return workDir
		}
	}
	return i.ProjectPath
}

// CreateForkedInstance creates a new Instance configured for forking
// Deprecated: Use CreateForkedInstanceWithOptions instead
func (i *Instance) CreateForkedInstance(newTitle, newGroupPath string) (*Instance, string, error) {
	return i.CreateForkedInstanceWithOptions(newTitle, newGroupPath, nil)
}

// CreateForkedInstanceWithOptions creates a new Instance configured for forking with custom options
func (i *Instance) CreateForkedInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *ClaudeOptions,
) (*Instance, string, error) {
	return i.createForkedClaudeInstance(newTitle, newGroupPath, opts, ForkParent{})
}

// ForkParent names the session a fork is nested under. The zero value is a
// root fork.
type ForkParent struct {
	SessionID   string
	ProjectPath string
}

func (p ForkParent) applyTo(inst *Instance) {
	if p.SessionID != "" {
		inst.SetParentWithPath(p.SessionID, p.ProjectPath)
	}
}

// inheritForkModel gives a fork the source session's own model (one picked
// with `session command /model` or `session set model`) unless the fork was
// given a model explicitly (ClaudeOptions.ModelExplicit), so a fork does not
// revert to the launch or default model. A Model filled from
// [claude].default_model (NewClaudeOptions, as `session fork -w` builds its
// options) is not explicit. opts is copied, never modified.
func inheritForkModel(source *Instance, opts *ClaudeOptions) *ClaudeOptions {
	model := source.LaunchModelID()
	if model == "" || (opts != nil && opts.ModelExplicit && strings.TrimSpace(opts.Model) != "") {
		return opts
	}
	var out ClaudeOptions
	if opts != nil {
		out = *opts
	} else {
		userConfig, _ := LoadUserConfig()
		out = *NewClaudeOptions(userConfig)
	}
	out.Model = model
	return &out
}

func (i *Instance) createForkedClaudeInstance(
	newTitle, newGroupPath string,
	opts *ClaudeOptions,
	parent ForkParent,
) (*Instance, string, error) {
	// Create new instance - use worktree path if provided, otherwise parent's project path
	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}
	forked := NewInstance(newTitle, projectPath)
	forked.Account = i.Account
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.tmuxSession.SetGroupPath(forked.GroupPath)
	forked.Tool = "claude"
	if IsClaudeCompatible(i.Tool) {
		forked.Tool = i.Tool
	}
	forked.Wrapper = i.Wrapper

	// #1407: persist the parent's ExtraArgs onto the fork record. The baked
	// one-shot fork command below inherits them implicitly via the builder
	// (which reads the PARENT's ExtraArgs), but without persisting them on
	// the fork they silently drop on the fork's first restart
	// (buildClaudeResumeCommand reads the fork's own ExtraArgs) and a
	// fork-of-a-fork never sees them at all. Mirrors how ClaudeOptions are
	// persisted via SetClaudeOptions further down. Copied, not aliased.
	if len(i.ExtraArgs) > 0 {
		forked.ExtraArgs = append([]string(nil), i.ExtraArgs...)
	}

	// The parent is set before the one-shot command is baked: the launch env
	// depends on it (a parented Claude session starts with prompt
	// suggestions off), and Start() runs the baked command verbatim.
	parent.applyTo(forked)

	opts = inheritForkModel(i, opts)
	cmd, err := i.buildClaudeForkCommandForTarget(forked, opts)
	if err != nil {
		return nil, "", err
	}
	forked.Command = cmd
	// #745: flag Start() to run cmd verbatim. Without this, Start() rebuilds
	// the command through buildClaudeResumeCommand and silently drops
	// --resume <parent-id> / --fork-session because the brand-new fork UUID
	// has no JSONL on disk yet.
	forked.IsForkAwaitingStart = true

	// Store options in the new instance for persistence
	if opts != nil {
		if err := forked.SetClaudeOptions(opts); err != nil {
			// Log but don't fail - options are not critical for fork
			sessionLog.Warn("set_claude_options_failed", slog.String("error", err.Error()))
		}
		// Copy transient worktree fields to the forked instance
		if opts.WorktreePath != "" {
			forked.WorktreePath = opts.WorktreePath
			forked.WorktreeRepoRoot = opts.WorktreeRepoRoot
			forked.WorktreeBranch = opts.WorktreeBranch
		}
	}

	return forked, cmd, nil
}

// CreateForkedInstanceForTool creates a forked instance using the correct
// tool-specific fork implementation. opts is the shared fork carrier for
// worktree fields; non-Claude tool options continue to come from global config.
func (i *Instance) CreateForkedInstanceForTool(newTitle, newGroupPath string, opts *ClaudeOptions) (*Instance, string, error) {
	return i.CreateForkedInstanceForToolUnder(newTitle, newGroupPath, opts, ForkParent{})
}

// CreateForkedInstanceForToolUnder is CreateForkedInstanceForTool for a fork
// nested under parent. A Claude fork gets the parent before its one-shot
// command is built, so its first start already carries the parented launch
// env; the other tools' fork commands do not depend on the parent.
func (i *Instance) CreateForkedInstanceForToolUnder(newTitle, newGroupPath string, opts *ClaudeOptions, parent ForkParent) (*Instance, string, error) {
	var (
		forked *Instance
		cmd    string
		err    error
	)
	switch {
	case i.Tool == "opencode":
		workDir := i.ProjectPath
		repoRoot := ""
		branch := ""
		if opts != nil && opts.WorkDir != "" {
			workDir = opts.WorkDir
			repoRoot = opts.WorktreeRepoRoot
			branch = opts.WorktreeBranch
		}
		forked, cmd, err = i.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(newTitle, newGroupPath, nil, workDir, repoRoot, branch)
	case i.Tool == "pi":
		forked, cmd, err = i.CreateForkedPiInstanceWithOptions(newTitle, newGroupPath, opts)
	case i.Tool == "omp":
		forked, cmd, err = i.CreateForkedOMPInstanceWithOptions(newTitle, newGroupPath, opts)
	case IsCodexCompatible(i.Tool):
		forked, cmd, err = i.CreateForkedCodexInstanceWithOptions(newTitle, newGroupPath, opts)
	default:
		return i.createForkedClaudeInstance(newTitle, newGroupPath, opts, parent)
	}
	if err == nil {
		parent.applyTo(forked)
	}
	return forked, cmd, err
}

// CreateForkedPiInstance creates a new Instance configured for forking a Pi session.
// Deprecated: Use CreateForkedPiInstanceWithOptions instead.
func (i *Instance) CreateForkedPiInstance(newTitle, newGroupPath string) (*Instance, string, error) {
	return i.CreateForkedPiInstanceWithOptions(newTitle, newGroupPath, nil)
}

// CreateForkedPiInstanceWithOptions creates a new Instance configured for forking a Pi session.
// The opts parameter is accepted for the shared fork worktree flow; only WorkDir
// and Worktree* fields are consumed for Pi.
func (i *Instance) CreateForkedPiInstanceWithOptions(
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
	forked.tmuxSession.SetGroupPath(forked.GroupPath)
	forked.Tool = "pi"
	forked.Wrapper = i.Wrapper

	baseCommand := strings.TrimSpace(i.Command)
	if baseCommand == "" {
		baseCommand = "pi"
	}
	forked.Command = baseCommand

	cmd, err := i.buildPiForkCommandForTarget(forked, baseCommand)
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

// CanForkCodex reports whether this Codex session can be forked. Forkability
// requires a flushed on-disk rollout for the captured session id — the same
// invariant buildCodexCommand uses to gate `codex resume` (#756). `codex fork`
// is a newer codex CLI subcommand; if the installed binary predates it the
// launched command fails into a recoverable error state.
func (i *Instance) CanForkCodex() bool {
	if !IsCodexCompatible(i.Tool) || i.CodexSessionID == "" {
		return false
	}
	sessionID, err := normalizeToolSessionID(FieldCodexSessionID, i.CodexSessionID)
	return err == nil && sessionID != "" && sessionID == strings.TrimSpace(i.CodexSessionID) && codexRolloutExistsInHome(sessionID, i.getCodexHomeDir())
}

// buildCodexForkCommandForTarget builds the one-time `codex fork <parent-sid>`
// launch command for a forked codex instance. Mirrors buildCodexCommand's resume
// path (instance.go:1374) but uses `fork`, which clones the parent transcript into
// a new thread with a fresh id while leaving the parent intact.
func (i *Instance) buildCodexForkCommandForTarget(target *Instance, baseCommand string) (string, error) {
	if !i.CanForkCodex() {
		return "", fmt.Errorf("cannot fork: no resumable Codex session")
	}
	envPrefix := target.buildEnvSourceCommand()
	// Shell-quote the injected env values: target.Title is user-editable and could
	// contain shell metacharacters (e.g. $(...) or backticks), and custom Codex-tool
	// identities are config-defined — keep the generated fork command injection-safe.
	envPrefix += fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%s AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ",
		shellescape.Quote(target.ID), shellescape.Quote(target.Title), shellescape.Quote(target.Tool), shellescape.Quote(sessionProfileEnvValue()))
	yoloFlag := target.resolveCodexYoloFlag()
	modelFlag := target.resolveCodexModelFlag()
	reasoningFlag := target.resolveCodexReasoningEffortFlag()
	command := target.resolveCodexCommand(baseCommand)
	if codexHome := target.codexHomeToExport(); codexHome != "" {
		if err := os.MkdirAll(codexHome, 0o755); err != nil {
			sessionLog.Warn("codex_home_mkdir_failed",
				slog.String("path", codexHome),
				slog.String("error", err.Error()))
		}
		envPrefix += "CODEX_HOME=" + shellescape.Quote(codexHome) + " "
	}
	return envPrefix + fmt.Sprintf("%s%s%s%s fork %s", command, yoloFlag, modelFlag, reasoningFlag, shellescape.Quote(i.CodexSessionID)), nil
}

// CreateForkedCodexInstanceWithOptions creates a forked Codex instance. Mirrors
// CreateForkedPiInstanceWithOptions: opts is the shared worktree carrier (only
// WorkDir/Worktree* consumed); launch is deferred via ForkStartCommand.
func (i *Instance) CreateForkedCodexInstanceWithOptions(
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
	forked.tmuxSession.SetGroupPath(forked.GroupPath)
	forked.Tool = i.Tool
	forked.Wrapper = i.Wrapper
	// #1929: a fork runs against the parent's thread, so it must run under the
	// parent's account. Without this the fork resolves its own codex home
	// through the default chain and `codex fork <parent-sid>` cannot find the
	// rollout, which lives under the account's home — the fork starts empty and
	// the account selection is lost from the record too.
	forked.Account = i.Account

	baseCommand := strings.TrimSpace(i.Command)
	if baseCommand == "" {
		baseCommand = "codex"
	}
	forked.Command = baseCommand
	if parentOpts := i.GetCodexOptions(); parentOpts != nil && parentOpts.ReasoningEffort != "" {
		forkOpts := &CodexOptions{ReasoningEffort: parentOpts.ReasoningEffort}
		if err := forked.SetCodexOptions(forkOpts); err != nil {
			return nil, "", fmt.Errorf("persist fork reasoning effort: %w", err)
		}
	}

	cmd, err := i.buildCodexForkCommandForTarget(forked, baseCommand)
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
