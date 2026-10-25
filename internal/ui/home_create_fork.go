package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/jujutsu"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
	"github.com/asheshgoplani/agent-deck/internal/vcsbackend"
)

// getDefaultPathForGroup returns the default path for a group
// Returns empty string if group not found or no default path set
func (h *Home) getDefaultPathForGroup(groupPath string) string {
	if h.groupTree == nil {
		return ""
	}
	// Only use stored path if it still exists on disk.
	// Stale paths (e.g. deleted worktrees) would cause the
	// new-session dialog to prompt for directory creation.
	p := h.groupTree.DefaultPathForGroup(groupPath)
	if p != "" {
		if _, err := os.Stat(p); err != nil {
			return ""
		}
	}
	return p
}

// createSessionInGroupWithWorktreeAndOptions creates a new session with full options including YOLO mode, sandbox, and tool options.
func (h *Home) createSessionInGroupWithWorktreeAndOptions(
	name, path, tool, command, groupPath, worktreePath, worktreeRepoRoot, worktreeBranch string,
	geminiYoloMode bool,
	sandboxEnabled bool,
	toolOptionsJSON json.RawMessage,
	claudeExtraArgs []string,
	claudeStartQuery string,
	claudeAccount string,
	launchModelID string,
	multiRepoEnabled bool,
	additionalPaths []string,
	parentSessionID, parentProjectPath string,
	tempID string,
	autoName bool,
) tea.Cmd {
	return func() tea.Msg {
		var setupWarning string // non-fatal worktree setup-script failure, if any

		uiLog.Info("create_session_start",
			slog.String("name", name),
			slog.String("path", path),
			slog.String("worktree_branch", worktreeBranch),
			slog.String("temp_id", tempID))

		// Check tmux availability before creating session
		if err := tmux.IsTmuxAvailable(); err != nil {
			return sessionCreatedMsg{err: fmt.Errorf("cannot create session: %w", err), tempID: tempID}
		}

		var worktreeBackend vcs.Backend
		if worktreePath != "" && worktreeRepoRoot != "" && worktreeBranch != "" && !multiRepoEnabled {
			// Single-repo worktree: create here. Multi-repo worktrees are handled below.
			//
			// Detect the VCS so jj repos get `jj workspace add` instead of `git worktree add`.
			backend, err := vcsbackend.Detect(worktreeRepoRoot)
			if err != nil {
				return sessionCreatedMsg{err: fmt.Errorf("failed to detect VCS: %w", err), tempID: tempID}
			}
			worktreeBackend = backend

			// Check for an existing worktree for this branch before creating a new one.
			if existingPath, err := backend.GetWorktreeForBranch(worktreeBranch); err == nil && existingPath != "" {
				uiLog.Info("worktree_reuse", slog.String("branch", worktreeBranch), slog.String("path", existingPath))
				worktreePath = existingPath
			} else {
				if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to create parent directory: %w", err), tempID: tempID}
				}
				setupErr, err := createWorktreeWithSetupAndLog(backend, worktreePath, worktreeBranch, path)
				if err != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to create worktree: %w", err), tempID: tempID}
				}
				if setupErr != nil {
					setupWarning = formatSetupWarning(setupErr)
				}
			}
			path = worktreePath
		}

		if tool == "" {
			tool, command = createSessionTool(command)
		}

		var inst *session.Instance
		if groupPath != "" {
			inst = session.NewInstanceWithGroupAndTool(name, path, groupPath, tool)
		} else {
			inst = session.NewInstanceWithTool(name, path, tool)
		}
		inst.Command = command
		inst.SetAutoName(autoName) // quick-create paths pass true; see render substitution

		// A title the user typed into the full create dialog (autoName=false) is
		// explicit intent, so lock it against the Claude session-name sync (#572).
		// Otherwise the next hook event or attach reconcile would overwrite it with
		// Claude's cwd-derived name — for a worktree that is the "<repo>-<branch>"
		// folder basename (e.g. "linqalpha-e7"), silently renaming the session.
		if !autoName && strings.TrimSpace(name) != "" {
			inst.TitleLocked = true
		}

		// Set worktree fields if provided
		if worktreePath != "" {
			inst.WorktreePath = worktreePath
			inst.WorktreeRepoRoot = worktreeRepoRoot
			inst.WorktreeBranch = worktreeBranch
			if worktreeBackend != nil {
				inst.WorktreeType = string(worktreeBackend.Type())
			}
		}

		applyCreateSessionToolOverrides(inst, tool, geminiYoloMode)

		// Apply generic tool options (claude, codex, etc.)
		if len(toolOptionsJSON) > 0 {
			inst.ToolOptionsJSON = toolOptionsJSON
		}

		// #1815: an operator who typed a conversation UUID into the "resume by
		// session ID" panel field made an explicit ownership declaration for
		// THIS session, exactly like --resume-session on the CLI (see
		// launch_cmd.go / main.go). Vouch for it here too, or the chokepoint
		// sees no recorded ClaudeSessionID (only opts.ResumeSessionID inside
		// ToolOptionsJSON, which canResumeClaudeSession never reads) and
		// silently mints a fresh id instead of resuming the one the operator
		// picked (review finding on #1830).
		//
		// The field is free-text (internal/ui/claudeoptions.go's resumeIDInput
		// applies no validation), so a well-formed-UUID check is required
		// before trusting it as an ownership declaration: without it, a value
		// containing shell metacharacters would still be vouched as verified
		// here and later reach the unquoted `--resume %s` command build
		// (review finding on #1830). A malformed value is left unassigned and
		// falls through to the normal fresh-id path instead.
		if tool == "claude" && len(toolOptionsJSON) > 0 {
			if opts, err := session.UnmarshalClaudeOptions(toolOptionsJSON); err == nil && opts != nil &&
				opts.SessionMode == "resume" {
				if candidate := strings.TrimSpace(opts.ResumeSessionID); candidate != "" && session.IsBareClaudeSessionUUID(candidate) {
					inst.ClaudeSessionID = candidate
					session.MarkClaudeSessionIDVerified(inst)
					inst.ClaudeDetectedAt = time.Now()
				}
			}
		}

		if launchModelID != "" {
			if err := inst.ApplyLaunchModel(launchModelID); err != nil {
				return sessionCreatedMsg{err: fmt.Errorf("failed to apply model override: %w", err), tempID: tempID}
			}
		}

		// Apply claude extra CLI tokens (claude-only, ignored for other tools).
		if tool == "claude" && len(claudeExtraArgs) > 0 {
			inst.ExtraArgs = claudeExtraArgs
		}

		// Apply claude startup query (claude-only, per-session, not
		// persisted — v1.7.67, #725). buildClaudeCommand emits this as a
		// single shell-quoted positional arg on the new-session command.
		if tool == "claude" && claudeStartQuery != "" {
			inst.StartupQuery = claudeStartQuery
		}

		// Apply the named account slot (#924). The Claude config-dir resolver
		// reads Instance.Account as the most specific level of the chain, so a
		// session created under an account spawns on that login's auth. A new
		// session has no conversation yet, so there is nothing to migrate —
		// changing the slot LATER goes through session.SwitchAccount.
		if claudeAccount != "" && session.IsClaudeCompatible(tool) {
			inst.Account = claudeAccount
		}

		// Apply sandbox config.
		if sandboxEnabled {
			inst.Sandbox = session.NewSandboxConfig("")
		}

		// Apply multi-repo config.
		if multiRepoEnabled && len(additionalPaths) > 0 {
			inst.MultiRepoEnabled = true
			inst.AdditionalPaths = additionalPaths
			allPaths := inst.AllProjectPaths()

			if worktreeBranch != "" {
				// Multi-repo + worktree: create a persistent parent dir with all worktrees inside.
				// Layout: <effective-data-dir>/multi-repo-worktrees/<branch>-<id>/<repo-name>/
				sanitizedBranch := strings.ReplaceAll(worktreeBranch, "/", "-")
				sanitizedBranch = strings.ReplaceAll(sanitizedBranch, " ", "-")
				worktreesRoot, rootErr := multiRepoWorktreesRoot()
				if rootErr != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to resolve multi-repo worktree dir: %w", rootErr), tempID: tempID}
				}
				parentDir := filepath.Join(worktreesRoot,
					fmt.Sprintf("%s-%s", sanitizedBranch, inst.ID[:8]))
				if mkErr := os.MkdirAll(parentDir, 0o755); mkErr != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to create multi-repo worktree dir: %w", mkErr), tempID: tempID}
				}
				if resolved, evalErr := filepath.EvalSymlinks(parentDir); evalErr == nil {
					parentDir = resolved
				}
				inst.MultiRepoTempDir = parentDir

				// Resolved for inst.ProjectPath (the primary repo, per #2093)
				// so directory-local .agent-deck/config.toml overrides apply.
				wtSettings, dirLocalNotice, wtSettingsErr := session.GetWorktreeSettingsForDirWithNotice(inst.ProjectPath)
				if wtSettingsErr != nil {
					return sessionCreatedMsg{err: fmt.Errorf("invalid directory-local config: %w", wtSettingsErr), tempID: tempID}
				}
				if dirLocalNotice != "" {
					setupWarning = formatSetupWarning(&dirLocalNoticeError{notice: dirLocalNotice})
				}
				wtResult := session.CreateMultiRepoWorktreesWithOptions(allPaths, parentDir, worktreeBranch, wtSettings)
				for _, w := range wtResult.Warnings {
					uiLog.Warn("multi_repo_worktree", slog.String("detail", w))
				}
				inst.MultiRepoWorktrees = wtResult.Worktrees
				inst.ProjectPath = wtResult.MappedPaths[0]
				inst.AdditionalPaths = wtResult.MappedPaths[1:]
			} else {
				// Multi-repo without worktree: create a persistent parent dir with symlinks.
				worktreesRoot, rootErr := multiRepoWorktreesRoot()
				if rootErr != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to resolve multi-repo dir: %w", rootErr), tempID: tempID}
				}
				parentDir := filepath.Join(worktreesRoot, inst.ID[:8])
				if mkErr := os.MkdirAll(parentDir, 0o755); mkErr != nil {
					return sessionCreatedMsg{err: fmt.Errorf("failed to create multi-repo dir: %w", mkErr), tempID: tempID}
				}
				if resolved, evalErr := filepath.EvalSymlinks(parentDir); evalErr == nil {
					parentDir = resolved
				}
				inst.MultiRepoTempDir = parentDir

				// Create symlinks for all paths
				dirnames := session.DeduplicateDirnames(allPaths)
				var newProjectPath string
				var newAdditionalPaths []string
				for i, p := range allPaths {
					linkPath := filepath.Join(parentDir, dirnames[i])
					_ = os.Symlink(p, linkPath)
					if i == 0 {
						newProjectPath = linkPath
					} else {
						newAdditionalPaths = append(newAdditionalPaths, linkPath)
					}
				}
				inst.ProjectPath = newProjectPath
				inst.AdditionalPaths = newAdditionalPaths
			}

			// Update tmux session working directory to the parent dir
			if inst.GetTmuxSession() != nil {
				inst.GetTmuxSession().WorkDir = inst.MultiRepoTempDir
			}

			// Pre-accept the Claude trust dialog and emit a parent CLAUDE.md
			// describing the layout (#1149, credit @spawnia). Skips silently
			// for non-claude tools or empty repo lists. Failures are logged
			// but non-fatal — the session can still launch; user just sees
			// the usual trust prompt.
			repoNames := make([]string, 0, len(inst.AllProjectPaths()))
			for _, p := range inst.AllProjectPaths() {
				repoNames = append(repoNames, filepath.Base(p))
			}
			if ctxErr := session.ApplyMultiRepoClaudeContext(
				inst.Tool, inst.MultiRepoEnabled,
				session.GetUserMCPRootPath(), inst.MultiRepoTempDir, repoNames,
			); ctxErr != nil {
				uiLog.Warn("multi_repo_claude_context", slog.String("error", ctxErr.Error()))
			}
			if ctxErr := session.ApplyMultiRepoCodexContext(
				inst.Tool, inst.MultiRepoEnabled, inst.MultiRepoTempDir,
			); ctxErr != nil {
				uiLog.Warn("multi_repo_codex_context", slog.String("error", ctxErr.Error()))
			}
		}

		if parentSessionID != "" {
			inst.SetParentWithPath(parentSessionID, parentProjectPath)
		}

		uiLog.Info("session_create_starting",
			slog.String("tool", inst.Tool),
			slog.String("path", inst.ProjectPath),
			slog.Bool("sandbox", inst.IsSandboxed()),
		)
		if err := inst.Start(); err != nil {
			uiLog.Error("session_create_failed", slog.String("error", err.Error()))
			return sessionCreatedMsg{err: err, tempID: tempID}
		}
		inst.RecordTelemetryCreate(telemetry.ViaTUINew)
		uiLog.Info("session_create_succeeded", slog.String("id", inst.ID))
		return sessionCreatedMsg{instance: inst, tempID: tempID, setupWarning: setupWarning}
	}
}

