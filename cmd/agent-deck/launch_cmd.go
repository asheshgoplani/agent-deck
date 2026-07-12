package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

// assertDoneInstruction is appended to a child's initial message so it ends its
// final turn with the #1186 completion sentinel. The completion ledger and the
// parent inbox both key off this line; without it "done" is never trustworthy.
const assertDoneInstruction = "\n\n## Final step — assert completion\n" +
	"When the task is fully done, print exactly this as the last line of your final message:\n" +
	"  ===AGENTDECK_DONE=== status=ok summary=<what you accomplished, one line>\n" +
	"Use status=fail if you could not complete it; put the blocker in the summary."

// applyAssertDone appends the completion-sentinel instruction when enabled and
// there is an initial message to attach it to. A no-op for an empty message
// (nothing to append to) or when disabled.
func applyAssertDone(message string, enabled bool) string {
	if !enabled || strings.TrimSpace(message) == "" {
		return message
	}
	return message + assertDoneInstruction
}

// handleLaunch combines add + start + optional send into a single command.
// It creates a new session, starts it, and optionally sends an initial message.
func handleLaunch(profile string, args []string) {
	fs := flag.NewFlagSet("launch", flag.ExitOnError)
	title := fs.String("title", "", "Session title (defaults to folder name)")
	titleShort := fs.String("t", "", "Session title (short)")
	group := fs.String("group", "", "Group path (defaults to parent folder)")
	groupShort := fs.String("g", "", "Group path (short)")
	command := fs.String("cmd", "", "Tool/command to run (e.g., 'claude' or 'codex --dangerously-bypass-approvals-and-sandbox')")
	commandShort := fs.String("c", "", "Tool/command to run (short)")
	wrapper := fs.String("wrapper", "", "Wrapper command (use {command} to include tool command; auto-generated when --cmd includes extra args)")
	message := fs.String("message", "", "Initial message to send once agent is ready")
	messageShort := fs.String("m", "", "Initial message to send (short)")
	noWait := fs.Bool("no-wait", false, "Don't wait for agent to be ready before sending message")
	assertDone := fs.Bool("assert-done", false, "Append a completion-sentinel instruction to the message (default on for -c claude)")
	noAssertDone := fs.Bool("no-assert-done", false, "Disable the completion-sentinel instruction")
	parent := fs.String("parent", "", "Parent session (creates sub-session; group is cwd-derived by default — auto-inherits the parent's group for git worktree children or with --inherit-group)")
	parentShort := fs.String("p", "", "Parent session (short)")
	noParent := fs.Bool("no-parent", false, "Disable automatic parent linking")
	// Keep a fanned-out child in the parent's group instead of the cwd-derived
	// group. Without this, a child launched into a worktree (.worktrees/<branch>)
	// derives its group from that leaf folder and lands in a per-branch group
	// detached from the parent. Opt-in so #972 (conductor children -> project
	// group) is preserved by default. Used by the fleet skill.
	inheritGroup := fs.Bool("inherit-group", false, "Place the child in the parent session's group instead of the cwd-derived group (auto-applied for git worktree children; use this to force it for non-worktree paths)")
	noTransitionNotify := fs.Bool("no-transition-notify", false, "Suppress transition event notifications to parent session")
	// #697: conductor-friendly title lock. Prevents Claude's session name
	// from overwriting the agent-deck title.
	titleLock := fs.Bool("title-lock", false, "Lock session title so Claude's session name never overrides it (#697)")
	noTitleSync := fs.Bool("no-title-sync", false, "Alias for --title-lock")
	// #1133: opt-in to inherit the conductor's TELEGRAM_* env vars in the
	// child. Off by default — a child inheriting TELEGRAM_STATE_DIR /
	// TELEGRAM_BOT_TOKEN spawns a duplicate `bun telegram` poller that
	// races the conductor for the bot lock (Telegram 409, dropped messages).
	inheritTelegramEnv := fs.Bool("inherit-telegram-env", false, "Keep TELEGRAM_* env vars in the child (#1133); off by default to prevent duplicate plugin pollers")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	// Worktree flags
	worktreeBranch := fs.String("w", "", "Create session in git worktree for branch")
	worktreeBranchLong := fs.String("worktree", "", "Create session in git worktree for branch")
	newBranch := fs.Bool("b", false, "Create new branch (use with --worktree)")
	newBranchLong := fs.Bool("new-branch", false, "Create new branch")
	worktreeLocation := fs.String("location", "", "Worktree location: sibling, subdirectory, or custom path")

	// MCP flag
	var mcpFlags []string
	fs.Func("mcp", "MCP to attach (can specify multiple times)", func(s string) error {
		mcpFlags = append(mcpFlags, s)
		return nil
	})

	// Plugin channel flag - can be specified multiple times; requires -c claude.
	// Mirrors handleAdd's --channel; both routes feed Instance.Channels which
	// buildClaudeExtraFlags emits as --channels <csv> on every Start/Restart.
	var channelFlags []string
	fs.Func("channel", "Plugin channel id (can specify multiple times); requires -c claude", func(s string) error {
		channelFlags = append(channelFlags, s)
		return nil
	})

	// Plugin enablement flag — repeatable, catalog-only, claude-only.
	// Mirrors handleAdd's --plugin; resolved at spawn through
	// [plugins.<name>] in ~/.agent-deck/config.toml (RFC docs/rfc/PLUGIN_ATTACH.md).
	var pluginFlags []string
	fs.Func("plugin", "Catalog plugin to enable for this session (can specify multiple times); requires -c claude", func(s string) error {
		pluginFlags = append(pluginFlags, s)
		return nil
	})
	noChannelLink := fs.Bool("no-channel-link", false, "Disable auto-link between --plugin entries with emits_channel=true and --channel")

	// Extra claude CLI tokens - repeatable; mirrors handleAdd's --extra-arg.
	// Each invocation contributes one already-tokenised arg; feeds
	// Instance.ExtraArgs which buildClaudeExtraFlags shellescapes and appends.
	// Persisted plaintext in state.db — do NOT pass secrets like API keys.
	var extraArgFlags []string
	fs.Func("extra-arg", "Extra claude CLI token (can specify multiple times); requires -c claude; persisted plaintext — no secrets", func(s string) error {
		if err := session.ValidateClaudeExtraArgToken(s); err != nil {
			return err
		}
		extraArgFlags = append(extraArgFlags, s)
		return nil
	})

	// Resume session flag
	resumeSession := fs.String("resume-session", "", "Claude session ID to resume")
	modelID := fs.String("model", "", "Model ID/version to use for this session (claude, codex, gemini, opencode)")

	// Socket isolation (v1.7.50+, issue #687). Same semantics as
	// `agent-deck add --tmux-socket`: overrides `[tmux].socket_name` for
	// this one session, captured once and persisted on the Instance.
	tmuxSocket := fs.String("tmux-socket", "", "tmux -L socket name for this session (overrides [tmux].socket_name)")

	// Issue #1143: auto-stop dormant child sessions.
	idleTimeout := fs.String("idle-timeout", "", "Auto-stop session after this duration of no tmux output (Go duration: 30m, 1h, 24h). 0 or unset = disabled")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck launch [path] [options]")
		fmt.Println()
		fmt.Println("Create, start, and optionally send a message to a new session in one step.")
		fmt.Println("Combines: add + session start + session send")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  [path]    Project directory (default: group default_path, then global default_path, then current directory)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck launch . -c claude")
		fmt.Println("  agent-deck launch . -c codex --model gpt-5.5")
		fmt.Println("  agent-deck launch . -c gemini --model gemini-3.1-pro-preview")
		fmt.Println("  agent-deck launch . -c claude -m \"Explain this codebase\"")
		fmt.Println("  agent-deck launch /path/to/project -t \"My Agent\" -c claude -g work")
		fmt.Println("  agent-deck launch . -c claude --mcp memory -m \"Research topic X\"")
		fmt.Println("  agent-deck launch . -c claude --channel plugin:telegram@user/repo -m \"Listen for messages\"")
		fmt.Println("  agent-deck launch . -c claude -m \"Fix bug\" --no-wait")
		fmt.Println("  agent-deck launch . -c claude -m \"Refactor X\"   # auto-appends completion sentinel (see session children)")
		fmt.Println("  agent-deck launch . -c \"codex --dangerously-bypass-approvals-and-sandbox\"")
		fmt.Println("  agent-deck launch . -g ard --no-parent -c claude -m \"Run review\"")
		fmt.Println("  agent-deck launch . -c claude -w feature/new -b -m \"Start work\"")
	}

	// Reorder args: move path to end so flags are parsed correctly
	args = reorderArgsForFlagParsing(args)

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Resolve path
	path, err := resolveLaunchPath(strings.Trim(fs.Arg(0), "'\""), mergeFlags(*group, *groupShort), profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to resolve path: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Verify path exists and is a directory
	info, err := os.Stat(path)
	if err != nil {
		out.Error(fmt.Sprintf("path does not exist: %s", path), ErrCodeNotFound)
		os.Exit(1)
	}
	if !info.IsDir() {
		out.Error(fmt.Sprintf("path is not a directory: %s", path), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Merge flags
	sessionTitle := mergeFlags(*title, *titleShort)
	sessionGroup := mergeFlags(*group, *groupShort)
	explicitGroupProvided := strings.TrimSpace(sessionGroup) != ""
	sessionCommandInput := mergeFlags(*command, *commandShort)
	sessionCommandTool, sessionCommandResolved, sessionWrapperResolved, sessionCommandNote := resolveSessionCommand(sessionCommandInput, *wrapper)
	sessionParent := mergeFlags(*parent, *parentShort)
	if sessionParent != "" && *noParent {
		out.Error("--parent and --no-parent cannot be used together", ErrCodeInvalidOperation)
		os.Exit(1)
	}
	initialMessage := mergeFlags(*message, *messageShort)

	// --assert-done: append the completion-sentinel instruction so the child
	// reliably reports back via the ledger / parent inbox. Default-on for
	// Claude children (a completion signal nobody requests is useless);
	// --no-assert-done always wins.
	assertDoneTool := firstNonEmpty(sessionCommandTool, detectTool(sessionCommandInput))
	assertDoneOn := *assertDone
	if !*assertDone && !*noAssertDone && session.IsClaudeCompatible(assertDoneTool) {
		assertDoneOn = true
	}
	if *noAssertDone {
		assertDoneOn = false
	}
	initialMessage = applyAssertDone(initialMessage, assertDoneOn)

	// Resolve worktree flags
	wtBranch := *worktreeBranch
	if *worktreeBranchLong != "" {
		wtBranch = *worktreeBranchLong
	}
	createNewBranch := *newBranch || *newBranchLong

	// Validate --resume-session requires Claude
	if *resumeSession != "" {
		tool := firstNonEmpty(sessionCommandTool, detectTool(sessionCommandInput))
		if tool != "claude" {
			out.Error("--resume-session only works with Claude sessions (-c claude)", ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Handle worktree creation
	var worktreePath, worktreeRepoRoot, worktreeType string
	if wtBranch != "" {
		backend, err := detectAndCreateBackend(path)
		if err != nil {
			out.Error(fmt.Sprintf("%v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		worktreeType = string(backend.Type())
		repoRoot := backend.RepoDir()

		// Apply configured branch prefix before validation/existence checks
		wtSettings := session.GetWorktreeSettings()
		wtBranch = wtSettings.ApplyBranchPrefix(wtBranch)

		if err := git.ValidateBranchName(wtBranch); err != nil {
			out.Error(fmt.Sprintf("invalid branch name: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		branchExists := backend.BranchExists(wtBranch)
		if createNewBranch && branchExists {
			out.Error(fmt.Sprintf("branch '%s' already exists (remove -b flag to use existing branch)", wtBranch), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		location := wtSettings.DefaultLocation
		if *worktreeLocation != "" {
			location = *worktreeLocation
		}

		worktreePath = backend.WorktreePath(vcs.WorktreePathOptions{
			Branch:    wtBranch,
			Location:  location,
			SessionID: git.GeneratePathID(),
			Template:  wtSettings.Template(),
		})

		// Check for an existing worktree for this branch before creating a new one
		if existingPath, err := backend.GetWorktreeForBranch(wtBranch); err == nil && existingPath != "" {
			fmt.Fprintf(os.Stderr, "Reusing existing worktree at %s for branch %s\n", existingPath, wtBranch)
			worktreePath = existingPath
		} else {
			if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
				out.Error(fmt.Sprintf("failed to create parent directory: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}

			if _, err := os.Stat(worktreePath); err == nil {
				out.Error(fmt.Sprintf("worktree already exists at %s", worktreePath), ErrCodeInvalidOperation)
				os.Exit(1)
			}

			setupErr, err := createWorktreeWithSetup(backend, worktreePath, wtBranch, os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
			if err != nil {
				out.Error(fmt.Sprintf("failed to create worktree: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			if setupErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: worktree setup script failed: %v\n", setupErr)
			}
		}

		worktreeRepoRoot = repoRoot
		path = worktreePath
	}

	// Load sessions
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve parent session if specified.
	// Issue #972: when no explicit -g is passed, prefer the cwd-derived
	// project group over the parent's group, so conductor-spawned children
	// land in the project group (e.g. `agent-deck`) instead of the
	// conductor's own group (`conductor`). The parent group is now a
	// fallback for path mappings that produce no group.
	cwdDerivedGroup := session.GroupPathForProject(path)
	// A worktree child auto-inherits its parent's group (issue: fleets fanned
	// into worktrees scattered into junk per-branch / `worktrees` groups, or
	// a deliberately-named group, detached from the parent). `path` is already
	// the final worktree path here (the -w branch above reassigns it before
	// this point). git.IsLinkedWorktree returns false for main working trees,
	// so #972's conductor children (separate real repos) keep cwd-derived group.
	// The thunk defers the git probe until shouldInheritParentGroup needs it.
	inheritParentGroup := shouldInheritParentGroup(explicitGroupProvided, *inheritGroup, func() bool {
		return git.IsLinkedWorktree(path)
	})
	var parentInstance *session.Instance
	if sessionParent != "" {
		var errMsg string
		parentInstance, errMsg, _ = ResolveSession(sessionParent, instances)
		if parentInstance == nil {
			out.Error(errMsg, ErrCodeNotFound)
			os.Exit(1)
		}
		if parentInstance.IsSubSession() {
			out.Error("cannot create sub-session of a sub-session (single level only)", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		sessionGroup = resolveGroupSelection(sessionGroup, cwdDerivedGroup, parentInstance.GroupPath, explicitGroupProvided, inheritParentGroup)
	} else if !*noParent {
		parentInstance = resolveAutoParentInstance(instances)
		if parentInstance != nil && !parentInstance.IsSubSession() {
			sessionGroup = resolveGroupSelection(sessionGroup, cwdDerivedGroup, parentInstance.GroupPath, explicitGroupProvided, inheritParentGroup)
		} else {
			parentInstance = nil
		}
	}

	// Default title to folder name
	if sessionTitle == "" {
		sessionTitle = filepath.Base(path)
	}

	// Check for duplicate and generate unique title
	userProvidedTitle := (mergeFlags(*title, *titleShort) != "")
	if !userProvidedTitle {
		sessionTitle = generateUniqueTitle(instances, sessionTitle, path)
	} else {
		if isDupe, existingInst := isDuplicateSession(instances, sessionTitle, path); isDupe {
			out.Error(
				fmt.Sprintf("session already exists: %s (%s)", existingInst.Title, existingInst.ID),
				ErrCodeAlreadyExists,
			)
			os.Exit(1)
		}
	}

	// Create new instance
	var newInstance *session.Instance
	if sessionGroup != "" {
		newInstance = session.NewInstanceWithGroup(sessionTitle, path, sessionGroup)
	} else {
		newInstance = session.NewInstance(sessionTitle, path)
	}

	// Socket-isolation CLI override (issue #687 phase 1, v1.7.50).
	// Matches `agent-deck add --tmux-socket`. Whitespace-only flag falls
	// back to the config default already seeded by NewInstance.
	if flagSocket := strings.TrimSpace(*tmuxSocket); flagSocket != "" {
		newInstance.TmuxSocketName = flagSocket
		if ts := newInstance.GetTmuxSession(); ts != nil {
			ts.SocketName = flagSocket
		}
	}

	if parentInstance != nil {
		newInstance.SetParentWithPath(parentInstance.ID, parentInstance.ProjectPath)
	}

	if *noTransitionNotify {
		newInstance.NoTransitionNotify = true
	}

	// #697: title-lock blocks Claude's session-name sync.
	if *titleLock || *noTitleSync {
		newInstance.TitleLocked = true
	}

	// #1133: explicit opt-in for inheriting the conductor's telegram env.
	if *inheritTelegramEnv {
		newInstance.InheritTelegramEnv = true
	}

	if sessionCommandInput != "" {
		newInstance.Tool = firstNonEmpty(sessionCommandTool, detectTool(sessionCommandInput))
		newInstance.Command = sessionCommandResolved
	}

	// Apply --channel flags (claude only — channels is a Claude Code CLI flag).
	if len(channelFlags) > 0 {
		if newInstance.Tool != "claude" {
			out.Error("--channel only supported for claude sessions (use -c claude); requires --channels on the claude binary", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		newInstance.Channels = channelFlags
	}

	// Apply --plugin flags (catalog-only, claude-only, RFC docs/rfc/PLUGIN_ATTACH.md).
	if len(pluginFlags) > 0 {
		if newInstance.Tool != "claude" {
			out.Error("--plugin only supported for claude sessions (use -c claude); plugins enable Claude Code plugin features per-session via enabledPlugins", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		if err := validatePluginFlags(pluginFlags); err != nil {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		newInstance.Plugins = pluginFlags
		newInstance.PluginChannelLinkDisabled = *noChannelLink
		applyPluginChannelAutolink(newInstance)
	} else if *noChannelLink {
		newInstance.PluginChannelLinkDisabled = true
	}

	// Apply --extra-arg flags (claude only; mirror of handleAdd).
	if len(extraArgFlags) > 0 {
		if newInstance.Tool != "claude" {
			out.Error("--extra-arg only supported for claude sessions (use -c claude); claude is the only tool whose builder appends user extra args", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		newInstance.ExtraArgs = extraArgFlags
	}

	if sessionWrapperResolved != "" {
		newInstance.Wrapper = sessionWrapperResolved
	}

	selectedModelID := strings.TrimSpace(*modelID)
	if selectedModelID != "" {
		if err := applyCLIModelOverride(newInstance, selectedModelID); err != nil {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	if worktreePath != "" {
		newInstance.WorktreePath = worktreePath
		newInstance.WorktreeRepoRoot = worktreeRepoRoot
		newInstance.WorktreeBranch = wtBranch
		newInstance.WorktreeType = worktreeType
	}

	// Issue #1143: --idle-timeout 30m → 1800s on the Instance, picked up by
	// the central watcher on its next tick.
	if idleSecs, err := session.ParseIdleTimeoutFlag(strings.TrimSpace(*idleTimeout)); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	} else {
		newInstance.IdleTimeoutSecs = idleSecs
	}

	if *resumeSession != "" {
		newInstance.ClaudeSessionID = *resumeSession
		newInstance.ClaudeDetectedAt = time.Now()

		opts := newInstance.GetClaudeOptions()
		if opts == nil {
			userConfig, _ := session.LoadUserConfig()
			opts = session.NewClaudeOptions(userConfig)
		}
		opts.SessionMode = "resume"
		opts.ResumeSessionID = *resumeSession
		_ = newInstance.SetClaudeOptions(opts)
	}

	// Add to instances list (in-memory only — used for downstream
	// group cap math and the second SaveWithGroups after PostStartSync).
	instances = append(instances, newInstance)

	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	launchCfg, _ := session.LoadUserConfig()
	groupTree.DefaultMaxConcurrent = launchCfg.GroupDefaults.MaxConcurrent
	if newInstance.GroupPath != "" {
		groupTree.CreateGroupPath(newInstance.GroupPath)
	}

	// v1.9.x issue #1031: targeted single-row insert + verify, NOT the
	// load-modify-write SaveWithGroups rewrite. SaveWithGroups under
	// concurrent launches loses sibling rows via the DELETE-NOT-IN
	// sweep inside SaveInstances; InsertSessionAndVerify uses
	// SaveInstance (single-row INSERT OR REPLACE) + verify-with-backoff
	// to guarantee persistence. Mirror of RemoveSessionAndVerify (#909).
	if err := storage.InsertSessionAndVerify(newInstance, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Attach MCPs if specified
	if len(mcpFlags) > 0 {
		availableMCPs := session.GetAvailableMCPs()
		for _, mcpName := range mcpFlags {
			if _, exists := availableMCPs[mcpName]; !exists {
				out.Error(fmt.Sprintf("MCP '%s' not found in config.toml", mcpName), ErrCodeNotFound)
				os.Exit(1)
			}
		}
		if err := session.WriteMCPJsonFromConfig(path, mcpFlags); err != nil {
			out.Error(fmt.Sprintf("failed to write MCPs: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// v1.9.1 group concurrency cap: if the target group is at its
	// max_concurrent cap, mark this session queued instead of starting.
	// Groups with max_concurrent<=0 (legacy default) skip this check.
	tree := session.NewGroupTreeWithGroups(instances, groups)
	maxC := session.GroupMaxConcurrent(tree, newInstance.GroupPath)
	if session.ShouldQueue(instances, newInstance.GroupPath, maxC) {
		newInstance.Status = session.StatusQueued
		// v1.9.x issue #1031: same targeted single-row pattern as the
		// initial insert above — saveSessionData → SaveWithGroups is
		// the load-modify-write rewrite that loses sibling launches'
		// rows under concurrency.
		if err := storage.InsertSessionAndVerify(newInstance, tree); err != nil {
			out.Error(fmt.Sprintf("failed to save queued state: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		queuedJSON := map[string]interface{}{
			"success":        true,
			"id":             newInstance.ID,
			"title":          newInstance.Title,
			"status":         "queued",
			"group":          newInstance.GroupPath,
			"max_concurrent": maxC,
		}
		addModelInfoJSON(queuedJSON, newInstance.LaunchModelInfo())
		out.Success(fmt.Sprintf("Queued session: %s (group at cap %d)", newInstance.Title, maxC), queuedJSON)
		return
	}

	// Issue #955: strip TELEGRAM_STATE_DIR from the agent-deck CLI
	// process env before the tmux server inherits it on the first
	// `new-session`. No-op for conductors and explicit telegram
	// channel owners — they legitimately own the bot token. Sits
	// above the S8 exec-layer (env -u TELEGRAM_STATE_DIR claude …)
	// so even non-claude descendants of the pane (Bash-tool spawns,
	// fork claudes, restart respawn) start with a clean env.
	session.ScrubProcessEnvForChildLaunch(newInstance)

	// Start the session.
	// - default: StartWithMessage waits for readiness and delivers initial prompt
	// - --no-wait: start immediately, then fire-and-forget send below
	//
	// Issue #964: gate the spawn through a process-wide semaphore so a burst
	// of parallel `agent-deck launch` calls cannot cascade into swap thrash +
	// fork:ENOMEM. Cap defaults to defaultMaxParallelLaunch (3) and honours
	// AGENT_DECK_MAX_PARALLEL_LAUNCH.
	throttle := defaultLaunchThrottle()
	throttle.Acquire()
	defer throttle.Release()

	if initialMessage != "" && !*noWait {
		if err := newInstance.StartWithMessage(initialMessage); err != nil {
			out.Error(fmt.Sprintf("failed to start session: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	} else {
		if err := newInstance.Start(); err != nil {
			out.Error(fmt.Sprintf("failed to start session: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Capture session ID from tmux
	newInstance.PostStartSync(3 * time.Second)

	// v1.9.x issue #1031: third save point — fields populated by
	// PostStartSync (tmux session name, ClaudeSessionID once detected)
	// land on `newInstance`. Same targeted single-row insert/upsert
	// pattern as the two saves above; the load-modify-write
	// saveSessionData → SaveWithGroups path would let a sibling
	// launch's row be silently DELETE'd by this rewrite's
	// `DELETE FROM instances WHERE id NOT IN (...)` step.
	postStartTree := session.NewGroupTreeWithGroups(instances, groups)
	postStartCfg, _ := session.LoadUserConfig()
	postStartTree.DefaultMaxConcurrent = postStartCfg.GroupDefaults.MaxConcurrent
	if newInstance.GroupPath != "" {
		postStartTree.CreateGroupPath(newInstance.GroupPath)
	}
	if err := storage.InsertSessionAndVerify(newInstance, postStartTree); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Send message only for --no-wait mode.
	// Non --no-wait mode already sent via StartWithMessage above.
	// Even in no-wait mode, run a send-verification loop so Enter-loss
	// races don't silently drop the initial prompt.
	//
	// v1.7.64 (internal task "54-launch-verify-prompt"): after the initial
	// sendWithRetryTarget pass, run verifyPromptConsumedAfterLaunch to catch
	// the welcome-screen race where claude eats the first Enter. 10s budget
	// per window + single retry + stderr warning on persistent no-op.
	if initialMessage != "" && *noWait {
		tmuxSess := newInstance.GetTmuxSession()
		if tmuxSess != nil {
			if _, err := sendWithRetryTarget(tmuxSess, initialMessage, skipClaudeDeliveryVerify(newInstance.Tool), sendRetryOptions{
				maxRetries: 8,
				checkDelay: 150 * time.Millisecond,
			}); err != nil {
				out.Error(fmt.Sprintf("failed to send initial message: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			verifyPromptConsumedAfterLaunch(
				tmuxSess, initialMessage,
				10*time.Second, 250*time.Millisecond,
				os.Stderr,
			)
		}
	}

	// Build output. v1.9.x issue #1031: surface the new session ID
	// under an explicit `session_id` key so callers (conductor fleet
	// spawn loops, shell scripts) don't have to fall back to diffing
	// `agent-deck list --json` before/after — that diff was unsafe
	// under the launch-race the structural fix above also closes.
	// The legacy `id` key is kept for backward compatibility.
	jsonData := map[string]interface{}{
		"success":    true,
		"id":         newInstance.ID,
		"session_id": newInstance.ID,
		"title":      newInstance.Title,
		"path":       path,
		"tool":       newInstance.Tool,
		"group":      newInstance.GroupPath,
		"profile":    storage.Profile(),
	}
	if sessionCommandInput != "" {
		jsonData["command"] = sessionCommandInput
		jsonData["resolved_command"] = newInstance.Command
		if newInstance.Wrapper != "" {
			jsonData["wrapper"] = newInstance.Wrapper
		}
		if sessionCommandNote != "" {
			jsonData["command_note"] = sessionCommandNote
		}
	}
	if initialMessage != "" {
		jsonData["message"] = initialMessage
		jsonData["message_pending"] = *noWait
	}
	if len(mcpFlags) > 0 {
		jsonData["mcps"] = mcpFlags
	}
	if parentInstance != nil {
		jsonData["parent_id"] = parentInstance.ID
	}
	if worktreePath != "" {
		jsonData["worktree_path"] = worktreePath
		jsonData["worktree_branch"] = wtBranch
	}
	addModelInfoJSON(jsonData, newInstance.LaunchModelInfo())

	msg := fmt.Sprintf("Launched session: %s", newInstance.Title)
	if initialMessage != "" {
		if *noWait {
			msg += " (message sent with --no-wait)"
		} else {
			msg += " (message sent)"
		}
	}
	out.Success(msg, jsonData)
}

// resolveLaunchPath resolves the project path for `agent-deck launch`.
//
// An explicit path argument always wins — including ".", which keeps its
// "right here" meaning (resolved like add's positional arg). When no path is
// given, the resolution chain matches `add` (#1303): the target group's
// default_path first, then the global config default_path, then cwd.
func resolveLaunchPath(rawPathArg, groupSelector, profile string) (string, error) {
	if rawPathArg != "" {
		return resolveAddPath(rawPathArg)
	}

	if grp := strings.TrimSpace(groupSelector); grp != "" {
		if storage, instances, groups, err := loadSessionData(profile); err == nil {
			groupTree := session.NewGroupTreeWithGroups(instances, groups)
			path := groupTree.DefaultPathForGroup(resolveGroupPathForAdd(groupTree, grp))
			_ = storage.Close()
			if path != "" {
				return path, nil
			}
		}
	}

	if userCfg, err := session.LoadUserConfig(); err == nil {
		if path := resolveConfiguredDefaultPath(userCfg.DefaultPath); path != "" {
			return path, nil
		}
	}

	return os.Getwd()
}
