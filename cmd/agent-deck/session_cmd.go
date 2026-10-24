package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/jujutsu"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

// handleSession dispatches session subcommands
func handleSession(profile string, args []string) {
	if len(args) == 0 {
		printSessionHelp()
		exitCLI(1)
	}

	switch args[0] {
	case "start":
		if coreRegistryEnabled() {
			cliSessionStart(profile, args[1:])
		} else {
			handleSessionStart(profile, args[1:])
		}
	case "stop":
		if coreRegistryEnabled() {
			cliSessionStop(profile, args[1:])
		} else {
			handleSessionStop(profile, args[1:])
		}
	case "remove":
		handleSessionRemove(profile, args[1:])
	case "cleanup", "prune":
		handleSessionCleanup(profile, args[1:])
	case "archive":
		handleSessionArchive(profile, args[1:])
	case "unarchive":
		handleSessionUnarchive(profile, args[1:])
	case "restart":
		if coreRegistryEnabled() {
			cliSessionRestart(profile, args[1:])
		} else {
			handleSessionRestart(profile, args[1:])
		}
	case "revive":
		handleSessionRevive(profile, args[1:])
	case "fork":
		handleSessionFork(profile, args[1:])
	case "handoff":
		handleSessionHandoff(profile, args[1:])
	case "attach":
		handleSessionAttach(profile, args[1:])
	case "focus":
		handleSessionFocus(profile, args[1:])
	case "recent":
		handleSessionRecent(profile, args[1:])
	case "show":
		handleSessionShow(profile, args[1:])
	case "viewers":
		handleSessionViewers(profile, args[1:])
	case "current":
		handleSessionCurrent(profile, args[1:])
	case "primer":
		handleSessionPrimer(profile, args[1:])
	case "set-parent":
		handleSessionSetParent(profile, args[1:])
	case "unset-parent":
		handleSessionUnsetParent(profile, args[1:])
	case "update":
		// Issue #974: users expect `session update <id> --no-parent` and
		// `session update <id> --parent <p>` to mirror typical CRUD verbs.
		// Route to the existing canonical handlers.
		handleSessionUpdate(profile, args[1:])
	case "set-transition-notify":
		handleSessionSetTransitionNotify(profile, args[1:])
	case "set-title-lock":
		handleSessionSetTitleLock(profile, args[1:])
	case "set":
		handleSessionSet(profile, args[1:])
	case "switch-account":
		handleSessionSwitchAccount(profile, args[1:])
	case "switch-settle":
		handleSessionSwitchSettle(profile, args[1:])
	case "switch":
		handleSessionSwitch(profile, args[1:])
	case "switch-preview":
		handleSessionSwitchPreview(profile, args[1:])
	case "move", "mv":
		handleSessionMove(profile, args[1:])
	case "image-upload":
		handleSessionImageUpload(profile, args[1:])
	case "send":
		handleSessionSend(profile, args[1:])
	case "ref":
		handleSessionRef(profile, args[1:])
	case "send-status":
		handleSessionSendStatus(profile, args[1:])
	case "queue":
		handleSessionQueue(profile, args[1:])
	case "send-worker":
		handleSessionSendWorker(profile, args[1:])
	case "approve":
		handleSessionApprove(profile, args[1:])
	case "command":
		handleSessionCommand(profile, args[1:])
	case "commands":
		handleSessionCommands(profile, args[1:])
	case "send-keys":
		handleSessionSendKeys(profile, args[1:])
	case "output":
		handleSessionOutput(profile, args[1:])
	case "context":
		handleSessionContext(profile, args[1:])
	case "children":
		handleSessionChildren(profile, args[1:])
	case "ownership":
		handleSessionOwnership(profile, args[1:])
	case "window":
		handleSessionWindow(profile, args[1:])
	case "search":
		handleSessionSearch(profile, args[1:])
	case "metrics":
		handleSessionMetrics(profile, args[1:])
	case "annotate":
		handleSessionAnnotate(profile, args[1:])
	case "help", "--help", "-h":
		printSessionHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown session command: %s\n", args[0])
		printSessionHelp()
		exitCLI(1)
	}
}