// createWorktreeWithSetupAndLog creates a worktree via the supplied backend.
// For git backends it also runs .worktreeinclude and worktree-setup.sh; for
// jujutsu backends only the workspace is created (setup-script behavior is
// git-only per the vcsbackend convention).
//
// setupErr is the non-fatal setup-script failure (nil on success): the worktree
// is created regardless, but the caller surfaces setupErr to the user. err is
// the fatal worktree-creation error. The setup output's tail is logged here; only
// the concise setupErr is returned for display (see formatSetupWarning).
// sourceDir is the directory the session was created/forked from; when
// `[worktree] sparse_checkout = "inherit"` is set, the new worktree inherits
// ITS sparse-checkout state (#1708). backend.RepoDir() must not be used for
// that: it is the normalized base root, which carries the main worktree's
// sparsity instead of the invoking one's.
func createWorktreeWithSetupAndLog(backend vcs.Backend, wtPath, branch, sourceDir string) (setupErr error, err error) {
	var buf bytes.Buffer
	// Resolved for sourceDir (the session's target directory, per #2093) so
	// directory-local .agent-deck/config.toml overrides apply.
	wtSettings, dirLocalNotice, settingsErr := session.GetWorktreeSettingsForDirWithNotice(sourceDir)
	if settingsErr != nil {
		return nil, fmt.Errorf("invalid directory-local config: %w", settingsErr)
	}
	setupErr, err = vcsbackend.CreateWorktreeWithSetup(backend, wtPath, branch,
		wtSettings.CreateOptions(sourceDir),
		&buf, &buf, wtSettings.SetupTimeout())
	if err != nil {
		telemetry.ErrorOccurred(telemetry.AreaWorktree, telemetry.ErrKindOf(err), "")
		return nil, err
	}
	if setupErr != nil {
		uiLog.Warn("worktree_setup_script_failed", slog.String("error", setupErr.Error()), slog.String("output", setupOutputForLog(buf.String())))
		setupErr = withSetupOutputTail(setupErr, buf.String())
	}
	// A skipped untrusted dir-local config changes which settings applied;
	// the footer is the only place the TUI user sees that.
	return withDirLocalNotice(setupErr, dirLocalNotice), nil
}

