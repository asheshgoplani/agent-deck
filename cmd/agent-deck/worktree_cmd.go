package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
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
	case "trust-hooks", "trust-scripts": // trust-scripts: pre-1.16.22 name
		exitCLI(runWorktreeTrustHooks(args[1:], os.Stdin, os.Stdout, os.Stderr, stdinStdoutIsTerminal()))
	case "help", "-h", "--help":
		printWorktreeUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown worktree command: %s\n", args[0])
		printWorktreeUsage()
		exitCLI(1)
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
	fmt.Println("  trust-hooks <repo> [--hook setup|destruction] [--yes] [--revoke]")
	fmt.Println("                    Review and approve .agent-deck/worktree-*.sh hooks for a repo")
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
	fmt.Println("  agent-deck worktree trust-hooks .")
	fmt.Println("  agent-deck worktree trust-hooks . --hook setup --yes")
	fmt.Println("  agent-deck worktree trust-hooks . --revoke")
}

// partitionWorktreeTrustScriptsArgs splits args into flag tokens and
// positional tokens so `agent-deck worktree trust-hooks` accepts a flag
// in any position relative to the repo-path positional.
//
// Go's stdlib flag.Parse stops consuming flags at the first non-flag
// argument. Our own usage text prints "trust-hooks . --revoke" (repo path
// before the flag) — under a plain fs.Parse(args), "." is seen first,
// parsing stops immediately, and "--revoke" is swallowed into the
// positional args instead of being recognized, so *revoke stays false and
// the command silently GRANTS trust instead of revoking it. Partitioning
// args by leading "-" before handing them to flag.Parse fixes that: flag
// position relative to the repo-path positional no longer matters.
//
// A literal "--" is honored as the conventional end-of-flags marker
// (matching flag.Parse's own behavior): everything after it is treated as
// positional even if it starts with "-", so a repo path that itself begins
// with a dash can still be passed via `trust-hooks --revoke -- -repo`.
// --hook is the only value-taking flag; its separate value token (`--hook
// setup`) stays with it.
func partitionWorktreeTrustScriptsArgs(args []string) (flagArgs, positional []string) {
	endOfFlags := false
	takeValue := false
	for _, a := range args {
		switch {
		case takeValue:
			flagArgs = append(flagArgs, a)
			takeValue = false
		case endOfFlags:
			positional = append(positional, a)
		case a == "--":
			endOfFlags = true
		case strings.HasPrefix(a, "-") && a != "-":
			flagArgs = append(flagArgs, a)
			takeValue = a == "--hook" || a == "-hook"
		default:
			positional = append(positional, a)
		}
	}
	return flagArgs, positional
}