// printSessionHelp prints help for session commands
func printSessionHelp() {
	fmt.Println("Usage: agent-deck session <command> [options]")
	fmt.Println()
	fmt.Println("Manage individual sessions.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  start <id>              Start a session's tmux process")
	fmt.Println("  stop <id>               Stop/kill session process")
	fmt.Println("  remove <id>             Remove session from registry (stopped/error only; --force to bypass)")
	fmt.Println("  cleanup [--days N]      Purge dead sessions idle N+ days (dry-run unless --yes)")
	fmt.Println("  archive <id|title>      Stop session and hide it from active lists (retained in storage)")
	fmt.Println("  unarchive <id|title>    Restore an archived session (does not restart it)")
	fmt.Println("  restart [id] [--all] [--env KEY=VALUE]  Restart session (Claude: reload MCPs)")
	fmt.Println("  revive [--all|--name]   Rebuild dead control pipes for errored sessions")
	fmt.Println("  fork <id>               Fork Claude, OpenCode, Pi, Codex, or Oh My Pi session with context")
	fmt.Println("  handoff <id>            Build a cross-tool handoff prompt from the session's conversation (read-only)")
	fmt.Println("  switch-preview <id>     Preview switch capability, fidelity and refusals (read-only, no mutation)")
	fmt.Println("  attach <id>             Attach to session interactively")
	fmt.Println("  focus <id> [--attach]   Signal the running TUI to select (or --attach) a session (--remote <name> for a remote's session)")
	fmt.Println("  recent [--limit N]      List sessions most-recently-used first (same last_accessed the TUI's alternate/MRU keys use)")
	fmt.Println("  show [id]               Show session details (auto-detect current if no id)")
	fmt.Println("  viewers [id]            List the terminals attached to a session (who is viewing it)")
	fmt.Println("  current                 Show current session and profile (auto-detect)")
	fmt.Println("  primer [id]             Show the resolved context-level and rendered primer/identity text (#2260; auto-detect current if no id)")
	fmt.Println("  set <id> <field> <value>  Update session property")
	fmt.Println("  switch <id> --to-harness <harness> [--to-account <account>]  Switch account or create a confirmed fresh cross-harness target")
	fmt.Println("  switch-account <id> <account>  Switch Claude account and migrate the conversation")
	fmt.Println("  switch-settle <id>             Finish a switch hands-free: ready, prompts, plugins, kick, verify")
	fmt.Println("  move <id> <path>        Move session to a new path (migrates Claude history)")
	fmt.Println("  send <id> <message>     Send a message to a running session (--queue: never silently lost, see send-status; --image <path>)")
	fmt.Println("  ref <source> <target>   Type a short reference to <source> into <target>'s composer, not sent (--print: only print it)")
	fmt.Println("  image-upload <id>       Stage an attachment from stdin (--name <uuid>.png --json; max 20 MiB)")
	fmt.Println("  command [id] \"/cmd ...\"  Run a Claude slash command (/model, /compact, /clear, /exit, skills) and verify the outcome")
	fmt.Println("  commands [id]           List the slash commands and skills a Claude session can run (--json)")
	fmt.Println("  send-status <send-id>   State of a queued send: queued, typed, submitted, landed or failed")
	fmt.Println("  queue list <session>    List durable queued sends (--json)")
	fmt.Println("  queue release <id>      Send a queued entry now, with confirmed delivery evidence (--json)")
	fmt.Println("  queue cancel <id>       Cancel an unsent entry or report its delivery (--json)")
	fmt.Println("  approve <id> [choice]   Resolve a visible Codex approval prompt")
	fmt.Println("  output <id>             Get the last response from a session")
	fmt.Println("  context [id]            Show what is loaded into the agent's context, ranked by cost")
	fmt.Println("  children [id]           List sub-sessions with status + last completion")
	fmt.Println("  ownership <cmd> <id>    Inspect/reconcile the processes a session owns (#1873)")
	fmt.Println("  window close <id> <window> --yes  Kill one tmux window in a session (CLI parity for the TUI's window-row 'd')")
	fmt.Println("  search <query>          Search message content across Claude sessions")
	fmt.Println("  metrics <id>|--all      Per-session eval numbers from the local event journal (--json, --since)")
	fmt.Println("  annotate <id>|--self    Record durable hints/tags (purpose, ticket, why, decision, outcome) for recall")
	fmt.Println("  set-parent <id> <parent>  Link session as sub-session of parent")
	fmt.Println("  unset-parent <id>       Remove sub-session link")
	fmt.Println("  update <id> --no-parent          Alias for unset-parent <id>")
	fmt.Println("  update <id> --parent <pid>       Alias for set-parent <id> <pid>")
	fmt.Println("  set-transition-notify <id> <on|off>  Enable/disable transition notifications")
	fmt.Println("  set-title-lock <id> <on|off>         Lock/unlock title from Claude session-name sync (#697)")
	fmt.Println()
	fmt.Println("Global Options:")
	fmt.Println("  -p, --profile <name>   Use specific profile")
	fmt.Println("  --json                 Output as JSON")
	fmt.Println("  -q, --quiet            Minimal output (exit codes only)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck session start my-project")
	fmt.Println("  agent-deck session stop abc123")
	fmt.Println("  agent-deck session restart my-project")
	fmt.Println("  agent-deck session restart --all                # Restart all active sessions")
	fmt.Println("  agent-deck session fork my-project -t \"my-project-fork\"")
	fmt.Println("  agent-deck session attach my-project")
	fmt.Println("  agent-deck session show                  # Auto-detect current session")
	fmt.Println("  agent-deck session show my-project --json")
	fmt.Println("  agent-deck session set-parent sub-task main-project  # Make sub-task a sub-session")
	fmt.Println("  agent-deck session unset-parent sub-task             # Remove sub-session link")
	fmt.Println("  agent-deck session set-transition-notify worker off    # Suppress notifications")
	fmt.Println("  agent-deck session set-transition-notify worker on     # Re-enable notifications")
	fmt.Println("  agent-deck session set-title-lock SCRUM-351 on         # Prevent Claude from renaming it")
	fmt.Println("  agent-deck session set-title-lock SCRUM-351 off        # Re-enable title sync")
	fmt.Println("  agent-deck session output my-project                 # Get last response from session")
	fmt.Println("  agent-deck session output my-project --json          # Get response as JSON")
	fmt.Println("  agent-deck session ref auth-fix reviewer             # Hand reviewer a reference to auth-fix (not sent)")
	fmt.Println("  agent-deck session context my-project                # What is in the agent's context")
	fmt.Println("  agent-deck session context my-project --tab breakdown # Ranked by token cost")
	fmt.Println("  agent-deck session context my-project --json         # Full report with provenance")
	fmt.Println("  agent-deck session archive my-project                # Stop and hide the session")
	fmt.Println("  agent-deck session unarchive my-project              # Restore an archived session")
	fmt.Println("  agent-deck session recent                            # Most-recently-used sessions first")
	fmt.Println("  agent-deck session recent --json --limit 5           # Same, machine-readable, top 5")
	fmt.Println("  agent-deck session annotate auth-fix --ticket SB-412 --tag auth  # Durable hints for recall")
	fmt.Println("  agent-deck session annotate --self --outcome worked  # An agent annotating its own session")
	fmt.Println()
	fmt.Println("Set command fields:")
	fmt.Println("  title              Session title")
	fmt.Println("  path               Project path")
	fmt.Println("  command            Command to run")
	fmt.Println("  tool               Tool type (claude, gemini, shell, etc.)")
	fmt.Println("  wrapper            Wrapper command (use {command} to include tool command; an unquoted {command} after a prefix gets a compound command (&&, ;, |, export ...) as one bash -c argv, so for remote use --ssh or quote {command})")
	fmt.Println("  claude-session-id  Claude conversation ID (for fork/resume)")
	fmt.Println("  favorite           Favourite flag: true or false (favorite in list/show --json)")
	fmt.Println("  gemini-session-id  Gemini conversation ID (for resume)")
	fmt.Println("  tool-session-id    Custom [tools.*] conversation ID (resume_flag after reboot)")
	fmt.Println()
	fmt.Println("Set examples:")
	fmt.Println("  agent-deck session set my-project title \"New Title\"")
	fmt.Println("  agent-deck session set my-project claude-session-id \"abc123-def456\"")
	fmt.Println("  agent-deck session set my-project tool-session-id \"019f683f-...\"")
	fmt.Println("  agent-deck session set my-project tool claude")
	fmt.Println("  agent-deck session set my-project wrapper \"nvim +'terminal {command}'\"")
}

// spawnVerifyWait bounds how long `session start`/`restart` wait for a
// missing tmux session to either appear or be explained by a spawn-failure
// record (#2099). The fast-death watcher records a death on its 250ms tick,
// so this comfortably covers a pane that died before the first tick; a live
// session returns immediately and never pays it.
const spawnVerifyWait = 2 * time.Second