// dirLocalNoticeError carries the notice that a directory-local config was
// skipped as untrusted, alongside the setup-script failure (if any), to the
// footer warning. Unwrap keeps errors.Is/As working on the setup error.
type dirLocalNoticeError struct {
	notice string
	setup  error
}

func (e *dirLocalNoticeError) Error() string {
	if e.setup == nil {
		return e.notice
	}
	return e.setup.Error() + "; " + e.notice
}

func (e *dirLocalNoticeError) Unwrap() error { return e.setup }

// setupWarningMaxLen bounds the setup-script failure text shown in the footer,
// which is height-constrained and auto-dismisses. More of the output (its last
// setupLogMaxLines lines, setupLogMaxBytes at most) is in uiLog.
const setupWarningMaxLen = 300

// formatSetupWarning turns a non-fatal setup-script error into a single bounded,
// sanitized footer line: ANSI stripped, whitespace collapsed, capped. The
// verbose output stays in the log (see createWorktreeWithSetupAndLog).
func formatSetupWarning(setupErr error) string {
	var notice *dirLocalNoticeError
	if errors.As(setupErr, &notice) {
		msg := truncateSetupWarning(strings.Join(strings.Fields(tmux.StripANSI(notice.notice)), " "))
		if notice.setup == nil {
			return msg
		}
		return formatSetupWarning(notice.setup) + " · " + msg
	}
	msg := strings.Join(strings.Fields(tmux.StripANSI(setupErr.Error())), " ")
	// Append the last lines of the script's output (#2637) so the footer says
	// why the script failed, not just "exit status 1". When the tail does not
	// fit, it is cut from the front so the final (usually most telling) line
	// survives.
	var tailed *setupScriptError
	if errors.As(setupErr, &tailed) && tailed.tail != "" && !errors.Is(setupErr, git.ErrWorktreeScriptNotApproved) {
		tail := []rune(tailed.tail)
		if budget := setupWarningMaxLen - len([]rune(msg)) - len([]rune(setupOutputSep)); budget > 1 {
			if len(tail) > budget {
				tail = append([]rune("…"), tail[len(tail)-(budget-1):]...)
			}
			msg += setupOutputSep + string(tail)
		}
	}
	msg = truncateSetupWarning(msg)
	// A hook the consent gate skipped did not fail; its message says so.
	if errors.Is(setupErr, git.ErrWorktreeScriptNotApproved) {
		return msg
	}
	return "worktree setup script failed: " + msg
}

// truncateSetupWarning caps msg at setupWarningMaxLen runes. Truncate by
// rune, not byte, so a multi-byte character is never split.
func truncateSetupWarning(msg string) string {
	if runes := []rune(msg); len(runes) > setupWarningMaxLen {
		return string(runes[:setupWarningMaxLen]) + "…"
	}
	return msg
}

// setupScriptError carries the last lines of a failed setup script's output
// alongside the error, so formatSetupWarning can show them. Error() is the
// original message (logs are unchanged); Unwrap keeps errors.Is working.
type setupScriptError struct {
	err  error
	tail string
}

func (e *setupScriptError) Error() string { return e.err.Error() }
func (e *setupScriptError) Unwrap() error { return e.err }

const (
	setupOutputTailLines = 5
	setupOutputSep       = " — "
)

// withSetupOutputTail attaches the tail of the captured script output to a
// setup error. It returns err unchanged when there is no useful output.
func withSetupOutputTail(err error, output string) error {
	if err == nil {
		return nil
	}
	tail := setupOutputTail(output, setupOutputTailLines)
	if tail == "" {
		return err
	}
	return &setupScriptError{err: err, tail: tail}
}