// runWorktreeTrustHooks reviews and pre-approves (or revokes approval for)
// a repository's .agent-deck/worktree-setup.sh and worktree-destruction.sh.
// Each hook's identity (path, symlink target, interpreter, sha256, first
// lines) is printed before it is trusted; on a terminal the user confirms
// with y/N, otherwise --yes is required so a script cannot trust a hook
// nobody looked at by accident. Returns the process exit code.
func runWorktreeTrustHooks(args []string, in io.Reader, out, errOut io.Writer, interactive bool) int {
	flagArgs, positional := partitionWorktreeTrustScriptsArgs(args)

	fs := flag.NewFlagSet("worktree trust-hooks", flag.ContinueOnError)
	fs.SetOutput(errOut)
	revoke := fs.Bool("revoke", false, "Remove previously stored trust instead of granting it")
	hook := fs.String("hook", "", "Only this hook: setup or destruction (default: both)")
	yes := fs.Bool("yes", false, "Trust without asking (for scripts; the hook is still printed)")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	if len(positional) < 1 {
		fmt.Fprintln(errOut, "Usage: agent-deck worktree trust-hooks <repo-path> [--hook setup|destruction] [--yes] [--revoke]")
		return 1
	}
	kinds := []string{"setup", "destruction"}
	switch *hook {
	case "":
	case "setup", "destruction":
		kinds = []string{*hook}
	default:
		fmt.Fprintf(errOut, "Error: --hook must be setup or destruction, got %q\n", *hook)
		return 1
	}

	repoRoot, err := git.GetRepoRoot(positional[0])
	if err != nil {
		fmt.Fprintf(errOut, "Error: %s is not inside a git repository: %v\n", positional[0], err)
		return 1
	}

	reader := bufio.NewReader(in)
	found := 0
	for _, kind := range kinds {
		if *revoke {
			// Always attempt revocation, independent of whether the script
			// still exists on disk right now — the trust store can hold an
			// entry for a script that was since deleted or renamed, and
			// that entry must still be removable via the CLI rather than
			// becoming permanently stale.
			existed, err := git.RevokeScriptConsent(repoRoot, kind)
			if err != nil {
				fmt.Fprintf(errOut, "Error revoking trust for %s hook in %s: %v\n", kind, repoRoot, err)
				return 1
			}
			if existed {
				found++
				fmt.Fprintf(out, "Revoked trust for the %s hook in %s\n", kind, repoRoot)
			}
			continue
		}

		id, err := git.InspectWorktreeScript(repoRoot, kind)
		if err != nil {
			fmt.Fprintf(errOut, "Error reading %s hook: %v\n", kind, err)
			return 1
		}
		if id == nil {
			continue
		}
		found++
		status, err := git.WorktreeScriptTrustStatus(id)
		if err != nil {
			fmt.Fprintf(errOut, "Warning: could not read the hook trust store: %v\n", err)
		}
		fmt.Fprintf(out, "Worktree %s hook:\n%s", kind, git.DescribeWorktreeScript(*id))
		if status == git.ScriptTrusted {
			fmt.Fprintf(out, "Already trusted (sha256:%s).\n\n", id.ShortHash())
			continue
		}
		if !*yes {
			if !interactive {
				fmt.Fprintf(errOut, "Error: not trusting the %s hook without confirmation: run this on a terminal, or pass --yes after reviewing it\n", kind)
				return 1
			}
			fmt.Fprintf(out, "Trust this version of the %s hook? [y/N] ", kind)
			line, _ := reader.ReadString('\n')
			if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
				fmt.Fprintf(out, "Not trusted.\n\n")
				continue
			}
		}
		if err := git.TrustWorktreeScript(id); err != nil {
			fmt.Fprintf(errOut, "Error trusting %s: %v\n", id.ScriptPath, err)
			return 1
		}
		fmt.Fprintf(out, "Trusted the %s hook (sha256:%s, %s).\n\n", kind, id.ShortHash(), id.Interpreter)
	}

	if found == 0 {
		if *revoke {
			fmt.Fprintf(out, "No stored trust found for worktree hooks under %s\n", repoRoot)
		} else {
			fmt.Fprintf(out, "No .agent-deck/worktree-setup.sh or worktree-destruction.sh found under %s\n", repoRoot)
		}
	}
	return 0
}