// sessionForkBeforeStartHook is nil in production. Tests assign it to inspect
// the fully-prepared fork before tmux Start() mutates the environment. When
// the hook is set, handleSessionFork invokes it and returns immediately —
// no tmux session, no persistence, no Start(). This lets contract tests
// assert option propagation without spawning real sessions.
var sessionForkBeforeStartHook func(parent *session.Instance, forked *session.Instance, state git.WorktreeStateOptions)

// handleSessionFork forks a supported tool session
func handleSessionFork(profile string, args []string) {
	fs := flag.NewFlagSet("session fork", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	title := fs.String("title", "", "Title for forked session")
	titleShort := fs.String("t", "", "Title for forked session (short)")
	group := fs.String("group", "", "Group for forked session")
	groupShort := fs.String("g", "", "Group for forked session (short)")
	worktreeBranch := fs.String("w", "", "Create fork in a worktree/workspace for branch (git or jj)")
	worktreeBranchLong := fs.String("worktree", "", "Create fork in a worktree/workspace for branch (git or jj)")
	newBranch := fs.Bool("b", false, "Create new branch/bookmark (use with --worktree)")
	newBranchLong := fs.Bool("new-branch", false, "Create new branch/bookmark")
	withState := fs.Bool("with-state", false, "Carry parent's uncommitted working state into the new worktree/workspace (git or jj; #1029/#1305, requires -w)")
	withStateGitignored := fs.Bool("with-state-and-gitignored", false, "Like --with-state, plus gitignored files (e.g. .env). Implies --with-state. Requires -w.")
	sandbox := fs.Bool("sandbox", false, "Run forked session in Docker sandbox")
	sandboxImage := fs.String("sandbox-image", "", "Docker image for sandbox (overrides config default)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session fork <id|title> [options]")
		fmt.Println()
		fmt.Println("Fork a Claude, OpenCode, Pi, Codex, or Oh My Pi session with conversation context.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session fork my-project")
		fmt.Println("  agent-deck session fork my-project -t \"my-fork\"")
		fmt.Println("  agent-deck session fork my-project -t \"my-fork\" -g \"experiments\"")
		fmt.Println("  agent-deck session fork my-project -w fork/experiment")
		fmt.Println("  agent-deck session fork my-project -w fork/new-idea -b")
		fmt.Println("  agent-deck session fork my-project -w fork/wip -b --with-state")
		fmt.Println("  agent-deck session fork my-project -w fork/wip -b --with-state-and-gitignored")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Merge short and long flags
	forkTitle := mergeFlags(*title, *titleShort)
	forkGroup := mergeFlags(*group, *groupShort)

	// Load sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// -g means what it means for add and launch: a unique group name resolves
	// to its stored path and an ambiguous one is an error, before any worktree
	// or session is created.
	if forkGroup != "" {
		groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
		if cfg, cfgErr := session.LoadUserConfig(); cfgErr == nil && cfg != nil {
			session.ReconcileDeclarativeGroups(groupTree, cfg)
		}
		resolvedGroup, groupErr := resolveGroupPathForAdd(groupTree, forkGroup)
		if groupErr != nil {
			out.Error(groupErr.Error(), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		forkGroup = resolvedGroup
	}

	// Verify this tool has a session-fork implementation.
	isClaudeFork := session.IsClaudeCompatible(inst.Tool)
	if !session.SupportsNativeFork(inst.Tool) {
		out.Error(
			fmt.Sprintf("session '%s' is not a forkable session (tool: %s)", inst.Title, inst.Tool),
			ErrCodeInvalidOperation,
		)
		exitCLI(1)
	}

	// Try to capture Claude session ID from tmux if missing (handles pre-fix sessions).
	if isClaudeFork && inst.ClaudeSessionID == "" && inst.Exists() {
		inst.PostStartSync(2 * time.Second)
	}

	// Verify it can be forked.
	if !inst.CanFork() {
		out.Error(
			fmt.Sprintf("session '%s' cannot be forked: no resumable session for tool %s", inst.Title, inst.Tool),
			ErrCodeInvalidOperation,
		)
		exitCLI(1)
	}

	// Default title if not provided. An explicitly passed -t/--title is user
	// intent and gets TitleLocked below (mirrors the TUI fork dialog); the
	// auto-generated "<title>-fork" default keeps the #572 name sync enabled
	// (mirrors quick fork).
	explicitTitle := forkTitle != ""
	if !explicitTitle {
		forkTitle = inst.Title + "-fork"
	}

	// Default group to parent's group
	if forkGroup == "" {
		forkGroup = inst.GroupPath
	}

	// Resolve worktree flags
	wtBranch := *worktreeBranch
	if *worktreeBranchLong != "" {
		wtBranch = *worktreeBranchLong
	}
	createNewBranch := *newBranch || *newBranchLong

	// #1029: --with-state-and-gitignored implies --with-state.
	wantState := *withState || *withStateGitignored
	if wantState && wtBranch == "" {
		out.Error("--with-state requires an explicit worktree branch (-w/--worktree)", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Handle worktree creation
	var opts *session.ClaudeOptions
	var worktreeType string
	if wtBranch != "" {
		backend, err := detectAndCreateBackend(inst.ProjectPath)
		if err != nil {
			out.Error(fmt.Sprintf("%v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		worktreeType = string(backend.Type())
		repoRoot := backend.RepoDir()

		// --with-state* anchors the new worktree/workspace at the parent's
		// committed point and materializes the parent's working state. git and
		// jujutsu both support it (jj since #1305); any other backend can't, so
		// reject early. The git-direct collision gate and anchoring below are
		// reached only on the git branch; jujutsu has its own branch.
		if wantState && backend.Type() != vcs.TypeGit && backend.Type() != vcs.TypeJujutsu {
			out.Error("--with-state is not supported for this repository's VCS backend", ErrCodeInvalidOperation)
			exitCLI(1)
		}

		// Apply configured branch prefix before validation/existence checks.
		// Resolved for repoRoot so directory-local .agent-deck/config.toml
		// overrides (#2093) apply before the worktree path is calculated.
		wtSettings, err := session.GetWorktreeSettingsForDir(repoRoot)
		if err != nil {
			out.Error(fmt.Sprintf("invalid directory-local config: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		wtBranch = wtSettings.ApplyBranchPrefix(wtBranch)

		// Destination gate (BUG-01/08). With-state forks create a NEW branch
		// anchored at the parent's HEAD, so they must refuse any pre-existing
		// branch or worktree — one well-defined collision gate, evaluated before
		// path computation and the legacy reuse check. Non-with-state forks keep
		// upstream's "branch must already exist (use -b to create)" contract.
		// These two are mutually exclusive: with-state requires the branch ABSENT,
		// the else-branch requires it PRESENT — never flatten them.
		if wantState && backend.Type() == vcs.TypeGit {
			if err := git.ValidateForkWithStateDestination(repoRoot, wtBranch); err != nil {
				var collErr *git.DestinationCollisionError
				if errors.As(err, &collErr) {
					switch collErr.Kind {
					case git.CollisionWorktreeExists:
						out.Error(fmt.Sprintf("branch '%s' already has a worktree at %s; choose a new destination branch for --with-state", collErr.Branch, collErr.Path), ErrCodeInvalidOperation)
					case git.CollisionBranchExists:
						out.Error(fmt.Sprintf("branch '%s' already exists; choose a new destination branch for --with-state", collErr.Branch), ErrCodeInvalidOperation)
					default:
						out.Error(collErr.Error(), ErrCodeInvalidOperation)
					}
					exitCLI(1)
				}
				out.Error(fmt.Sprintf("failed to validate destination: %v", err), ErrCodeInvalidOperation)
				exitCLI(1)
			}
		} else if wantState {
			// jujutsu with-state: a fresh destination bookmark is required, mirroring
			// the git collision gate. (Workspace-path collision is caught by the
			// os.Stat check below.)
			exists, bmErr := jujutsu.BookmarkExists(repoRoot, wtBranch)
			if bmErr != nil {
				out.Error(fmt.Sprintf("failed to validate destination: %v", bmErr), ErrCodeInvalidOperation)
				exitCLI(1)
			}
			if exists {
				out.Error(fmt.Sprintf("bookmark '%s' already exists; choose a new destination branch for --with-state", wtBranch), ErrCodeInvalidOperation)
				exitCLI(1)
			}
		} else if !createNewBranch && !backend.BranchExists(wtBranch) {
			out.Error(fmt.Sprintf("branch '%s' does not exist (use -b to create)", wtBranch), ErrCodeInvalidOperation)
			exitCLI(1)
		}

		worktreePath := backend.WorktreePath(vcs.WorktreePathOptions{
			Branch:    wtBranch,
			Location:  wtSettings.DefaultLocation,
			SessionID: git.GeneratePathID(),
			Template:  wtSettings.Template(),
		})

		// Check for an existing worktree for this branch before creating a new
		// one. Routed through the backend so jujutsu reuse keeps working. The
		// with-state path is validated above and must NEVER reuse a worktree, so
		// the reuse assignment is gated on !wantState (BUG-01/08).
		reuseExistingWorktree := false
		if !wantState {
			if existingPath, err := backend.GetWorktreeForBranch(wtBranch); err == nil && existingPath != "" {
				fmt.Fprintf(os.Stderr, "Reusing existing worktree at %s for branch %s\n", existingPath, wtBranch)
				worktreePath = existingPath
				reuseExistingWorktree = true
			}
		}
		if !reuseExistingWorktree {
			if _, statErr := os.Stat(worktreePath); statErr == nil {
				out.Error(fmt.Sprintf("worktree path already exists: %s", worktreePath), ErrCodeInvalidOperation)
				exitCLI(1)
			}

			if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
				out.Error(fmt.Sprintf("failed to create directory: %v", err), ErrCodeInvalidOperation)
				exitCLI(1)
			}

			var setupErr error
			if wantState && backend.Type() == vcs.TypeGit {
				//
				// Mid-op refusal: surface an actionable error BEFORE creating the
				// worktree, so the user sees the exact abort command for their
				// parent instead of MaterializeWipFromParent's terse backstop
				// wording (which fires AFTER worktree creation and triggers
				// cleanup-on-error). The backstop in materialize_wip.go's
				// refuseUnsafeParentState still covers detectErr != nil cases — we
				// fall through silently there.
				if kind, detectErr := git.DetectInProgressOperation(inst.ProjectPath); detectErr == nil && kind != "" {
					abortCmd := map[string]string{
						"rebase":      "git rebase --abort",
						"merge":       "git merge --abort",
						"cherry-pick": "git cherry-pick --abort",
						"revert":      "git revert --abort",
						"bisect":      "git bisect reset",
					}[kind]
					out.Error(fmt.Sprintf("parent session is mid-%s; finish or abort the %s before forking with state (cd %s && %s)",
						kind, kind, inst.ProjectPath, abortCmd), ErrCodeInvalidOperation)
					exitCLI(1)
				}

				if git.HasSubmodules(inst.ProjectPath) {
					fmt.Fprintln(os.Stderr, "Warning: submodules detected — copied as files, not recursed (parent's submodule states preserved)")
				}

				// Capture parent's HEAD so linked-worktree parents anchor correctly.
				parentHead, hcErr := git.HeadCommit(inst.ProjectPath)
				if hcErr != nil {
					out.Error(fmt.Sprintf("failed to resolve parent session HEAD: %v", hcErr), ErrCodeInvalidOperation)
					exitCLI(1)
				}

				// #1708: inherit the PARENT SESSION's sparse state (its own
				// worktree), not repoRoot's — see git.CaptureSparseCheckout.
				createdBranch, cwErr := git.CreateWorktreeAtStartPointWithOptions(repoRoot, worktreePath, wtBranch, parentHead,
					wtSettings.CreateOptions(inst.ProjectPath))
				if cwErr != nil {
					out.Error(fmt.Sprintf("worktree creation failed: %v", cwErr), ErrCodeInvalidOperation)
					exitCLI(1)
				}

				// Materialize parent state, with cleanup-on-error.
				if matErr := git.MaterializeWipFromParent(inst.ProjectPath, worktreePath, *withStateGitignored); matErr != nil {
					var cleanupErrs []string
					if rmErr := git.RemoveWorktree(repoRoot, worktreePath, true); rmErr != nil {
						cleanupErrs = append(cleanupErrs, fmt.Sprintf("worktree remove failed: %v", rmErr))
					}
					if createdBranch {
						if brErr := exec.Command("git", "-C", repoRoot, "branch", "-D", wtBranch).Run(); brErr != nil {
							cleanupErrs = append(cleanupErrs, fmt.Sprintf("branch delete failed: %v", brErr))
						}
					}
					if len(cleanupErrs) == 0 {
						out.Error(fmt.Sprintf("failed to materialize parent state: %v; new worktree cleaned up", matErr), ErrCodeInvalidOperation)
					} else {
						out.Error(fmt.Sprintf("failed to materialize parent state: %v; cleanup also failed (%s); manual cleanup required: rm -rf %s%s",
							matErr,
							strings.Join(cleanupErrs, "; "),
							shellescape.Quote(worktreePath),
							branchCleanupHint(createdBranch, repoRoot, wtBranch),
						), ErrCodeInvalidOperation)
					}
					exitCLI(1)
				}

				// Continue upstream's wrapper tail: worktreeinclude + setup hook.
				if inclErr := git.ProcessWorktreeInclude(repoRoot, worktreePath, os.Stderr); inclErr != nil {
					fmt.Fprintf(os.Stderr, "worktreeinclude: %v\n", inclErr)
				}
				setupErr = git.RunWorktreeSetupAfterCreate(repoRoot, worktreePath, os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
			} else if wantState {
				// jujutsu with-state (#1305): anchor the new workspace at the
				// parent's committed point (@-) and materialize its working copy.
				parentBase, pbErr := jujutsu.WorkingCopyParentRevision(inst.ProjectPath)
				if pbErr != nil {
					out.Error(fmt.Sprintf("failed to resolve parent session committed anchor: %v", pbErr), ErrCodeInvalidOperation)
					exitCLI(1)
				}
				if cwErr := jujutsu.CreateWorkspaceAtRevision(repoRoot, worktreePath, wtBranch, parentBase); cwErr != nil {
					out.Error(fmt.Sprintf("workspace creation failed: %v", cwErr), ErrCodeInvalidOperation)
					exitCLI(1)
				}
				if matErr := jujutsu.MaterializeWipFromParent(inst.ProjectPath, worktreePath, *withStateGitignored); matErr != nil {
					var cleanupErrs []string
					if rmErr := backend.RemoveWorktree(worktreePath, true); rmErr != nil {
						cleanupErrs = append(cleanupErrs, fmt.Sprintf("workspace forget failed: %v", rmErr))
					}
					if brErr := backend.DeleteBranch(wtBranch, true); brErr != nil {
						cleanupErrs = append(cleanupErrs, fmt.Sprintf("bookmark delete failed: %v", brErr))
					}
					if len(cleanupErrs) == 0 {
						out.Error(fmt.Sprintf("failed to materialize parent state: %v; new workspace cleaned up", matErr), ErrCodeInvalidOperation)
					} else {
						out.Error(fmt.Sprintf("failed to materialize parent state: %v; cleanup also failed (%s); manual cleanup required: rm -rf %s",
							matErr, strings.Join(cleanupErrs, "; "), shellescape.Quote(worktreePath)), ErrCodeInvalidOperation)
					}
					exitCLI(1)
				}
				if *withStateGitignored && !jujutsu.SupportsGitignoredCopy(inst.ProjectPath) {
					fmt.Fprintln(os.Stderr, "Warning: forked without gitignored files: this jj repo has no git metadata to copy them")
				}
			} else if backend.Type() == vcs.TypeGit {
				// Non-with-state git path: upstream's combined wrapper unchanged.
				var cwErr error
				setupErr, cwErr = git.CreateWorktreeWithSetupOptions(
					repoRoot, worktreePath, wtBranch,
					git.WorktreeStateOptions{},
					wtSettings.CreateOptions(inst.ProjectPath),
					os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
				if cwErr != nil {
					out.Error(fmt.Sprintf("worktree creation failed: %v", cwErr), ErrCodeInvalidOperation)
					exitCLI(1)
				}
			} else {
				// Non-git backend (jujutsu): with-state already rejected above.
				if err := backend.CreateWorktree(worktreePath, wtBranch); err != nil {
					out.Error(fmt.Sprintf("worktree creation failed: %v", err), ErrCodeInvalidOperation)
					exitCLI(1)
				}
			}
			// A skipped (unapproved) hook already printed its notice above.
			if setupErr != nil && !errors.Is(setupErr, git.ErrWorktreeScriptNotApproved) {
				fmt.Fprintf(os.Stderr, "Warning: worktree setup script failed: %v\n", setupErr)
			}
		}

		userConfig, _ := session.LoadUserConfig()
		opts = session.NewClaudeOptions(userConfig)
		opts.WorkDir = worktreePath
		opts.WorktreePath = worktreePath
		opts.WorktreeRepoRoot = repoRoot
		opts.WorktreeBranch = wtBranch
	}

	// Create the forked instance
	var forkedInst *session.Instance
	forkedInst, _, err = inst.CreateForkedInstanceForTool(forkTitle, forkGroup, opts)
	if err != nil {
		out.Error(fmt.Sprintf("failed to create fork: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	if explicitTitle {
		forkedInst.TitleLocked = true
	}

	if worktreeType != "" {
		forkedInst.WorktreeType = worktreeType
	}

	// Apply sandbox config if requested.
	if *sandbox {
		forkedInst.Sandbox = session.NewSandboxConfig(*sandboxImage)
	}

	// Test seam: when set, capture the fully-prepared fork before tmux Start()
	// mutates the environment and return early. Production runs leave the hook
	// nil, so this is a no-op outside of tests.
	if sessionForkBeforeStartHook != nil {
		sessionForkBeforeStartHook(inst, forkedInst, git.WorktreeStateOptions{WithState: wantState, WithIgnored: *withStateGitignored})
		return
	}

	// Start the forked session
	if err := forkedInst.Start(); err != nil {
		out.Error(fmt.Sprintf("failed to start forked session: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	forkedInst.RecordTelemetryCreate(telemetry.ViaCLIFork)

	// Capture forked session's new session ID
	forkedInst.PostStartSync(3 * time.Second)

	// Add to instances
	instances = append(instances, forkedInst)

	// Rebuild group tree and ensure group exists
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	forkCfg, _ := session.LoadUserConfig()
	groupTree.DefaultMaxConcurrent = forkCfg.GroupDefaults.MaxConcurrent
	if forkedInst.GroupPath != "" {
		groupTree.CreateGroupPath(forkedInst.GroupPath)
	}

	// Save
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Output success
	out.Success(
		fmt.Sprintf("Forked session: %s -> %s (%s)", inst.Title, forkedInst.Title, TruncateID(forkedInst.ID)),
		map[string]interface{}{
			"success":   true,
			"parent_id": inst.ID,
			"new_id":    forkedInst.ID,
			"new_title": forkedInst.Title,
		},
	)
}

// errFocusNotFound signals that `session focus` was given an id absent from the
// current profile. Callers map it to a distinct (exit 2) "not found" code.
var errFocusNotFound = errors.New("session not found")

// errFocusRemoteUnknown signals that `session focus --remote <name>` named a
// remote absent from the config. It also exits 2, like an unknown id.
var errFocusRemoteUnknown = errors.New("remote is not configured")

// loadSessionData loads storage and session data for a profile
// The Storage.LoadWithGroups() method already handles tmux reconnection internally
func loadSessionData(profile string) (*session.Storage, []*session.Instance, []*session.GroupData, error) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	instances, groupsData, err := storage.LoadWithGroups()
	if err != nil {
		_ = storage.Close()
		return nil, nil, nil, fmt.Errorf("failed to load sessions: %w", err)
	}

	// LoadWithGroups reconnects tmux sessions with lazy loading.
	// Status uses cached values from JSON; session IDs are not synced at load time.

	return storage, instances, groupsData, nil
}

// saveSessionData saves session data with groups, preserving stored group metadata (sort_order).
func saveSessionData(storage *session.Storage, instances []*session.Instance, groups []*session.GroupData) error {
	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	return storage.SaveWithGroups(instances, groupTree)
}

// Delivery status values surfaced by the `session send` path (issue #1413).
// They are part of the `--json` contract: callers (watchers, conductors,
// bridges) key off the `delivery` field to distinguish a confirmed submit
// from a message left typed-but-unsubmitted at the composer.
const (
	// deliverySubmitted: positive evidence the agent accepted the message
	// (an "active" transition, or the composer cleared after holding it).
	deliverySubmitted = "submitted"
	// deliveryUnverified: the message was sent but neither Claude-shaped
	// submission signals nor a content-arrival check could reach a verdict,
	// so submission is genuinely unknown. Since issue #1793 this is the
	// narrow "we could not tell" bucket, not the catch-all it used to be:
	// a send only lands here when the payload is small enough that the
	// canonical-overflow failure mode cannot apply and it carries no token
	// distinctive enough to look for in the pane.
	deliveryUnverified = "unverified"
	// deliveryDelivered: the message body reached the target pane and Enter
	// was sent, and nothing positive was observed either way afterwards —
	// the tool exposes no submission signal (a shell, an unknown tool) or
	// its signal did not arrive within the window. Exit 0, `"submitted":
	// false`, `"confirmation": "unknown"`, and the human line says so.
	// This replaces the `typed` failure (issue #1793's false-negative half,
	// #1978, #2071): the exit code follows the evidence, never its absence,
	// and "NOT delivered" is only ever said on positive evidence — a pane
	// that is gone, a composer still holding the body, an open menu.
	deliveryDelivered = "delivered"
	// deliveryLineTooLong: refused before typing anything because the pane's
	// reader is in canonical mode and a payload line exceeds its line buffer
	// (issue #1793). The kernel would discard the overflow and the
	// submitting Enter with it, so this can never be reported as success.
	deliveryLineTooLong = "line_too_long"
	// deliveryMenuOpen: an AskUserQuestion picker or permission dialog was
	// open at the end of the budget and the message was never taken; a menu
	// consumes keystrokes as option selections (internal/tmux substate).
	// Positive evidence of a swallowed send: nonzero exit.
	deliveryMenuOpen = "menu_open"
	// deliveryPaneGone: the target pane disappeared during verification.
	// Positive evidence: nonzero exit.
	deliveryPaneGone = "pane_gone"
	// deliveryTypedNotSubmitted: the message body is still sitting unsent in
	// the composer after the bounded Enter-retry budget (issue #1413).
	deliveryTypedNotSubmitted = "typed_not_submitted"
	// deliveryNoEvidence: no positive delivery signal was ever observed
	// (issue #876 silent-drop classification).
	deliveryNoEvidence = "no_evidence"
	// deliverySendFailed: the initial tmux send-keys itself failed.
	deliverySendFailed = "send_failed"
	// deliveryComposerBlocked: no input sent because composer safety was not established.
	deliveryComposerBlocked = "composer_blocked"
	// deliveryTargetBusy: no input sent because another send still held the
	// per-target lock after the bounded wait (messaging audit P2-2, #2104).
	// Nothing was typed, so a retry is safe.
	deliveryTargetBusy = "target_busy"
	// deliveryAcceptanceRefused: no input sent because exact Codex turn
	// acceptance could not be established (an unresolved earlier
	// submission, another session owning the identity, the acceptance lock
	// held by another send). Nothing was typed, so a retry is safe.
	deliveryAcceptanceRefused = "acceptance_refused"
	// deliveryQueued: the message was typed and Entered once, and the
	// target's hook-driven status reports it mid-turn (issue #2033). Claude
	// holds such input as a queued message and takes it up when the turn
	// ends, so this is a successful single delivery — but NOT a confirmed
	// submit. Exit 0, `"submitted": false`.
	deliveryQueued = "queued"
	// deliveryQueuedSocket: the message was written to the target's Claude Code
	// messaging socket after the endpoint's identity was verified (issue
	// #2089). Claude's inbox sends no in-band ack or refusal (verified
	// against 2.1.259) and it can drop or hold what it received, so this
	// means only "the bytes were written" — reported as `submitted: false,
	// acknowledged: false` (maintainer review of #2100). It is not the
	// positive-acceptance claim deliverySubmitted makes for tmux.
	deliveryQueuedSocket = "queued_socket"
	// deliverySocketWriteFailed: the write started and failed. The message may
	// or may not have been queued, so it is NEVER retried on tmux.
	deliverySocketWriteFailed = "socket_write_failed"
)

// The `wait_outcome` values `session send --wait` reports on the socket
// transport. There are three, and NONE of them claims the observed turn
// belongs to this message, because nothing on this transport can establish
// that: Claude's inbox returns no receipt, so waitForTurnStart,
// waitForCompletion and waitForFreshOutput all key off target status and
// wall-clock timestamps that are not tied to any message id (CodeRabbit on
// e94b296c). Every socket --wait therefore also carries `verified: false`.
// Part of the --json contract; exit code stays 0 for all three, and none
// ever retries on tmux — the socket write is already committed. The tmux
// transport reports neither key: its submit verification is a real
// pane-observed signal for the message it just typed.
const (
	// waitOutcomeUnverifiedBusyTarget: the target was confirmed mid-turn
	// when the message was written to its socket inbox, so the next
	// completion belongs to the turn already in flight. --wait declines to
	// wait and prints no output.
	waitOutcomeUnverifiedBusyTarget = "unverified_busy_target"
	// waitOutcomeUnverifiedBusyProbeFailed: the pre-write probe could read
	// no status, so the target could not be confirmed idle. Distinct from
	// the above because nothing established that it was generating (round-2
	// review of #2100). --wait declines to wait and prints no output.
	waitOutcomeUnverifiedBusyProbeFailed = "unverified_busy_probe_failed"
	// waitOutcomeObservedNotCorrelated: the probe read idle, so --wait ran
	// the normal turn-start-then-completion pipeline and printed real
	// output. The output is a turn that was observed after the write, not a
	// turn proven to be this message's: a turn starting in the window
	// between the idle probe and the write would be reported here just the
	// same. Honest naming for the case that DOES return output, rather than
	// letting its silence imply a correlation that was never established.
	waitOutcomeObservedNotCorrelated = "observed_not_correlated"
)

const (
	sessionSendDefaultTimeout  = 10 * time.Minute
	codexAcceptanceLockTimeout = 5 * time.Second
)

var (
	codexAcceptedTurnPollTimeout  = 2 * time.Second
	codexAcceptedTurnPollInterval = 50 * time.Millisecond
)

// The two acceptance errors that mean the Codex identity is provably
// unavailable, rather than contested: no identity at all, or a stored id
// with no current rollout generation. Only these may use
// --codex-composer-fallback.
var (
	errCodexIdentityUnavailable   = errors.New("Codex session identity is unavailable")
	errCodexGenerationUnavailable = errors.New("current rollout generation is unavailable")
)

// messageDeliveryToken returns a short, content-bearing slice of the message
// suitable for "did this body appear in the pane?" verification. Returns "" if
// the message contains no usefully-distinctive token (e.g. all whitespace, or
// only short common words).
// arrivalVerifyChecks bounds the post-send content-arrival poll. The body
// echoes into the pane as fast as the agent redraws, so this only has to
// cover a redraw, not a reply: at the callers' 200ms checkDelay that is ~2s.
const arrivalVerifyChecks = 10

// arrivalSafeLineBytes is the longest LINE that no line discipline can lose,
// and therefore the threshold above which an unconfirmed send is a failure
// rather than an "unverified" shrug. Same figure the tmux transport treats as
// always-safe (internal/tmux canonicalSafeBytes): at or below it the
// canonical-overflow loss mode of issue #1793 cannot occur, so a missed pane
// match is far likelier to be a rendering quirk than a lost message and the
// historical best-effort contract is kept. Above it a missed match is the
// actual bug signature and must not be reported as success.
//
// Measured per LINE, deliberately. Gating on total payload size would fail a
// 20 KB body of short lines that the transport in this same change delivers
// without trouble.
const arrivalSafeLineBytes = 1023

// claudeQueuePlaceholder is the composer placeholder Claude Code renders
// while it holds input behind the running turn.
const claudeQueuePlaceholder = send.ClaudeQueuedHint

// waitForCompletionTiming is waitForCompletion's initial grace period and
// poll interval. Unit tests that drive a scripted statusChecker shrink both
// so the poll sequence runs in milliseconds; production uses the defaults.
var waitForCompletionTiming = struct{ grace, poll time.Duration }{grace: time.Second, poll: 2 * time.Second}

// freshOutputTestConfig, when non-nil, overrides the default timing constants.
// Only set from tests.
var freshOutputTestConfig *freshOutputConfig

// getCurrentTmuxSessionName gets the current tmux session name (single subprocess call)
func getCurrentTmuxSessionName() (string, error) {
	// Bounded — see tmuxProbeTimeout.
	output, err := tmuxProbeBounded("display-message", "-p", "#{session_name}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// findInstanceDataByTmuxFast finds a session by tmux name using LoadLite (no tmux initialization)
// First tries the specified profile, then searches all profiles if not found.
// Returns the InstanceData and the profile it was found in.
func findInstanceDataByTmuxFast(tmuxSessionName, preferredProfile string) (*session.InstanceData, string) {
	// Try preferred profile first
	storage, err := session.NewStorageWithProfile(preferredProfile)
	if err == nil {
		instances, _, err := storage.LoadLite()
		if err == nil {
			if inst := matchInstanceDataByTmuxName(instances, tmuxSessionName); inst != nil {
				return inst, preferredProfile
			}
		}
	}

	// Search all profiles
	profiles, err := session.ListProfiles()
	if err != nil {
		return nil, ""
	}

	for _, p := range profiles {
		if p == preferredProfile {
			continue // Already checked
		}
		storage, err := session.NewStorageWithProfile(p)
		if err != nil {
			continue
		}
		instances, _, err := storage.LoadLite()
		if err != nil {
			continue
		}
		if inst := matchInstanceDataByTmuxName(instances, tmuxSessionName); inst != nil {
			return inst, p
		}
	}

	return nil, ""
}

// matchInstanceDataByTmuxName finds an InstanceData by exact tmux session name match
func matchInstanceDataByTmuxName(instances []*session.InstanceData, tmuxSessionName string) *session.InstanceData {
	for _, inst := range instances {
		if inst.TmuxSession == tmuxSessionName {
			return inst
		}
	}
	return nil
}

// childrenOf returns the direct sub-sessions of parentID, preserving the input
// order. Pure helper so the filtering is unit-testable without a live registry.
func childrenOf(parentID string, instances []*session.Instance) []*session.Instance {
	var out []*session.Instance
	for _, inst := range instances {
		if inst != nil && inst.ParentSessionID == parentID {
			out = append(out, inst)
		}
	}
	return out
}

// handleSessionChildren implements `session children [id]` — a read-only fleet
// view that lists a session's sub-sessions with live status and each child's
// last asserted completion (from the non-destructive completion ledger). It
// defaults to the current session and never clears the inbox, so a parent can
// poll it from any chat without disturbing delivery.
func handleSessionChildren(profile string, args []string) {
	fs := flag.NewFlagSet("session children", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	follow := fs.Bool("follow", false, "Stream child state changes as JSONL (one event per line) until interrupted")
	interval := fs.Duration("interval", 2*time.Second, "Poll interval for --follow")
	heartbeat := fs.Duration("heartbeat", 60*time.Second, "Heartbeat event interval for --follow (0 disables)")
	untilDone := fs.Bool("until-done", false, "With --follow: exit 0 once live state shows every child needs input or is terminal (waiting, idle with completion history, error, or stopped)")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session children [id|title] [options]")
		fmt.Println()
		fmt.Println("List a session's sub-sessions with live status and last completion history.")
		fmt.Println("Completion fields describe the last assertion; live status determines the current turn.")
		fmt.Println("Defaults to the current session. Read-only; does not clear the inbox.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("--follow emits JSONL events: snapshot (initial state per child), added,")
		fmt.Println("status (from/to transition), done (completion sentinel), removed, error,")
		fmt.Println("plus periodic heartbeat and a final complete line with --until-done.")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session children --json")
		fmt.Println("  agent-deck session children --follow                    # live fleet event stream")
		fmt.Println("  agent-deck session children --follow --until-done      # exits when every child needs input or finishes")
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}
	// Default to the caller's own session. resolveSelfSessionID prefers
	// AGENTDECK_INSTANCE_ID (the authoritative full id) over the tmux session
	// name, whose suffix is only a short hash and won't resolve.
	if strings.TrimSpace(identifier) == "" {
		self, err := resolveSelfSessionID()
		if err != nil {
			out.Error(err.Error(), ErrCodeNotFound)
			exitCLI(2)
		}
		identifier = self
	}
	parent, errMsg, errCode := ResolveSession(identifier, instances)
	if parent == nil {
		out.Error(errMsg, errCode)
		exitCLI(2)
	}

	if *untilDone && !*follow {
		out.Error("--until-done requires --follow", ErrCodeInvalidOperation)
		exitCLI(1)
	}
	// A non-positive interval makes time.Sleep a no-op, turning the poll into a
	// busy-loop that reopens storage every pass. Reject rather than clamp: a
	// silently different interval than asked for is its own surprise.
	if *follow && *interval <= 0 {
		out.Error("--interval must be positive", ErrCodeInvalidOperation)
		exitCLI(1)
	}
	if *follow {
		// The stream is JSONL by contract; --json/-q are irrelevant here.
		exitCLI(runChildrenFollow(profile, parent.ID, *interval, *heartbeat, *untilDone, os.Stdout))
	}

	kids := childrenOf(parent.ID, instances)
	session.RefreshInstancesForCLIStatus(kids)

	rows := buildChildRows(kids, sampleChildPanes)
	var human strings.Builder
	fmt.Fprintf(&human, "Children of %s (%s):\n", parent.Title, parent.ID)
	for _, row := range rows {
		done := row.DoneStatus
		if done == "" {
			done = "-"
		}
		fmt.Fprintf(&human, "  %s  %-20s  %-8s  done=%s  %s\n", row.ID, row.Title, row.Status, done, row.DoneSummary)
	}
	if len(kids) == 0 {
		human.WriteString("  (no sub-sessions)\n")
	}
	out.Print(human.String(), map[string]interface{}{"parent": parent.ID, "children": rows})
}

// handleSessionSearch implements issue #483 — search across Claude session
// message content (not just titles). Wraps the internal global-search index
// behind a CLI surface so users can find past prompts / responses without
// dropping into the TUI.
func handleSessionSearch(profile string, args []string) {
	_ = profile // reserved: future per-profile claudeDir lookup
	fs := flag.NewFlagSet("session search", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	limit := fs.Int("limit", 20, "Maximum number of results to return")
	recentDays := fs.Int("days", 30, "Only search sessions modified within the last N days (0 = all)")
	tierFlag := fs.String("tier", "auto", "Index tier: instant, balanced, auto")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session search <query> [options]")
		fmt.Println()
		fmt.Println("Search message content across all Claude sessions.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  <query>   Free-text query (case-insensitive substring match)")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session search \"MCP server\"")
		fmt.Println("  agent-deck session search authentication --json")
		fmt.Println("  agent-deck session search \"database migration\" --limit 5")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	out := NewCLIOutput(*jsonOutput, *quiet || *quietShort)

	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		out.Error("query is required", ErrCodeNotFound)
		fs.Usage()
		exitCLI(1)
	}

	claudeDir := session.GetClaudeConfigDir()
	searchEnabled := true
	cfg := session.GlobalSearchSettings{
		Enabled:        &searchEnabled,
		Tier:           *tierFlag,
		MemoryLimitMB:  100,
		RecentDays:     *recentDays,
		IndexRateLimit: 200,
	}
	index, err := session.NewGlobalSearchIndex(claudeDir, cfg)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize search index: %v", err), ErrCodeNotFound)
		exitCLI(1)
	}
	if index == nil {
		out.Error("search index is disabled", ErrCodeNotFound)
		exitCLI(1)
	}
	defer index.Close()

	// Wait for the background initialLoad to finish. The fs.Size-based tier
	// detector returns immediately; content population is async. Poll up to
	// ~3s — enough for most ~.claude/projects but bounded for CLI latency.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !index.IsLoading() {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	results := index.Search(query)
	if *limit > 0 && len(results) > *limit {
		results = results[:*limit]
	}

	type hitJSON struct {
		SessionID string `json:"session_id"`
		Snippet   string `json:"snippet"`
		CWD       string `json:"cwd"`
		Summary   string `json:"summary,omitempty"`
		FilePath  string `json:"file_path,omitempty"`
	}

	hits := make([]hitJSON, 0, len(results))
	for _, r := range results {
		if r == nil || r.Entry == nil {
			continue
		}
		hits = append(hits, hitJSON{
			SessionID: r.Entry.SessionID,
			Snippet:   r.Snippet,
			CWD:       r.Entry.CWD,
			Summary:   r.Entry.Summary,
			FilePath:  r.Entry.FilePath,
		})
	}

	if *jsonOutput {
		out.Success("", map[string]interface{}{
			"query":   query,
			"results": hits,
			"count":   len(hits),
		})
		return
	}

	if len(hits) == 0 {
		fmt.Printf("No sessions matched %q\n", query)
		return
	}
	fmt.Printf("Found %d match(es) for %q:\n", len(hits), query)
	for i, h := range hits {
		fmt.Printf("%d. %s\n", i+1, h.SessionID)
		if h.CWD != "" {
			fmt.Printf("   cwd: %s\n", h.CWD)
		}
		if h.Snippet != "" {
			fmt.Printf("   %s\n", h.Snippet)
		}
	}
}