// setupOutputTail returns the last maxLines non-blank lines of script output,
// ANSI-stripped, with whitespace collapsed, joined by " | ". Carriage returns
// split lines too, so progress-bar redraws do not glue together. The
// "Running worktree setup script..." preamble and the trailing "Worktree setup
// script failed after ..." line are dropped: they carry nothing the warning
// does not already say.
func setupOutputTail(output string, maxLines int) string {
	clean := strings.ReplaceAll(tmux.StripANSI(output), "\r", "\n")
	var lines []string
	for _, line := range strings.Split(clean, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || line == "Running worktree setup script..." || strings.HasPrefix(line, "Worktree setup script failed after ") {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, " | ")
}

// A failed setup script's output is logged as its tail only: the last
// setupLogMaxLines lines and at most setupLogMaxBytes bytes. A setup script
// that runs npm install or a build can print megabytes, and the log is
// rotated by size, so logging all of it at Warn pushes everything else out
// (and copies whatever the script printed into the log for good). The
// error is almost always in the last lines.
const (
	setupLogMaxLines = 40
	setupLogMaxBytes = 4 << 10
)

// setupOutputForLog returns the tail of setup-script output for the log,
// prefixed with a note of how much was cut. Output within both caps is
// returned as is.
func setupOutputForLog(output string) string {
	tail := output
	cutLines := 0
	// The last setupLogMaxLines lines; a trailing newline ends the last line
	// and does not start another.
	body := strings.TrimSuffix(tail, "\n")
	if n := strings.Count(body, "\n") + 1; n > setupLogMaxLines {
		idx := len(body)
		for i := 0; i < setupLogMaxLines; i++ {
			idx = strings.LastIndexByte(body[:idx], '\n')
		}
		tail = tail[idx+1:]
		cutLines = n - setupLogMaxLines
	}
	if len(tail) > setupLogMaxBytes {
		start := len(tail) - setupLogMaxBytes
		for start < len(tail) && !utf8.RuneStart(tail[start]) {
			start++
		}
		tail = tail[start:]
	}
	if len(tail) == len(output) {
		return output
	}
	note := fmt.Sprintf("[setup output truncated: last %d of %d bytes", len(tail), len(output))
	if cutLines > 0 {
		note += fmt.Sprintf(", %d lines cut", cutLines)
	}
	return note + "]\n" + tail
}

// createSessionTool maps a free-form command to (tool, command). Built-in
// tools resolve to themselves; custom tool names from user config resolve
// to the configured binary while keeping the custom name as the tool
// identity (so [tools.<name>] config lookup works). Anything unrecognised
// falls back to a "shell" session running `command` verbatim.
func createSessionTool(command string) (string, string) {
	tool := "shell"
	switch command {
	case "claude":
		tool = "claude"
	case "gemini":
		tool = "gemini"
	case "aider":
		tool = "aider"
	case "codex":
		tool = "codex"
	case "opencode", "opencode2":
		tool = "opencode"
	case "pi":
		tool = "pi"
	case "omp":
		tool = "omp"
	case "copilot":
		tool = "copilot"
	case "crush":
		tool = "crush"
	case "muse":
		tool = "muse"
	case "cursor":
		tool = "cursor"
		command = session.GetToolCommand("cursor")
	case "hermes":
		tool = "hermes"
	default:
		if toolDef := session.GetToolDef(command); toolDef != nil {
			tool = command
			command = toolDef.Command
		}
	}
	return tool, command
}

func applyCreateSessionToolOverrides(inst *session.Instance, tool string, geminiYoloMode bool) {
	if inst == nil {
		return
	}
	if tool == "gemini" {
		inst.SetGeminiYoloMode(geminiYoloMode)
	}
}

// quickForkSpec is the resolved input set for a comprehensive quick fork.
type quickForkSpec struct {
	Title     string
	GroupPath string
	Branch    string
	Plan      session.ResolvedForkPlan
}

// quickForkInputs computes the comprehensive quick-fork spec from the source
// session and [fork] config. Pure: no side effects, no UI, no I/O — the wiring
// (Claude-opts inheritance, degradation notices, dispatch) lives in
// quickForkSession. parentSandboxed is source.IsSandboxed().
func quickForkInputs(source *session.Instance, fork session.ForkSettings, parentSandboxed bool) quickForkSpec {
	slug := git.SanitizeBranchName(strings.ToLower(strings.TrimSpace(source.Title)))
	if slug == "" {
		slug = "fork"
	}
	return quickForkSpec{
		Title:     source.Title + " (fork)",
		GroupPath: source.GroupPath,
		Branch:    fork.GetBranchPrefix() + slug,
		Plan:      fork.Resolve(parentSandboxed),
	}
}

// resolveQuickForkSpec computes the comprehensive quick-fork spec and applies the
// backend with-state gate — i.e. the exact spec quickForkSession forks from
// (modulo the downstream unique-branch bump). Kept as a seam so the `f`-path
// with-state decision is testable against real repos without the tmux/tea.Cmd
// machinery.
func resolveQuickForkSpec(source *session.Instance, fork session.ForkSettings) quickForkSpec {
	in := quickForkInputs(source, fork, source.IsSandboxed())
	return gateForkStateForBackend(in, source.ProjectPath)
}

// gateForkStateForBackend disables with-state materialization when the source
// repo's VCS backend cannot carry working state. The comprehensive quick-fork
// default forces WithState=true; git and jujutsu both support with-state (git
// via forkWithStateWorktree, jj via forkWithStateWorkspaceJJ since #1305), so
// only a truly unsupported or undetectable backend degrades to a plain
// (workspace) fork. The worktree/workspace itself is still created either way.
func gateForkStateForBackend(in quickForkSpec, projectPath string) quickForkSpec {
	if !in.Plan.WithState {
		return in
	}
	backend, err := vcsbackend.Detect(projectPath)
	if err != nil || !backendSupportsWithState(backend.Type()) {
		in.Plan.WithState = false
		in.Plan.WithIgnored = false
	}
	return in
}

// backendSupportsWithState reports whether a VCS backend can materialize a
// parent's working state into a fork. git and jujutsu qualify; everything else
// degrades to a plain workspace fork.
func backendSupportsWithState(t vcs.Type) bool {
	return t == vcs.TypeGit || t == vcs.TypeJujutsu
}

// quickForkSession performs a comprehensive quick fork: new worktree+branch,
// carry tracked+gitignored state, match parent Docker, inherit the parent's
// Claude launch options for Claude-compatible sessions, and keep sibling
// placement. Defaults come from [fork] config (comprehensive when unset).
// Non-fatal degradation notices are reported after the async fork succeeds.
func (h *Home) quickForkSession(source *session.Instance) tea.Cmd {
	if source == nil {
		return nil
	}
	cfg, _ := session.LoadUserConfig()
	fork := session.ForkSettings{}
	if cfg != nil {
		fork = cfg.Fork
	}
	in := resolveQuickForkSpec(source, fork)

	// Repeated quick forks of the same source would otherwise collide on the
	// deterministic branch name (comprehensive fork forces a fresh worktree+branch),
	// so the second `f` would fail. Bump to fork/<slug>-2, -3, … so you can fan out
	// multiple alternative forks from one session.
	if in.Plan.Worktree {
		in.Branch = uniqueForkBranch(source.ProjectPath, in.Branch)
	}

	// Inherit the parent's persisted Claude launch options (transient worktree
	// fields are json:"-" so they are never carried over). nil falls back to
	// global config downstream, as before.
	opts := source.GetClaudeOptions()

	result := h.buildForkCmd(
		source, in.Title, in.GroupPath, in.Branch,
		forkToggles{
			Worktree:    in.Plan.Worktree,
			WithState:   in.Plan.WithState,
			WithIgnored: in.Plan.WithIgnored,
			Sandbox:     in.Plan.Sandbox,
			// ExplicitWorktree stays false: quick fork worktree is
			// config-default, not an explicit toggle (#1185). LockTitle stays
			// false: the auto-generated "<title> (fork)" name is not user
			// intent, so the #572 name sync stays enabled.
		},
		opts,
		source.ParentSessionID, source.ParentProjectPath,
	)
	if result.errMsg != "" {
		h.setError(fmt.Errorf("%s", result.errMsg))
		return nil
	}
	return result.cmd
}

// quickCreateSession creates a session instantly with auto-generated name and smart defaults.
// When the cursor is on a session, it inherits that session's path and tool settings
// (duplicate-like behavior per community feedback). When on a group header, it uses
// the group's default path and most recently created session's settings.
func (h *Home) quickCreateSession() tea.Cmd {
	groupPath := ""
	var sourceSession *session.Instance

	// Determine context from cursor position
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeSession && item.Session != nil {
			sourceSession = item.Session
			groupPath = item.Session.GroupPath
		} else if item.Type == session.ItemTypeGroup && item.Group != nil {
			groupPath = item.Group.Path
		}
	}
	if groupPath == "" {
		if h.groupScope != "" {
			groupPath = h.groupScope
		} else {
			groupPath = session.DefaultGroupPath
		}
	}

	projectPath := ""
	tool := ""
	command := ""
	var toolOptionsJSON json.RawMessage
	geminiYoloMode := false

	if sourceSession != nil {
		// Cursor on a session: inherit from THAT session (duplicate-like)
		projectPath = sourceSession.ProjectPath
		tool = sourceSession.Tool
		command = sourceSession.Command
		if len(sourceSession.ToolOptionsJSON) > 0 {
			toolOptionsJSON = session.StripResumeFields(sourceSession.ToolOptionsJSON)
		}
		if sourceSession.GeminiYoloMode != nil && *sourceSession.GeminiYoloMode {
			geminiYoloMode = true
		}
	} else {
		// Cursor on a group header: use group defaults + most recent session
		projectPath = h.getDefaultPathForGroup(groupPath)
		if projectPath == "" {
			projectPath = h.mostRecentPathInGroup(groupPath)
		}

		h.instancesMu.RLock()
		var mostRecent *session.Instance
		for _, inst := range h.instances {
			if inst.GroupPath == groupPath {
				if mostRecent == nil || inst.CreatedAt.After(mostRecent.CreatedAt) {
					mostRecent = inst
				}
			}
		}
		if mostRecent != nil {
			tool = mostRecent.Tool
			command = mostRecent.Command
			if len(mostRecent.ToolOptionsJSON) > 0 {
				toolOptionsJSON = session.StripResumeFields(mostRecent.ToolOptionsJSON)
			}
			if mostRecent.GeminiYoloMode != nil && *mostRecent.GeminiYoloMode {
				geminiYoloMode = true
			}
		}
		h.instancesMu.RUnlock()
	}

	// Fallback for path
	if projectPath == "" {
		var err error
		projectPath, err = os.Getwd()
		if err != nil {
			return func() tea.Msg {
				return sessionCreatedMsg{err: fmt.Errorf("cannot determine project path: %w", err)}
			}
		}
	}

	// Fallback for tool
	if tool == "" {
		tool = effectiveDefaultTool(session.GetDefaultTool())
	}
	if command == "" && tool != "shell" {
		if toolDef := session.GetToolDef(tool); toolDef != nil {
			command = toolDef.Command
		} else if tool == "cursor" {
			command = session.GetToolCommand("cursor")
		} else {
			command = tool
		}
	}

	// Generate unique name
	h.instancesMu.RLock()
	name := session.GenerateUniqueSessionName(h.instances, groupPath)
	h.instancesMu.RUnlock()

	return h.createSessionInGroupWithWorktreeAndOptions(
		name, projectPath, tool, command, groupPath,
		"", "", "", // no worktree
		geminiYoloMode, false, toolOptionsJSON,
		nil,        // no extra claude args (recent-session path)
		"",         // no claude startup query (recent-session path)
		"",         // no account slot (inherit)
		"",         // no explicit model override
		false, nil, // no multi-repo
		"", "", // no parent
		"",   // no placeholder
		true, // quick-create → auto-named handle
	)
}

