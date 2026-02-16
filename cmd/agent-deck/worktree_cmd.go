package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleWorktree dispatches worktree subcommands
func handleWorktree(profile string, args []string) {
	if len(args) == 0 {
		printWorktreeUsage()
		return
	}

	switch args[0] {
	case "list", "ls":
		handleWorktreeList(profile, args[1:])
	case "info":
		handleWorktreeInfo(profile, args[1:])
	case "cleanup":
		handleWorktreeCleanup(profile, args[1:])
	case "finish":
		handleWorktreeFinish(profile, args[1:])
	case "help", "-h", "--help":
		printWorktreeUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown worktree command: %s\n", args[0])
		printWorktreeUsage()
		os.Exit(1)
	}
}

// printWorktreeUsage prints help for worktree commands
func printWorktreeUsage() {
	fmt.Println("Usage: agent-deck worktree <command> [options]")
	fmt.Println()
	fmt.Println("Manage git worktrees and their session associations.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list              List all worktrees in current repository")
	fmt.Println("  info <session>    Show worktree info for a session")
	fmt.Println("  finish <session>  Merge branch, remove worktree, and delete session")
	fmt.Println("  cleanup [--force] Find and remove orphaned worktrees/sessions")
	fmt.Println()
	fmt.Println("Global Options:")
	fmt.Println("  -p, --profile <name>   Use specific profile")
	fmt.Println("  --json                 Output as JSON")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck worktree list")
	fmt.Println("  agent-deck worktree list --json")
	fmt.Println("  agent-deck worktree info \"My Session\"")
	fmt.Println("  agent-deck worktree finish \"My Session\"")
	fmt.Println("  agent-deck worktree finish \"My Session\" --no-merge")
	fmt.Println("  agent-deck worktree finish \"My Session\" --into develop")
	fmt.Println("  agent-deck worktree cleanup")
	fmt.Println("  agent-deck worktree cleanup --force")
}

