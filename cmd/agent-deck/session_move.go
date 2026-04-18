package main

import (
	"flag"
	"fmt"
	"os"
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

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session move <id|title> <new-path> [options]")
		fmt.Println()
		fmt.Println("Move a session to a new project path, migrating its Claude")
		fmt.Println("conversation history from ~/.claude/projects/<old>/ to <new>/.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session move my-project /new/path")
		fmt.Println("  agent-deck session move my-project /new/path --group work/frontend")
		fmt.Println("  agent-deck session move my-project /new/path --no-restart")
		fmt.Println("  agent-deck session move my-project /new/path --copy")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

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