// deriveSessionNameFromPath returns the trailing directory of projectPath as a
// session title. Falls back to a generated name for paths that have no
// meaningful basename (empty, root, or relative `.`).
func deriveSessionNameFromPath(projectPath string) string {
	base := filepath.Base(strings.TrimSpace(projectPath))
	if base == "" || base == "." || base == "/" {
		return session.GenerateSessionName()
	}
	return base
}

// ensureUniqueSessionTitle appends a numeric suffix if the preferred title
// collides with an existing instance. Dedup is global rather than per-group
// so the zoxide flow doesn't depend on post-hoc group derivation.
func ensureUniqueSessionTitle(preferred string, instances []*session.Instance) string {
	used := make(map[string]bool, len(instances))
	for _, inst := range instances {
		used[inst.Title] = true
	}
	if !used[preferred] {
		return preferred
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", preferred, i)
		if !used[candidate] {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", preferred, time.Now().Unix())
}

// pathSuggestProvider returns a closure that produces the unified,
// frecency-ranked candidate list for the create picker: recent session paths,
// group default paths and (for a non-empty query) zoxide hits, merged and
// ranked by session.PathSuggest — the single source of truth shared with the
// CLI and ADE (per NEW-SESSION-UX-PLAN.md §3.1).
func (h *Home) pathSuggestProvider() func(string) []session.PathCandidate {
	// Snapshot group default paths once per picker-open; they don't change
	// while the picker is up and re-deriving them per keystroke is wasteful.
	var groupDefaults []string
	if h.groupTree != nil {
		for groupPath := range h.groupTree.Groups {
			if dp := h.getDefaultPathForGroup(groupPath); dp != "" {
				groupDefaults = append(groupDefaults, dp)
			}
		}
	}

	return func(query string) []session.PathCandidate {
		var zox []string
		if strings.TrimSpace(query) != "" && session.ZoxideAvailable() {
			ctx, cancel := context.WithTimeout(context.Background(), zoxideQueryTimeout)
			if results, err := session.ZoxideQuery(ctx, query); err == nil {
				zox = results
			}
			cancel()
		}

		h.instancesMu.RLock()
		insts := make([]*session.Instance, len(h.instances))
		copy(insts, h.instances)
		h.instancesMu.RUnlock()

		return session.PathSuggest(session.PathSuggestInput{
			Instances:     insts,
			GroupDefaults: groupDefaults,
			Zoxide:        zox,
			Query:         query,
		})
	}
}

// groupDialogPathCandidates snapshots the ranked candidate paths for the
// group dialog's Default Path suggestions: recent session paths + group
// default paths via session.PathSuggest. Zoxide is intentionally excluded —
// the corpus is fetched once per dialog open, not per keystroke, and the
// fuzzy filter plus live filesystem completions cover discovery from there.
func (h *Home) groupDialogPathCandidates() []string {
	var groupDefaults []string
	if h.groupTree != nil {
		for groupPath := range h.groupTree.Groups {
			if dp := h.getDefaultPathForGroup(groupPath); dp != "" {
				groupDefaults = append(groupDefaults, dp)
			}
		}
	}

	h.instancesMu.RLock()
	insts := make([]*session.Instance, len(h.instances))
	copy(insts, h.instances)
	h.instancesMu.RUnlock()

	cands := session.PathSuggest(session.PathSuggestInput{
		Instances:     insts,
		GroupDefaults: groupDefaults,
		Limit:         20,
	})
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Path)
	}
	return out
}

// quickCreateSessionAt creates a session rooted at the given path with an
// auto-generated name and the user's configured default tool, bypassing
// cursor-context tool inheritance so the zoxide flow always lands on the
// user's chosen default (Claude, unless overridden in config.toml).
func (h *Home) quickCreateSessionAt(projectPath string) tea.Cmd {
	tool := effectiveDefaultTool(session.GetDefaultTool())
	var command string
	if tool == "shell" {
		command = ""
	} else {
		tool, command = createSessionTool(tool)
	}

	preferred := deriveSessionNameFromPath(projectPath)
	h.instancesMu.RLock()
	name := ensureUniqueSessionTitle(preferred, h.instances)
	h.instancesMu.RUnlock()

	return h.createSessionInGroupWithWorktreeAndOptions(
		name, projectPath, tool, command,
		"",         // empty group → creator derives from path via extractGroupPath
		"", "", "", // no worktree
		false, false, nil,
		nil, // no extra claude args
		"",  // no claude startup query
		"",  // no account slot (inherit)
		"",  // no explicit model override
		false, nil,
		"", "",
		"",
		true, // quick-create → auto-named handle
	)
}

// suggestConductorParent returns the ID of the most contextually relevant conductor
// based on the current cursor position: the cursor session itself if it's a conductor,
// or the conductor pointed to by its ParentSessionID.
func (h *Home) suggestConductorParent() string {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return ""
	}
	item := h.flatItems[h.cursor]
	if item.Type != session.ItemTypeSession || item.Session == nil {
		return ""
	}
	inst := item.Session
	// Cursor is directly on a conductor.
	if inst.IsConductor {
		return inst.ID
	}
	// Cursor is on a session that has a conductor parent.
	if inst.ParentSessionID != "" {
		h.instancesMu.RLock()
		parent, ok := h.instanceByID[inst.ParentSessionID]
		h.instancesMu.RUnlock()
		if ok && parent.IsConductor {
			return parent.ID
		}
	}
	return ""
}

