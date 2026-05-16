package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionMove implements `agent-deck session move <id> <new-path> [options]`
// (issue #414). It wraps up what used to be a 4-step manual ritual (session
// set path + group move + cp ~/.claude/projects/<old>/ + session restart)
// into a single atomic command.
func handleSessionMove(profile string, args []string) {
	fs := flag.NewFlagSet("session move", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	group := fs.String("group", "", "Also move to this group path (optional)")
	noRestart := fs.Bool("no-restart", false, "Skip automatic restart after migration")
	copyHistory := fs.Bool("copy", false, "Copy Claude session history instead of moving (preserves old path data)")
	toProfile := fs.String("to-profile", "", "Migrate the session to another profile's DB (issue #928); incompatible with <new-path>")
	force := fs.Bool("force", false, "With --to-profile: migrate running sessions (tmux process keeps running)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session move <id|title> <new-path> [options]")
		fmt.Println("       agent-deck session move <id|title> --to-profile <name> [--force]")
		fmt.Println()
		fmt.Println("Move a session to a new project path (default form), migrating its Claude")
		fmt.Println("conversation history from ~/.claude/projects/<old>/ to <new>/.")
		fmt.Println()
		fmt.Println("With --to-profile, instead migrate the session row to another profile's DB,")
		fmt.Println("preserving all metadata and associated rows (cost_events, watcher_events).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session move my-project /new/path")
		fmt.Println("  agent-deck session move my-project /new/path --group work/frontend")
		fmt.Println("  agent-deck session move my-project /new/path --no-restart")
		fmt.Println("  agent-deck session move my-project /new/path --copy")
		fmt.Println("  agent-deck session move my-project --to-profile march")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Cross-profile migration mode (issue #928): different argument shape and
	// completely different code path. Branch as early as possible so the
	// existing path-move flow doesn't run.
	if *toProfile != "" {
		if fs.NArg() < 1 {
			out.Error("session move --to-profile requires <id|title>", ErrCodeInvalidOperation)
			fs.Usage()
			os.Exit(1)
		}
		if fs.NArg() > 1 {
			out.Error("--to-profile is incompatible with a <new-path> positional argument", ErrCodeInvalidOperation)
			fs.Usage()
			os.Exit(1)
		}
		// Reject path-move flags that don't apply when migrating across
		// profiles — silently ignoring them masks user mistakes. Detect via
		// flag.Visit which only enumerates flags that were explicitly set.
		var incompatible []string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "group", "no-restart", "copy":
				incompatible = append(incompatible, "--"+f.Name)
			}
		})
		if len(incompatible) > 0 {
			out.Error(fmt.Sprintf("--to-profile is incompatible with: %s", incompatible), ErrCodeInvalidOperation)
			fs.Usage()
			os.Exit(1)
		}
		handleSessionMoveToProfile(profile, *toProfile, fs.Arg(0), *force, out)
		return
	}

	if fs.NArg() < 2 {
		out.Error("session move requires <id|title> and <new-path>", ErrCodeInvalidOperation)
		fs.Usage()
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	newPath := fs.Arg(1)

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return
	}

	oldPath := inst.ProjectPath
	oldGroup := inst.GroupPath

	home, err := os.UserHomeDir()
	if err != nil {
		out.Error(fmt.Sprintf("resolve home dir: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if err := session.MigrateClaudeProjectDir(home, oldPath, newPath, *copyHistory); err != nil {
		out.Error(fmt.Sprintf("migrate claude history: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	inst.ProjectPath = newPath

	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	if *group != "" {
		targetGroupPath := *group
		if targetGroupPath == "root" {
			targetGroupPath = session.DefaultGroupPath
		}
		if _, ok := groupTree.Groups[targetGroupPath]; !ok && targetGroupPath != session.DefaultGroupPath {
			created := groupTree.CreateGroup(targetGroupPath)
			targetGroupPath = created.Path
		}
		groupTree.MoveSessionToGroup(inst, targetGroupPath)
	}

	if err := storage.SaveWithGroups(groupTree.GetAllInstances(), groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	restarted := false
	if !*noRestart && inst.Exists() {
		if err := inst.Restart(); err != nil {
			out.Error(fmt.Sprintf("session moved, but restart failed: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		if session.IsClaudeCompatible(inst.Tool) && inst.ClaudeSessionID == "" {
			inst.PostStartSync(3 * time.Second)
		}
		if err := saveSessionData(storage, instances, groups); err != nil {
			out.Error(fmt.Sprintf("failed to save after restart: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		restarted = true
	}

	out.Success(fmt.Sprintf("Moved %q: %s → %s", inst.Title, oldPath, newPath), map[string]interface{}{
		"success":   true,
		"id":        inst.ID,
		"title":     inst.Title,
		"old_path":  oldPath,
		"new_path":  newPath,
		"old_group": oldGroup,
		"new_group": inst.GroupPath,
		"restarted": restarted,
		"copied":    *copyHistory,
	})
}

// handleSessionMoveToProfile implements `session move <id> --to-profile <name>`
// (issue #928). The identifier is resolved against the source profile (and,
// if missing there, the target — preserving idempotency on re-runs), then
// the session row + cost_events + watcher_events are transferred to the
// target profile's state.db via session.MigrateSessionsToProfile.
func handleSessionMoveToProfile(sourceProfile, targetProfile, identifier string, force bool, out *CLIOutput) {
	// Resolve identifier → ID using the source profile's instance list. We do
	// this in the CLI layer (not in MigrateSessionsToProfile) because
	// ResolveSession lives in cmd/agent-deck and supports title/path lookup
	// that the storage layer does not.
	_, srcInstances, _, err := loadSessionData(sourceProfile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}
	inst, errMsg, errCode := ResolveSession(identifier, srcInstances)
	if inst == nil {
		// Idempotent re-run: the session may already be at the target.
		// Resolve there and let MigrateSessionsToProfile report it as a
		// SkippedIdempotent success. Skip if the target doesn't exist yet —
		// loadSessionData would auto-create it via NewStorageWithProfile and
		// mask the cleaner "target profile does not exist" error from
		// MigrateSessionsToProfile.
		if dstDir, derr := session.GetProfileDir(targetProfile); derr == nil {
			if _, statErr := os.Stat(filepath.Join(dstDir, "state.db")); statErr == nil {
				if _, dstInstances, _, lerr := loadSessionData(targetProfile); lerr == nil {
					if dstInst, _, _ := ResolveSession(identifier, dstInstances); dstInst != nil {
						inst = dstInst
					}
				}
			}
		}
	}
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return
	}

	result, err := session.MigrateSessionsToProfile(
		sourceProfile, targetProfile, []string{inst.ID},
		session.ProfileMigrateOptions{Force: force},
	)
	if err != nil {
		exitCode := ErrCodeInvalidOperation
		hint := ""
		switch {
		case errors.Is(err, session.ErrProfileMissing):
			exitCode = ErrCodeNotFound
		case errors.Is(err, session.ErrSameProfile):
			exitCode = ErrCodeInvalidOperation
		case errors.Is(err, session.ErrSessionRunning):
			hint = " (stop the session with `agent-deck session stop`, or re-run with --force)"
		}
		out.Error(fmt.Sprintf("%v%s", err, hint), exitCode)
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Migrated %q: profile %s → %s", inst.Title, sourceProfile, targetProfile), map[string]interface{}{
		"success":         true,
		"id":              inst.ID,
		"title":           inst.Title,
		"from_profile":    sourceProfile,
		"to_profile":      targetProfile,
		"cost_events":     result.MovedCostEvents,
		"watcher_events":  result.MovedWatcherEvents,
		"groups_created":  result.CreatedGroups,
		"already_at_dest": len(result.SkippedIdempotent) > 0,
	})
}