// handleWorktreeList lists all worktrees with session associations
func handleWorktreeList(profile string, args []string) {
	fs := flag.NewFlagSet("worktree list", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck worktree list [options]")
		fmt.Println()
		fmt.Println("List all git worktrees in the current repository with session associations.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	out := NewCLIOutput(*jsonOutput, false)

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		out.Error(fmt.Sprintf("failed to get current directory: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	backend, err := detectAndCreateBackend(cwd)
	if err != nil {
		out.Error(fmt.Sprintf("%v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	repoRoot := backend.RepoDir()

	// List worktrees
	worktrees, err := backend.ListWorktrees()
	if err != nil {
		out.Error(fmt.Sprintf("failed to list worktrees: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Load sessions
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Build session map: LOCAL path -> session. A remote session contributes
	// nothing: its ProjectPath is a placeholder, not a checkout, so listing it
	// here labels a local worktree with a session running on another host
	// (#1852 site 2).
	sessionByPath := make(map[string]*session.Instance)
	for _, inst := range instances {
		if loc := locationOf(inst); loc.IsLocal() && loc.Path != "" {
			sessionByPath[loc.Path] = inst
		}
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
	fs := flag.NewFlagSet("worktree info", flag.ContinueOnError)
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

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	out := NewCLIOutput(*jsonOutput, false)

	if identifier == "" {
		out.Error("session identifier is required", ErrCodeNotFound)
		fmt.Println()
		fs.Usage()
		exitCLI(1)
	}

	// Load sessions
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		exitCLI(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if session has worktree info
	if !inst.IsWorktree() {
		out.Error(fmt.Sprintf("session '%s' is not in a worktree", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
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
	fs := flag.NewFlagSet("worktree cleanup", flag.ContinueOnError)
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

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	out := NewCLIOutput(*jsonOutput, false)

	// Load sessions
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		exitCLI(1)
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
		exitCLI(1)
	}

	// Find orphaned worktrees (exist but no session points to them)
	var orphanedWorktrees []vcs.Worktree
	var protectedWorktrees []worktreeCleanupFacts
	var cleanupBackend vcs.Backend

	if backend, bErr := detectAndCreateBackend(cwd); bErr == nil {
		cleanupBackend = backend
		worktrees, wErr := cleanupBackend.ListWorktrees()
		if wErr == nil {
			// Build the set of LOCAL paths that sessions occupy. This is the
			// harmful direction of #1852 site 3: a remote session's placeholder
			// inserted here makes a genuinely orphaned worktree look in-use, so
			// cleanup silently skips it forever.
			sessionPaths, err := allProfileSessionPaths(profile, instances)
			if err != nil {
				out.Error(err.Error(), ErrCodeInvalidOperation)
				exitCLI(1)
			}

			orphanedWorktrees, protectedWorktrees = classifyUnregisteredWorktrees(worktrees, sessionPaths)
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
		protectedWorktreeData := make([]map[string]interface{}, 0, len(protectedWorktrees))
		for _, facts := range protectedWorktrees {
			protectedWorktreeData = append(protectedWorktreeData, facts.jsonData())
		}

		result := map[string]interface{}{
			"orphaned_sessions":   orphanedSessionData,
			"orphaned_worktrees":  orphanedWorktreeData,
			"protected_worktrees": protectedWorktreeData,
			"dry_run":             !*force,
		}

		out.Print("", result)

		if !*force {
			return
		}
	}

	// Human-readable output
	if !*jsonOutput {
		if len(orphanedSessions) == 0 && len(orphanedWorktrees) == 0 && len(protectedWorktrees) == 0 {
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

		if len(protectedWorktrees) > 0 {
			fmt.Println("Protected Worktrees (not orphans; cleanup will not remove):")
			for _, facts := range protectedWorktrees {
				fmt.Printf("  - %s (branch: %s; %s)\n", FormatPath(facts.Worktree.Path), facts.Worktree.Branch, facts.summary())
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

		// Save updated session data
		if err := saveSessionData(storage, remaining, groups); err != nil {
			out.Error(fmt.Sprintf("failed to save session data: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}

	// Remove orphaned worktrees
	removedWorktrees := 0
	for _, wt := range orphanedWorktrees {
		if cleanupBackend == nil {
			fmt.Fprintf(os.Stderr, "Warning: no VCS backend for worktree removal\n")
			break
		}
		// Confirmation may wait indefinitely. Reload both registry ownership and
		// repository state, then inspect the candidate again at the destructive
		// boundary. --force authorizes removal; it never bypasses this gate.
		removed, skipReason, removeErr := removeCleanupCandidate(cleanupBackend, wt, func() (map[string]bool, error) {
			currentStorage, currentInstances, _, err := loadSessionData(profile)
			if err != nil {
				return nil, err
			}
			defer currentStorage.Close()
			return allProfileSessionPaths(profile, currentInstances)
		})
		if !removed {
			if removeErr != nil {
				fmt.Printf("Skipped worktree: %s (%s: %v)\n", FormatPath(wt.Path), skipReason, removeErr)
			} else {
				fmt.Printf("Skipped worktree: %s (%s)\n", FormatPath(wt.Path), skipReason)
			}
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
	fs := flag.NewFlagSet("worktree finish", flag.ContinueOnError)
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

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	out := NewCLIOutput(*jsonOutput, false)

	if identifier == "" {
		out.Error("session identifier is required", ErrCodeNotFound)
		fmt.Println()
		fs.Usage()
		exitCLI(1)
	}

	// Load sessions
	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		exitCLI(1)
		return
	}

	// Validate it's a worktree session
	if !inst.IsWorktree() {
		out.Error(fmt.Sprintf("session '%s' is not in a worktree", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	repoRoot := inst.WorktreeRepoRoot
	worktreePath := inst.WorktreePath
	worktreeBranch := inst.WorktreeBranch

	finishBackend, err := detectAndCreateBackend(repoRoot)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize VCS: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Check for uncommitted changes (uses worktree path, not repoDir — stays standalone)
	if !*force {
		dirty, err := git.HasUncommittedChanges(worktreePath)
		if err != nil {
			// Worktree dir might be gone already
			if _, statErr := os.Stat(worktreePath); os.IsNotExist(statErr) {
				// Worktree directory is gone, skip the check
				dirty = false
			} else {
				out.Error(fmt.Sprintf("failed to check worktree status: %v", err), ErrCodeInvalidOperation)
				exitCLI(1)
			}
		}
		if dirty {
			out.Error("worktree has uncommitted changes (use --force to override)", ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}

	// Determine target branch
	targetBranch := *into
	if targetBranch == "" && !*noMerge {
		targetBranch, err = finishBackend.GetDefaultBranch()
		if err != nil {
			out.Error(fmt.Sprintf("could not determine target branch: %v\nUse --into <branch> to specify", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}

	// Validate target != source
	if !*noMerge && targetBranch == worktreeBranch {
		out.Error(fmt.Sprintf("cannot merge branch '%s' into itself", worktreeBranch), ErrCodeInvalidOperation)
		exitCLI(1)
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
			exitCLI(1)
		}

		// Merge the worktree branch
		if err := finishBackend.MergeBranch(worktreeBranch); err != nil {
			// Abort the merge to leave things clean (git-specific)
			if finishBackend.Type() == vcs.TypeGit {
				abortCmd := exec.Command("git", "-C", repoRoot, "merge", "--abort")
				_ = abortCmd.Run()
			}
			out.Error(fmt.Sprintf("merge failed (aborted): %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		fmt.Printf("  %s Merged successfully\n", successSymbol)
	}

	// Step 2: Remove worktree
	if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
		fmt.Printf("Removing worktree at %s...\n", FormatPath(worktreePath))
		if err := finishBackend.RemoveWorktree(worktreePath, *force); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove worktree: %v\n", err)
		} else {
			fmt.Printf("  %s Worktree removed\n", successSymbol)
		}
	}
	_ = finishBackend.PruneWorktrees()

	// Step 3: Delete branch (if not --keep-branch)
	if !*keepBranch {
		fmt.Printf("Deleting branch %s...\n", worktreeBranch)
		if err := finishBackend.DeleteBranch(worktreeBranch, *force); err != nil {
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

	// Step 5: Remove session from agent-deck.
	//
	// #1396: this must use the targeted RemoveSessionAndVerify path (the same
	// one `session remove` uses), NOT saveSessionData/SaveWithGroups.
	// Historically SaveWithGroups(remaining) with an empty `remaining` tripped
	// the S1 empty-sweep guard AFTER the irreversible git steps, orphaning the
	// row; since #1550 SaveWithGroups is upsert-only and would not delete the
	// row at all. Either way, removal requires the targeted DELETE.
	remaining := dropInstance(instances, inst.ID)
	groupTree := session.NewGroupTreeWithGroups(remaining, groups)
	if err := storage.RemoveSessionAndVerify(inst.ID, remaining, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save session data: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Issue #1576: sweep transition-notifier state for the removed session,
	// mirroring the #910 cleanup on `agent-deck rm` / `session remove`.
	// Without this, `worktree finish` leaves orphan records in
	// runtime/transition-notify-state.json and stale inbox JSONL lines that
	// keep re-firing [EVENT] deliveries to the parent conductor. Best-effort:
	// failures warn but never block the finish (the SQLite removal above is
	// the user-visible contract).
	if swept, err := session.SweepInboxesForChildSession(inst.ID); err != nil && !*jsonOutput {
		fmt.Fprintf(os.Stderr, "warn: inbox sweep for %s failed: %v\n", inst.ID, err)
	} else if swept > 0 && !*jsonOutput {
		fmt.Fprintf(os.Stderr, "swept %d stale inbox event(s) for removed session\n", swept)
	}
	if _, err := session.RemoveNotifyStateRecord(inst.ID); err != nil && !*jsonOutput {
		fmt.Fprintf(os.Stderr, "warn: notify-state sweep for %s failed: %v\n", inst.ID, err)
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

// worktreeLocationAndTemplate picks the location and path template to pass to
// vcs.WorktreePathOptions for a new worktree.
//
// An explicit --location flag wins over a configured or inherited
// path_template (#2093): WorktreePath ignores Location whenever Template is
// non-empty, so the template has to be cleared for the flag to take effect.
func worktreeLocationAndTemplate(settings session.WorktreeSettings, explicitLocation string) (location, template string) {
	if explicitLocation != "" {
		return explicitLocation, ""
	}
	return settings.DefaultLocation, settings.Template()
}