// activeConductorSessions returns all non-stopped, non-error conductor sessions
// in the current profile (h.instances is already scoped to the active profile).
func (h *Home) activeConductorSessions() []*session.Instance {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()

	var out []*session.Instance
	for _, inst := range h.instances {
		if inst.IsConductor &&
			inst.Status != session.StatusError &&
			inst.Status != session.StatusStopped {
			out = append(out, inst)
		}
	}
	return out
}

// mostRecentPathInGroup returns the project path of the most recently created
// session in the given group, or empty string if no sessions exist.
func (h *Home) mostRecentPathInGroup(groupPath string) string {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()

	var mostRecent *session.Instance
	for _, inst := range h.instances {
		if inst.GroupPath == groupPath && inst.ProjectPath != "" {
			if mostRecent == nil || inst.CreatedAt.After(mostRecent.CreatedAt) {
				mostRecent = inst
			}
		}
	}
	if mostRecent != nil {
		return mostRecent.ProjectPath
	}
	return ""
}

// forkSessionWithDialog opens the fork dialog to customize title and group
func (h *Home) forkSessionWithDialog(source *session.Instance) tea.Cmd {
	if source == nil {
		return nil
	}
	// Pre-populate dialog with source session info
	conductors := h.activeConductorSessions()
	suggestedParentID := h.suggestConductorParent()
	h.forkDialog.SetSize(h.width, h.height)
	h.forkDialog.ShowWithParentSandboxed(source.Title, source.ProjectPath, source.GroupPath, conductors, suggestedParentID, source.IsSandboxed())
	return nil
}

type forkWithStateWorktreeDeps struct {
	statPath                  func(string) (os.FileInfo, error)
	mkdirAll                  func(string, os.FileMode) error
	validateDestination       func(string, string) error
	detectInProgressOperation func(string) (string, error)
	hasSubmodules             func(string) bool
	headCommit                func(string) (string, error)
	createAtStartPoint        func(string, string, string, string) (bool, error)
	materialize               func(string, string, bool) error
	processInclude            func(string, string, io.Writer) error
	runSetup                  func(string, string, io.Writer, io.Writer, time.Duration) error
	removeWorktree            func(string, string, bool) error
	deleteBranch              func(string, string, bool) error
	// dirLocalNotice names a directory-local config skipped as untrusted
	// while resolving these settings; it is returned with the setup result.
	dirLocalNotice string
}

// sparseSourceDir is the parent session's worktree: with
// `[worktree] sparse_checkout = "inherit"` the fork's worktree inherits ITS
// sparse-checkout state (#1708). Pass "" to keep git's default checkout.
func defaultForkWithStateWorktreeDeps(sparseSourceDir string) forkWithStateWorktreeDeps {
	// Resolved for sparseSourceDir (the session's target directory, per
	// #2093) so directory-local .agent-deck/config.toml overrides apply. This
	// helper has no error return, so an invalid dir-local config here falls
	// back to the conservative default (no sparse-checkout inheritance)
	// rather than blocking the fork; the CLI/TUI call sites that resolve the
	// worktree PATH itself (session_cmd.go, worktree_target.go) still fail
	// closed on the same error. With no source dir there is nothing to
	// inherit from, so SparseInheritOptions yields the same zero options
	// either way and the lookup is skipped.
	wtSettings := session.GetWorktreeSettings()
	var dirLocalNotice string
	if sparseSourceDir != "" {
		if local, notice, err := session.GetWorktreeSettingsForDirWithNotice(sparseSourceDir); err != nil {
			uiLog.Warn("dir_local_config_invalid", slog.String("dir", sparseSourceDir), slog.String("error", err.Error()))
			wtSettings.SparseCheckout = ""
		} else {
			wtSettings = local
			dirLocalNotice = notice
		}
	}
	createOpts := wtSettings.CreateOptions(sparseSourceDir)
	return forkWithStateWorktreeDeps{
		dirLocalNotice:            dirLocalNotice,
		statPath:                  os.Stat,
		mkdirAll:                  os.MkdirAll,
		validateDestination:       git.ValidateForkWithStateDestination,
		detectInProgressOperation: git.DetectInProgressOperation,
		hasSubmodules:             git.HasSubmodules,
		headCommit:                git.HeadCommit,
		createAtStartPoint: func(repoDir, worktreePath, branch, startPoint string) (bool, error) {
			return git.CreateWorktreeAtStartPointWithOptions(repoDir, worktreePath, branch, startPoint, createOpts)
		},
		materialize:    git.MaterializeWipFromParent,
		processInclude: git.ProcessWorktreeInclude,
		runSetup:       git.RunWorktreeSetupAfterCreate,
		removeWorktree: git.RemoveWorktree,
		deleteBranch:   git.DeleteBranch,
	}
}

// forkInstanceDeps injects the post-helper instance-create/start/multi-repo and
// rollback steps of forkSessionCmdWithOptions so the three rollback paths are
// behaviorally testable (review finding G1).
type forkInstanceDeps struct {
	createInstance     func(source *session.Instance, title, groupPath string, opts *session.ClaudeOptions, parent session.ForkParent) (*session.Instance, error)
	createMultiRepoDir func(inst, source *session.Instance) error
	startInstance      func(inst *session.Instance) error
	rollback           func(repoRoot, worktreePath, branch string)
}

func defaultForkInstanceDeps() forkInstanceDeps {
	return forkInstanceDeps{
		createInstance: func(source *session.Instance, title, groupPath string, opts *session.ClaudeOptions, parent session.ForkParent) (*session.Instance, error) {
			inst, _, err := source.CreateForkedInstanceForToolUnder(title, groupPath, opts, parent)
			return inst, err
		},
		createMultiRepoDir: func(inst, source *session.Instance) error {
			// Propagate multi-repo config from source.
			if source.IsMultiRepo() {
				inst.MultiRepoEnabled = true
				inst.AdditionalPaths = append([]string{}, source.AdditionalPaths...)
				// Copy worktree tracking from source (shared worktrees)
				if len(source.MultiRepoWorktrees) > 0 {
					inst.MultiRepoWorktrees = append([]session.MultiRepoWorktree{}, source.MultiRepoWorktrees...)
				}
				// Create a new persistent dir for the fork with symlinks to shared worktrees
				worktreesRoot, rootErr := multiRepoWorktreesRoot()
				if rootErr != nil {
					return fmt.Errorf("failed to resolve multi-repo dir: %w", rootErr)
				}
				parentDir := filepath.Join(worktreesRoot, inst.ID[:8])
				if mkErr := os.MkdirAll(parentDir, 0o755); mkErr != nil {
					return fmt.Errorf("failed to create multi-repo dir: %w", mkErr)
				}
				if resolved, evalErr := filepath.EvalSymlinks(parentDir); evalErr == nil {
					parentDir = resolved
				}
				inst.MultiRepoTempDir = parentDir
				if inst.GetTmuxSession() != nil {
					inst.GetTmuxSession().WorkDir = parentDir
				}
				// Recreate symlinks/entries in new parent dir pointing to source worktree paths
				allPaths := inst.AllProjectPaths()
				dirnames := session.DeduplicateDirnames(allPaths)
				var newProjectPath string
				var newAdditionalPaths []string
				for i, p := range allPaths {
					linkPath := filepath.Join(parentDir, dirnames[i])
					_ = os.Symlink(p, linkPath)
					if i == 0 {
						newProjectPath = linkPath
					} else {
						newAdditionalPaths = append(newAdditionalPaths, linkPath)
					}
				}
				inst.ProjectPath = newProjectPath
				inst.AdditionalPaths = newAdditionalPaths
			}
			return nil
		},
		startInstance: func(inst *session.Instance) error {
			if err := inst.Start(); err != nil {
				return err
			}
			inst.RecordTelemetryCreate(telemetry.ViaTUIFork)
			telemetry.FeatureUsed("fork", false)
			return nil
		},
		rollback: func(repoRoot, worktreePath, branch string) {
			_ = rollbackForkWithStateWorktree(repoRoot, worktreePath, branch)
		},
	}
}

