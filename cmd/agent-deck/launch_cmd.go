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
	command := fs.String("cmd", "", "Command to run (e.g., 'claude', 'gemini')")
	commandShort := fs.String("c", "", "Command to run (short)")
	wrapper := fs.String("wrapper", "", "Wrapper command (use {command} to include tool command)")
	message := fs.String("message", "", "Initial message to send once agent is ready")
	messageShort := fs.String("m", "", "Initial message to send (short)")
	noWait := fs.Bool("no-wait", false, "Don't wait for agent to be ready before sending message")
	parent := fs.String("parent", "", "Parent session (creates sub-session, inherits group)")
	parentShort := fs.String("p", "", "Parent session (short)")
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

	// Resume session flag
	resumeSession := fs.String("resume-session", "", "Claude session ID to resume")

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
		fmt.Println("  agent-deck launch . -c claude -m \"Fix bug\" --no-wait")
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
	sessionCommand := mergeFlags(*command, *commandShort)
	sessionParent := mergeFlags(*parent, *parentShort)
	initialMessage := mergeFlags(*message, *messageShort)

	// Resolve worktree flags
	wtBranch := *worktreeBranch
	if *worktreeBranchLong != "" {
		wtBranch = *worktreeBranchLong
	}
	createNewBranch := *newBranch || *newBranchLong

	// Validate --resume-session requires Claude
	if *resumeSession != "" {
		tool := detectTool(sessionCommand)
		if tool != "claude" {
			out.Error("--resume-session only works with Claude sessions (-c claude)", ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Handle worktree creation
	var worktreePath, worktreeRepoRoot string
	if wtBranch != "" {
		if !git.IsGitRepo(path) {
			out.Error(fmt.Sprintf("%s is not a git repository", path), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		repoRoot, err := git.GetWorktreeBaseRoot(path)
		if err != nil {
			out.Error(fmt.Sprintf("failed to get repo root: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		if err := git.ValidateBranchName(wtBranch); err != nil {
			out.Error(fmt.Sprintf("invalid branch name: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		branchExists := git.BranchExists(repoRoot, wtBranch)
		if createNewBranch && branchExists {
			out.Error(fmt.Sprintf("branch '%s' already exists (remove -b flag to use existing branch)", wtBranch), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		wtSettings := session.GetWorktreeSettings()
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

		if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
			out.Error(fmt.Sprintf("failed to create parent directory: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		if _, err := os.Stat(worktreePath); err == nil {
			out.Error(fmt.Sprintf("worktree already exists at %s", worktreePath), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		if err := git.CreateWorktree(repoRoot, worktreePath, wtBranch); err != nil {
			out.Error(fmt.Sprintf("failed to create worktree: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
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
		sessionGroup = parentInstance.GroupPath
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

	if parentInstance != nil {
		newInstance.SetParentWithPath(parentInstance.ID, parentInstance.ProjectPath)
	}

	if sessionCommand != "" {
		newInstance.Tool = detectTool(sessionCommand)
		if toolDef := session.GetToolDef(newInstance.Tool); toolDef != nil {
			newInstance.Command = toolDef.Command
		} else {
			newInstance.Command = sessionCommand
		}
	}

	if *wrapper != "" {
		newInstance.Wrapper = *wrapper
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

	// Start the session (with or without initial message)
	if initialMessage != "" {
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
	if err := saveSessionData(storage, instances); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Send message if provided and StartWithMessage wasn't used
	// (StartWithMessage uses the deferred send mechanism; for --no-wait we send directly)
	if initialMessage != "" && *noWait {
		tmuxSess := newInstance.GetTmuxSession()
		if tmuxSess != nil {
			// Wait briefly for agent to initialize, then send without retry
			time.Sleep(500 * time.Millisecond)
			_ = tmuxSess.SendKeysAndEnter(initialMessage)
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
	if initialMessage != "" {
		jsonData["message"] = initialMessage
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
		msg += " (message pending)"
	}
	out.Success(msg, jsonData)
}