// handleWorktreeList lists all worktrees with session associations
func handleWorktreeList(profile string, args []string) {
	fs := flag.NewFlagSet("worktree list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck worktree list [options]")
		fmt.Println()
		fmt.Println("List all git worktrees in the current repository with session associations.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(*jsonOutput, false)

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		out.Error(fmt.Sprintf("failed to get current directory: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Check if in a git repo
	if !git.IsGitRepo(cwd) {
		out.Error("not in a git repository", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Get repo root (resolve through worktrees to prevent nesting)
	repoRoot, err := git.GetWorktreeBaseRoot(cwd)
	if err != nil {
		out.Error(fmt.Sprintf("failed to get repo root: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// List worktrees
	worktrees, err := git.ListWorktrees(repoRoot)
	if err != nil {
		out.Error(fmt.Sprintf("failed to list worktrees: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Load sessions
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Build session map: path -> session title
	sessionByPath := make(map[string]*session.Instance)
	for _, inst := range instances {
		sessionByPath[inst.ProjectPath] = inst
		if inst.WorktreePath != "" {
			sessionByPath[inst.WorktreePath] = inst
		}
	}

	// Build output data
	type worktreeInfo struct {
		Path    string `json:"path"`
		Branch  string `json:"branch"`
		Type    string `json:"type"` // "main" or "worktree"
		Session string `json:"session,omitempty"`
	}

	var results []worktreeInfo

	for i, wt := range worktrees {
		info := worktreeInfo{
			Path:   wt.Path,
			Branch: wt.Branch,
		}

		// First worktree is typically the main repo
		if i == 0 {
			info.Type = "main"
		} else {
			info.Type = "worktree"
		}

		// Find associated session
		if inst := sessionByPath[wt.Path]; inst != nil {
			info.Session = inst.Title
		}

		results = append(results, info)
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"repo_root": repoRoot,
			"worktrees": results,
			"count":     len(results),
		})
		return
	}

	// Human-readable output
	if len(results) == 0 {
		fmt.Println("No worktrees found.")
		return
	}

	fmt.Printf("Repository: %s\n\n", FormatPath(repoRoot))
	fmt.Printf("%-40s  %-20s  %-10s  %s\n", "PATH", "BRANCH", "TYPE", "SESSION")
	fmt.Printf("%-40s  %-20s  %-10s  %s\n", strings.Repeat("-", 40), strings.Repeat("-", 20), strings.Repeat("-", 10), strings.Repeat("-", 20))

	for _, wt := range results {
		sessionStr := wt.Session
		if sessionStr == "" {
			sessionStr = "-"
		}
		fmt.Printf("%-40s  %-20s  %-10s  %s\n",
			truncateString(FormatPath(wt.Path), 40),
			truncateString(wt.Branch, 20),
			wt.Type,
			truncateString(sessionStr, 20))
	}

	fmt.Printf("\nTotal: %d worktree(s)\n", len(results))
}

// handleWorktreeInfo shows worktree info for a specific session
func handleWorktreeInfo(profile string, args []string) {
	fs := flag.NewFlagSet("worktree info", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck worktree info <session> [options]")
		fmt.Println()
		fmt.Println("Show worktree information for a session.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  session    Session title, ID prefix, or path")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	out := NewCLIOutput(*jsonOutput, false)

	if identifier == "" {
		out.Error("session identifier is required", ErrCodeNotFound)
		fmt.Println()
		fs.Usage()
		os.Exit(1)
	}

	// Load sessions
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if session has worktree info
	if !inst.IsWorktree() {
		out.Error(fmt.Sprintf("session '%s' is not in a worktree", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Check if worktree still exists
	worktreeExists := false
	if _, err := os.Stat(inst.WorktreePath); err == nil {
		worktreeExists = true
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"session":         inst.Title,
			"session_id":      inst.ID,
			"branch":          inst.WorktreeBranch,
			"worktree_path":   inst.WorktreePath,
			"main_repo":       inst.WorktreeRepoRoot,
			"worktree_exists": worktreeExists,
		})
		return
	}

	// Human-readable output
	fmt.Printf("Session:        %s\n", inst.Title)
	fmt.Printf("Branch:         %s\n", inst.WorktreeBranch)
	fmt.Printf("Worktree Path:  %s\n", FormatPath(inst.WorktreePath))
	fmt.Printf("Main Repo:      %s\n", FormatPath(inst.WorktreeRepoRoot))

	if worktreeExists {
		fmt.Printf("Status:         exists\n")
	} else {
		fmt.Printf("Status:         MISSING (worktree directory not found)\n")
	}
}

// handleWorktreeCleanup finds and removes orphaned worktrees and sessions
func handleWorktreeCleanup(profile string, args []string) {
	fs := flag.NewFlagSet("worktree cleanup", flag.ExitOnError)
	force := fs.Bool("force", false, "Actually remove orphans (default is dry-run)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck worktree cleanup [options]")
		fmt.Println()
		fmt.Println("Find and remove orphaned worktrees and sessions.")
		fmt.Println()
		fmt.Println("Orphans are detected as:")
		fmt.Println("  - Sessions with WorktreePath set but the directory doesn't exist")
		fmt.Println("  - Worktrees that exist but no session points to them")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("By default, runs in dry-run mode (shows what would be removed).")
		fmt.Println("Use --force to actually perform the cleanup.")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(*jsonOutput, false)

	// Load sessions
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	// Find orphaned sessions (WorktreePath set but directory doesn't exist)
	var orphanedSessions []*session.Instance
	for _, inst := range instances {
		if inst.WorktreePath != "" {
			if _, err := os.Stat(inst.WorktreePath); os.IsNotExist(err) {
				orphanedSessions = append(orphanedSessions, inst)
			}
		}
	}

	// Get current working directory for worktree scan
	cwd, err := os.Getwd()
	if err != nil {
		out.Error(fmt.Sprintf("failed to get current directory: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Find orphaned worktrees (exist but no session points to them)
	var orphanedWorktrees []git.Worktree
	var repoRoot string

	if git.IsGitRepo(cwd) {
		repoRoot, err = git.GetWorktreeBaseRoot(cwd)
		if err == nil {
			worktrees, err := git.ListWorktrees(repoRoot)
			if err == nil {
				// Build set of paths that sessions use
				sessionPaths := make(map[string]bool)
				for _, inst := range instances {
					sessionPaths[inst.ProjectPath] = true
					if inst.WorktreePath != "" {
						sessionPaths[inst.WorktreePath] = true
					}
				}

				// Check each worktree (skip the first one which is usually the main repo)
				for i, wt := range worktrees {
					if i == 0 {
						continue // Skip main repo
					}
					if !sessionPaths[wt.Path] {
						orphanedWorktrees = append(orphanedWorktrees, wt)
					}
				}
			}
		}
	}

	// JSON output
	if *jsonOutput {
		orphanedSessionData := make([]map[string]string, 0, len(orphanedSessions))
		for _, inst := range orphanedSessions {
			orphanedSessionData = append(orphanedSessionData, map[string]string{
				"id":            inst.ID,
				"title":         inst.Title,
				"worktree_path": inst.WorktreePath,
			})
		}

		orphanedWorktreeData := make([]map[string]string, 0, len(orphanedWorktrees))
		for _, wt := range orphanedWorktrees {
			orphanedWorktreeData = append(orphanedWorktreeData, map[string]string{
				"path":   wt.Path,
				"branch": wt.Branch,
			})
		}

		result := map[string]interface{}{
			"orphaned_sessions":  orphanedSessionData,
			"orphaned_worktrees": orphanedWorktreeData,
			"dry_run":            !*force,
		}

		out.Print("", result)

		if !*force {
			return
		}
	}

	// Human-readable output
	if !*jsonOutput {
		if len(orphanedSessions) == 0 && len(orphanedWorktrees) == 0 {
			fmt.Println("No orphans found. Everything is clean!")
			return
		}

		if len(orphanedSessions) > 0 {
			fmt.Println("Orphaned Sessions (worktree directory missing):")
			for _, inst := range orphanedSessions {
				fmt.Printf("  - %s (worktree: %s)\n", inst.Title, FormatPath(inst.WorktreePath))
			}
			fmt.Println()
		}

		if len(orphanedWorktrees) > 0 {
			fmt.Println("Orphaned Worktrees (no session associated):")
			for _, wt := range orphanedWorktrees {
				fmt.Printf("  - %s (branch: %s)\n", FormatPath(wt.Path), wt.Branch)
			}
			fmt.Println()
		}
	}

	// If not force mode, show what would be done
	if !*force {
		fmt.Println("This is a dry run. Use --force to actually remove orphans.")
		return
	}

	// Confirm before proceeding
	fmt.Printf("\nThis will remove %d session(s) and %d worktree(s). Continue? [y/N]: ",
		len(orphanedSessions), len(orphanedWorktrees))

	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response != "y" && response != "yes" {
		fmt.Println("Aborted.")
		return
	}

	// Remove orphaned sessions
	removedSessions := 0
	for _, inst := range orphanedSessions {
		// Kill tmux session if it exists
		if inst.Exists() {
			if err := inst.Kill(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to kill tmux session %s: %v\n", inst.Title, err)
			}
		}
		removedSessions++
		fmt.Printf("Removed session: %s\n", inst.Title)
	}

	// Filter out removed sessions from instances
	if removedSessions > 0 {
		var remaining []*session.Instance
		removedIDs := make(map[string]bool)
		for _, inst := range orphanedSessions {
			removedIDs[inst.ID] = true
		}
		for _, inst := range instances {
			if !removedIDs[inst.ID] {
				remaining = append(remaining, inst)
			}
		}

		// Save updated session data (uses existing saveSessionData which rebuilds GroupTree)
		if err := saveSessionData(storage, remaining); err != nil {
			out.Error(fmt.Sprintf("failed to save session data: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Remove orphaned worktrees
	removedWorktrees := 0
	for _, wt := range orphanedWorktrees {
		if err := git.RemoveWorktree(repoRoot, wt.Path, false); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove worktree %s: %v\n", wt.Path, err)
			continue
		}
		removedWorktrees++
		fmt.Printf("Removed worktree: %s\n", FormatPath(wt.Path))
	}

	fmt.Printf("\nCleanup complete: removed %d session(s), %d worktree(s)\n",
		removedSessions, removedWorktrees)
}

// handleWorktreeFinish merges a worktree branch, removes the worktree, and deletes the session
func handleWorktreeFinish(profile string, args []string) {
	fs := flag.NewFlagSet("worktree finish", flag.ExitOnError)
	into := fs.String("into", "", "Target branch to merge into (default: auto-detect)")
	noMerge := fs.Bool("no-merge", false, "Skip merge (e.g. for PR workflows)")
	keepBranch := fs.Bool("keep-branch", false, "Don't delete local branch after finish")
	force := fs.Bool("force", false, "Skip safety checks and force branch deletion")
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck worktree finish <session> [options]")
		fmt.Println()
		fmt.Println("Merge a worktree branch, remove the worktree, and delete the session.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  session    Session title, ID prefix, or path")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck worktree finish \"My Feature\"")
		fmt.Println("  agent-deck worktree finish \"My Feature\" --into develop")
		fmt.Println("  agent-deck worktree finish \"My Feature\" --no-merge")
		fmt.Println("  agent-deck worktree finish \"My Feature\" --no-merge --force")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	out := NewCLIOutput(*jsonOutput, false)

	if identifier == "" {
		out.Error("session identifier is required", ErrCodeNotFound)
		fmt.Println()
		fs.Usage()
		os.Exit(1)
	}

	// Load sessions
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(1)
		return
	}

	// Validate it's a worktree session
	if !inst.IsWorktree() {
		out.Error(fmt.Sprintf("session '%s' is not in a worktree", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	repoRoot := inst.WorktreeRepoRoot
	worktreePath := inst.WorktreePath
	worktreeBranch := inst.WorktreeBranch

	// Check for uncommitted changes
	if !*force {
		dirty, err := git.HasUncommittedChanges(worktreePath)
		if err != nil {
			// Worktree dir might be gone already
			if _, statErr := os.Stat(worktreePath); os.IsNotExist(statErr) {
				// Worktree directory is gone, skip the check
				dirty = false
			} else {
				out.Error(fmt.Sprintf("failed to check worktree status: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
		}
		if dirty {
			out.Error("worktree has uncommitted changes (use --force to override)", ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Determine target branch
	targetBranch := *into
	if targetBranch == "" && !*noMerge {
		targetBranch, err = git.GetDefaultBranch(repoRoot)
		if err != nil {
			out.Error(fmt.Sprintf("could not determine target branch: %v\nUse --into <branch> to specify", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Validate target != source
	if !*noMerge && targetBranch == worktreeBranch {
		out.Error(fmt.Sprintf("cannot merge branch '%s' into itself", worktreeBranch), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Show summary and confirm
	if !*force && !*jsonOutput {
		fmt.Printf("Session:   %s\n", inst.Title)
		fmt.Printf("Branch:    %s\n", worktreeBranch)
		fmt.Printf("Worktree:  %s\n", FormatPath(worktreePath))
		if *noMerge {
			fmt.Printf("Merge:     skipped (--no-merge)\n")
		} else {
			fmt.Printf("Merge:     %s → %s\n", worktreeBranch, targetBranch)
		}
		if *keepBranch {
			fmt.Printf("Branch:    kept (--keep-branch)\n")
		} else {
			fmt.Printf("Delete:    branch '%s' will be deleted\n", worktreeBranch)
		}
		fmt.Println()
		fmt.Print("Proceed? [y/N]: ")

		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			fmt.Println("Aborted.")
			return
		}
		fmt.Println()
	}

	// Step 1: Merge (if requested)
	if !*noMerge {
		fmt.Printf("Merging %s into %s...\n", worktreeBranch, targetBranch)

		// Checkout target branch in main repo
		cmd := exec.Command("git", "-C", repoRoot, "checkout", targetBranch)
		checkoutOutput, err := cmd.CombinedOutput()
		if err != nil {
			out.Error(fmt.Sprintf("failed to checkout %s: %s", targetBranch, strings.TrimSpace(string(checkoutOutput))), ErrCodeInvalidOperation)
			os.Exit(1)
		}

		// Merge the worktree branch
		if err := git.MergeBranch(repoRoot, worktreeBranch); err != nil {
			// Abort the merge to leave things clean
			abortCmd := exec.Command("git", "-C", repoRoot, "merge", "--abort")
			_ = abortCmd.Run()
			out.Error(fmt.Sprintf("merge failed (aborted): %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		fmt.Printf("  %s Merged successfully\n", successSymbol)
	}

	// Step 2: Remove worktree
	if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
		fmt.Printf("Removing worktree at %s...\n", FormatPath(worktreePath))
		if err := git.RemoveWorktree(repoRoot, worktreePath, *force); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove worktree: %v\n", err)
		} else {
			fmt.Printf("  %s Worktree removed\n", successSymbol)
		}
	}
	_ = git.PruneWorktrees(repoRoot)

	// Step 3: Delete branch (if not --keep-branch)
	if !*keepBranch {
		fmt.Printf("Deleting branch %s...\n", worktreeBranch)
		if err := git.DeleteBranch(repoRoot, worktreeBranch, *force); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to delete branch: %v\n", err)
		} else {
			fmt.Printf("  %s Branch deleted\n", successSymbol)
		}
	}

	// Step 4: Kill tmux session
	if inst.Exists() {
		if err := inst.Kill(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to kill tmux session: %v\n", err)
		}
	}

	// Step 5: Remove session from agent-deck
	var remaining []*session.Instance
	for _, i := range instances {
		if i.ID != inst.ID {
			remaining = append(remaining, i)
		}
	}
	if err := saveSessionData(storage, remaining); err != nil {
		out.Error(fmt.Sprintf("failed to save session data: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"success":        true,
			"session":        inst.Title,
			"session_id":     inst.ID,
			"branch":         worktreeBranch,
			"merged_into":    targetBranch,
			"merged":         !*noMerge,
			"branch_deleted": !*keepBranch,
		})
	} else {
		fmt.Printf("\n%s Finished: session '%s' removed, worktree cleaned up", successSymbol, inst.Title)
		if !*noMerge {
			fmt.Printf(", branch merged into %s", targetBranch)
		}
		fmt.Println()
	}
}

// truncateString truncates a string to maxLen, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