// completeFork performs the post-forkWithStateWorktree sequence: create the
// forked instance, apply sandbox/multi-repo/parent config, and start it. On any
// failure after a with-state worktree was created (withStateWorktreeCreated),
// it rolls back the new worktree+branch. Free function: it needs nothing from
// *Home.
//
// toggles.LockTitle marks the new instance TitleLocked: a forked Claude
// session inherits the parent's session name (e.g. an auto-assigned plan
// title), so without the lock the #572 name sync clobbers the title the user
// typed in the fork dialog on the fork's first hook event. Quick fork leaves
// it false — its "<title> (fork)" name is auto-generated, not user intent.
func completeFork(
	source *session.Instance,
	title, groupPath string,
	toggles forkToggles,
	opts *session.ClaudeOptions,
	parentSessionID, parentProjectPath string,
	withStateWorktreeCreated bool,
	deps forkInstanceDeps,
) (*session.Instance, error) {
	// The parent goes into the fork's construction, not onto the finished
	// instance: a Claude fork's first start runs the command baked here, and
	// its launch env depends on the parent.
	parent := session.ForkParent{SessionID: parentSessionID, ProjectPath: parentProjectPath}
	inst, err := deps.createInstance(source, title, groupPath, opts, parent)
	if err != nil {
		if withStateWorktreeCreated {
			deps.rollback(opts.WorktreeRepoRoot, opts.WorktreePath, opts.WorktreeBranch)
		}
		return nil, fmt.Errorf("cannot create forked instance: %w", err)
	}
	if toggles.LockTitle {
		inst.TitleLocked = true
	}

	// Apply sandbox config to forked instance.
	if toggles.Sandbox {
		inst.Sandbox = session.NewSandboxConfig("")
	}

	if err := deps.createMultiRepoDir(inst, source); err != nil {
		if withStateWorktreeCreated {
			deps.rollback(opts.WorktreeRepoRoot, opts.WorktreePath, opts.WorktreeBranch)
		}
		return nil, err
	}

	if err := deps.startInstance(inst); err != nil {
		if withStateWorktreeCreated {
			deps.rollback(opts.WorktreeRepoRoot, opts.WorktreePath, opts.WorktreeBranch)
		}
		return nil, err
	}

	switch inst.Tool {
	case "opencode":
		go inst.DetectOpenCodeSession()
	}

	return inst, nil
}

type forkBuildResult struct {
	cmd             tea.Cmd
	worktreeApplied bool
	notice          string
	errMsg          string
}

// forkToggles groups the fork pipeline's boolean knobs so call sites stay
// self-documenting instead of a positional true/false list.
type forkToggles struct {
	// Worktree creates the fork in a new worktree on a new branch.
	Worktree bool
	// WithState materializes the parent's working-tree state into the worktree.
	WithState bool
	// WithIgnored also copies gitignored files (implies WithState).
	WithIgnored bool
	// Sandbox runs the forked session in a Docker sandbox.
	Sandbox bool
	// ExplicitWorktree marks Worktree as an explicit user toggle rather than a
	// config default, gating the #1185 non-repo fallback.
	ExplicitWorktree bool
	// LockTitle sets TitleLocked on the fork: the title is explicit user
	// intent (fork dialog), so the #572 Claude-name sync must not clobber it.
	LockTitle bool
}

