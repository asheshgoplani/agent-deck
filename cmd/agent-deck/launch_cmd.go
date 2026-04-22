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
)

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
	parent := fs.String("parent", "", "Parent session (creates sub-session, inherits group)")
	parentShort := fs.String("p", "", "Parent session (short)")
	noParent := fs.Bool("no-parent", false, "Disable automatic parent linking")
	noTransitionNotify := fs.Bool("no-transition-notify", false, "Suppress transition event notifications to parent session")
	// #697: conductor-friendly title lock. Prevents Claude's session name
	// from overwriting the agent-deck title.
	titleLock := fs.Bool("title-lock", false, "Lock session title so Claude's session name never overrides it (#697)")
	noTitleSync := fs.Bool("no-title-sync", false, "Alias for --title-lock")
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

	// Extra claude CLI tokens - repeatable; mirrors handleAdd's --extra-arg.
	// Each invocation contributes one already-tokenised arg; feeds
	// Instance.ExtraArgs which buildClaudeExtraFlags shellescapes and appends.
	// Persisted plaintext in state.db — do NOT pass secrets like API keys.
	var extraArgFlags []string
	fs.Func("extra-arg", "Extra claude CLI token (can specify multiple times); requires -c claude; persisted plaintext — no secrets", func(s string) error {
		extraArgFlags = append(extraArgFlags, s)
		return nil
	})

	// Resume session flag
	resumeSession := fs.String("resume-session", "", "Claude session ID to resume")

	// Socket isolation (v1.7.50+, issue #687). Same semantics as
	// `agent-deck add --tmux-socket`: overrides `[tmux].socket_name` for
	// this one session, captured once and persisted on the Instance.
	tmuxSocket := fs.String("tmux-socket", "", "tmux -L socket name for this session (overrides [tmux].socket_name)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck launch [path] [options]")
		fmt.Println()
		fmt.Println("Create, start, and optionally send a message to a new session in one step.")
		fmt.Println("Combines: add + session start + session send")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  [path]    Project directory (defaults to current directory)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck launch . -c claude")
		fmt.Println("  agent-deck launch . -c claude -m \"Explain this codebase\"")
		fmt.Println("  agent-deck launch /path/to/project -t \"My Agent\" -c claude -g work")
		fmt.Println("  agent-deck launch . -c claude --mcp memory -m \"Research topic X\"")
		fmt.Println("  agent-deck launch . -c claude --channel plugin:telegram@user/repo -m \"Listen for messages\"")
		fmt.Println("  agent-deck launch . -c claude -m \"Fix bug\" --no-wait")
		fmt.Println("  agent-deck launch . -c \"codex --dangerously-bypass-approvals-and-sandbox\"")
		fmt.Println("  agent-deck launch . -g ard --no-parent -c claude -m \"Run review\"")
	}

	// Reorder args: move path to end so flags are parsed correctly
	args = reorderArgsForFlagParsing(args)

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Resolve path
	path := strings.Trim(fs.Arg(0), "'\"")
	if path == "" || path == "." {
		var err error
		path, err = os.Getwd()
		if err != nil {
			out.Error(fmt.Sprintf("failed to get current directory: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	} else {
		var err error
		path, err = filepath.Abs(path)
		if err != nil {
			out.Error(fmt.Sprintf("failed to resolve path: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
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
	var worktreePath, worktreeRepoRoot string
	if wtBranch != "" {
		if !git.IsGitRepoOrBareProjectRoot(path) {
			out.Error(fmt.Sprintf("%s is not a git repository", path), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		repoRoot, err := git.GetWorktreeBaseRoot(path)
		if err != nil {
			out.Error(fmt.Sprintf("failed to get repo root: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		// Apply configured branch prefix before validation/existence checks
		wtSettings := session.GetWorktreeSettings()
		wtBranch = wtSettings.ApplyBranchPrefix(wtBranch)

		if err := git.ValidateBranchName(wtBranch); err != nil {
			out.Error(fmt.Sprintf("invalid branch name: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		branchExists := git.BranchExists(repoRoot, wtBranch)
		if createNewBranch && branchExists {
			out.Error(fmt.Sprintf("branch '%s' already exists (remove -b flag to use existing branch)", wtBranch), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		location := wtSettings.DefaultLocation
		if *worktreeLocation != "" {
			location = *worktreeLocation
		}

		worktreePath = git.WorktreePath(git.WorktreePathOptions{
			Branch:    wtBranch,
			Location:  location,
			RepoDir:   repoRoot,
			SessionID: git.GeneratePathID(),
			Template:  wtSettings.Template(),
		})

		// Check for an existing worktree for this branch before creating a new one
		if existingPath, err := git.GetWorktreeForBranch(repoRoot, wtBranch); err == nil && existingPath != "" {
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

			setupErr, err := git.CreateWorktreeWithSetup(repoRoot, worktreePath, wtBranch, os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
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

	// Resolve parent session if specified
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
		sessionGroup = resolveGroupSelection(sessionGroup, parentInstance.GroupPath, explicitGroupProvided)
	} else if !*noParent {
		parentInstance = resolveAutoParentInstance(instances)
		if parentInstance != nil && !parentInstance.IsSubSession() {
			sessionGroup = resolveGroupSelection(sessionGroup, parentInstance.GroupPath, explicitGroupProvided)
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

	if worktreePath != "" {
		newInstance.WorktreePath = worktreePath
		newInstance.WorktreeRepoRoot = worktreeRepoRoot
		newInstance.WorktreeBranch = wtBranch
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

	// Add to instances and save
	instances = append(instances, newInstance)

	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	if newInstance.GroupPath != "" {
		groupTree.CreateGroup(newInstance.GroupPath)
	}

	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
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

	// Start the session.
	// - default: StartWithMessage waits for readiness and delivers initial prompt
	// - --no-wait: start immediately, then fire-and-forget send below
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

	// Save again with updated state (session ID, tmux name)
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Send message only for --no-wait mode.
	// Non --no-wait mode already sent via StartWithMessage above.
	// Even in no-wait mode, run a short send-verification loop so Enter-loss
	// races don't silently drop the initial prompt.
	if initialMessage != "" && *noWait {
		tmuxSess := newInstance.GetTmuxSession()
		if tmuxSess != nil {
			if err := sendWithRetryTarget(tmuxSess, initialMessage, false, sendRetryOptions{
				maxRetries: 8,
				checkDelay: 150 * time.Millisecond,
			}); err != nil {
				out.Error(fmt.Sprintf("failed to send initial message: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
		}
	}

	// Build output
	jsonData := map[string]interface{}{
		"success": true,
		"id":      newInstance.ID,
		"title":   newInstance.Title,
		"path":    path,
		"tool":    newInstance.Tool,
		"group":   newInstance.GroupPath,
		"profile": storage.Profile(),
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