func joinForkNotices(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

// noticeError folds a non-fatal fork degradation notice into any pre-existing
// error (e.g. one set by a failed forceSaveInstances) so the notice never
// silently masks a real failure. Returns existing unchanged when notice is empty.
func noticeError(existing error, notice string) error {
	if notice == "" {
		return existing
	}
	if existing == nil {
		return fmt.Errorf("%s", notice)
	}
	return fmt.Errorf("%v; %s", existing, notice)
}

// rollbackForkWithStateWorktree removes the worktree/workspace and deletes the
// branch created by the with-state fork helpers, used when a later step fails
// after the helper already succeeded. Failures are both logged and returned
// (joined): the completeFork post-create path discards the return as best-effort
// so the caller's original error is preserved, while forkWithStateWorkspaceJJ
// uses it to report an accurate "cleaned up" vs "cleanup also failed" message
// instead of claiming success over an orphaned workspace.
//
// It re-detects the VCS backend so jj forks roll back via `jj workspace forget`
// (and bookmark delete) rather than git's worktree/branch removal — using the
// wrong VCS here would leave the new workspace orphaned. On detection failure it
// falls back to git semantics (the pre-#1305 behavior).
func rollbackForkWithStateWorktree(repoRoot, worktreePath, branch string) error {
	var errs []string
	removeWorktree := func(rmErr error) {
		if rmErr != nil {
			uiLog.Warn("fork_with_state_rollback_worktree_failed", slog.String("path", worktreePath), slog.String("err", rmErr.Error()))
			errs = append(errs, fmt.Sprintf("worktree remove failed: %v", rmErr))
		}
	}
	deleteBranch := func(brErr error) {
		if brErr != nil {
			uiLog.Warn("fork_with_state_rollback_branch_failed", slog.String("branch", branch), slog.String("err", brErr.Error()))
			errs = append(errs, fmt.Sprintf("branch delete failed: %v", brErr))
		}
	}
	if backend, err := vcsbackend.Detect(repoRoot); err != nil {
		removeWorktree(git.RemoveWorktree(repoRoot, worktreePath, true))
		deleteBranch(git.DeleteBranch(repoRoot, branch, true))
	} else {
		removeWorktree(backend.RemoveWorktree(worktreePath, true))
		deleteBranch(backend.DeleteBranch(branch, true))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// forkWithStateWorktree creates the fork's worktree, anchors it to the parent
// session's HEAD, and materializes the parent's working-tree state, mirroring
// the CLI safeguards from #1263. Defined after forkSessionCmdWithOptions so the
// call site (not this definition) is the structurally-first reference.
//
// setupErr is the non-fatal setup-script failure (nil on success); the fork is
// complete regardless, but the caller surfaces it. err is the fatal error.
func forkWithStateWorktree(parentPath, repoRoot, worktreePath, branch string, state git.WorktreeStateOptions, deps forkWithStateWorktreeDeps) (setupErr error, err error) {
	if state.WithIgnored {
		state.WithState = true
	}
	if !state.WithState {
		return nil, errors.New("forkWithStateWorktree called without WithState")
	}
	// Destination collision is the more actionable refusal, so check it before
	// the local path-existence guard (mirrors #1263's CLI precedence).
	if err := deps.validateDestination(repoRoot, branch); err != nil {
		var collErr *git.DestinationCollisionError
		if errors.As(err, &collErr) {
			switch collErr.Kind {
			case git.CollisionWorktreeExists:
				return nil, fmt.Errorf("branch %q already has a worktree at %s; choose a new destination branch for --with-state", collErr.Branch, collErr.Path)
			case git.CollisionBranchExists:
				return nil, fmt.Errorf("branch %q already exists; choose a new destination branch for --with-state", collErr.Branch)
			}
		}
		return nil, fmt.Errorf("failed to validate destination: %w", err)
	}
	if _, statErr := deps.statPath(worktreePath); statErr == nil {
		return nil, fmt.Errorf("worktree path already exists: %s", worktreePath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to stat worktree path: %w", statErr)
	}
	kind, detectErr := deps.detectInProgressOperation(parentPath)
	if detectErr != nil {
		return nil, fmt.Errorf("failed to inspect parent session state: %w", detectErr)
	}
	if kind != "" {
		abortCmd := map[string]string{
			"rebase":      "git rebase --abort",
			"merge":       "git merge --abort",
			"cherry-pick": "git cherry-pick --abort",
			"revert":      "git revert --abort",
			"bisect":      "git bisect reset",
		}[kind]
		return nil, fmt.Errorf("parent session is mid-%s; finish or abort the %s before forking with state (cd %q && %s)", kind, kind, parentPath, abortCmd)
	}
	if err := deps.mkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}
	if deps.hasSubmodules(parentPath) {
		uiLog.Warn("fork_with_state_submodules_detected", slog.String("parent", parentPath))
	}
	parentHead, err := deps.headCommit(parentPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve parent session HEAD: %w", err)
	}
	createdBranch, err := deps.createAtStartPoint(repoRoot, worktreePath, branch, parentHead)
	if err != nil {
		return nil, fmt.Errorf("worktree creation failed: %w", err)
	}
	if err := deps.materialize(parentPath, worktreePath, state.WithIgnored); err != nil {
		var cleanupErrs []string
		if rmErr := deps.removeWorktree(repoRoot, worktreePath, true); rmErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("worktree remove failed: %v", rmErr))
		}
		if createdBranch {
			if brErr := deps.deleteBranch(repoRoot, branch, true); brErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Sprintf("branch delete failed: %v", brErr))
			}
		}
		if len(cleanupErrs) == 0 {
			return nil, fmt.Errorf("failed to materialize parent state: %w; new worktree cleaned up", err)
		}
		branchHint := ""
		if createdBranch {
			branchHint = fmt.Sprintf(" && git -C %q branch -D %q", repoRoot, branch)
		}
		return nil, fmt.Errorf("failed to materialize parent state: %w; cleanup also failed (%s); manual cleanup required: rm -rf %q%s", err, strings.Join(cleanupErrs, "; "), worktreePath, branchHint)
	}
	if err := deps.processInclude(repoRoot, worktreePath, io.Discard); err != nil {
		uiLog.Warn("fork_with_state_worktreeinclude_failed", slog.String("path", worktreePath), slog.String("err", err.Error()))
	}
	var setupBuf bytes.Buffer
	if scriptErr := deps.runSetup(repoRoot, worktreePath, &setupBuf, &setupBuf, session.GetWorktreeSettings().SetupTimeout()); scriptErr != nil {
		// Non-fatal: the worktree and parent state are already created. Mirror
		// #1263's CLI, which warns on a failed setup script rather than failing
		// the whole fork. The output's tail is logged (setupOutputForLog); the
		// concise error is returned so the caller can surface it in the UI.
		uiLog.Warn("fork_with_state_setup_failed", slog.String("path", worktreePath), slog.String("err", scriptErr.Error()), slog.String("output", setupOutputForLog(setupBuf.String())))
		return withDirLocalNotice(withSetupOutputTail(scriptErr, setupBuf.String()), deps.dirLocalNotice), nil
	}
	return withDirLocalNotice(nil, deps.dirLocalNotice), nil
}

// withDirLocalNotice attaches a skipped-dir-local-config notice to the
// setup result for the footer; with no notice it returns setupErr as is.
func withDirLocalNotice(setupErr error, notice string) error {
	if notice == "" {
		return setupErr
	}
	return &dirLocalNoticeError{notice: notice, setup: setupErr}
}

// forkWithStateWorkspaceJJ is the jujutsu equivalent of forkWithStateWorktree:
// it creates the fork's jj workspace anchored at the parent session's committed
// point (@-) and materializes the parent's uncommitted working-copy state into
// it. Wired into forkSessionCmdWithOptions for jj backends (#1305).
//
// jj's model makes this leaner than git: the working copy is a commit and
// untracked files are auto-snapshotted, so a single restore carries tracked +
// untracked state; only gitignored files need a filesystem copy. There is also
// no staging area or in-progress rebase/merge/cherry-pick state to guard
// against, so the git path's index/operation safeguards do not apply.
func forkWithStateWorkspaceJJ(parentPath, repoRoot, workspacePath, branch string, state git.WorktreeStateOptions) error {
	if state.WithIgnored {
		state.WithState = true
	}
	if !state.WithState {
		return errors.New("forkWithStateWorkspaceJJ called without WithState")
	}
	// Validate the destination (read-only) BEFORE creating any filesystem state,
	// mirroring the git path (forkWithStateWorktree validates before deps.mkdirAll)
	// and the jj CLI path (session_cmd.go pre-checks BookmarkExists before its
	// os.MkdirAll). Otherwise a refused fork would leave an empty worktrees
	// container dir behind. CreateWorkspaceAtRevision re-checks the bookmark as a
	// transactional guard — the same defense-in-depth the git path uses.
	if _, statErr := os.Stat(workspacePath); statErr == nil {
		return fmt.Errorf("workspace path already exists: %s", workspacePath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("failed to stat workspace path: %w", statErr)
	}
	if branch != "" {
		if exists, bmErr := jujutsu.BookmarkExists(repoRoot, branch); bmErr != nil {
			return fmt.Errorf("failed to validate destination: %w", bmErr)
		} else if exists {
			return fmt.Errorf("bookmark %q already exists; choose a new destination branch for --with-state", branch)
		}
	}
	parentBase, err := jujutsu.WorkingCopyParentRevision(parentPath)
	if err != nil {
		return fmt.Errorf("failed to resolve parent session committed anchor: %w", err)
	}
	// All checks passed — now create the container dir and the workspace.
	if err := os.MkdirAll(filepath.Dir(workspacePath), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := jujutsu.CreateWorkspaceAtRevision(repoRoot, workspacePath, branch, parentBase); err != nil {
		return fmt.Errorf("workspace creation failed: %w", err)
	}
	if err := jujutsu.MaterializeWipFromParent(parentPath, workspacePath, state.WithIgnored); err != nil {
		// Roll back the workspace we just created so a materialize failure does
		// not leave an orphaned workspace behind — and report accurately whether
		// that cleanup actually succeeded.
		if cleanupErr := rollbackForkWithStateWorktree(repoRoot, workspacePath, branch); cleanupErr != nil {
			return fmt.Errorf("failed to materialize parent state: %w; cleanup also failed (%s); manual cleanup may be required: rm -rf %q", err, cleanupErr, workspacePath)
		}
		return fmt.Errorf("failed to materialize parent state: %w; new workspace cleaned up", err)
	}
	return nil
}
