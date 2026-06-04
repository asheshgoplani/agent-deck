package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionArchive marks a session as archived (stop + hide from default
// list; worktree and Claude transcripts are preserved). Use `session unarchive`
// to restore.
func handleSessionArchive(profile string, args []string) {
	fs := flag.NewFlagSet("session archive", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	keepRunning := fs.Bool("keep-running", false, "Archive without stopping the running process")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session archive <id|title> [options]")
		fmt.Println()
		fmt.Println("Archive a session: stops the process (unless --keep-running),")
		fmt.Println("hides the session from the default list, and preserves the")
		fmt.Println("worktree and Claude transcripts. Use `session unarchive` to restore.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	if identifier == "" {
		out.Error("usage: session archive <id|title>", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	if inst.IsArchived() {
		out.Error(fmt.Sprintf("session '%s' is already archived", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if !*keepRunning {
		_ = inst.KillAndWait()
	}

	inst.ArchivedAt = time.Now()

	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Archived session: %s", inst.Title), map[string]interface{}{
		"success":  true,
		"id":       inst.ID,
		"title":    inst.Title,
		"archived": true,
	})
}

// handleSessionUnarchive restores an archived session to stopped state,
// making it visible in the default session list again.
func handleSessionUnarchive(profile string, args []string) {
	fs := flag.NewFlagSet("session unarchive", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session unarchive <id|title> [options]")
		fmt.Println()
		fmt.Println("Unarchive a session, returning it to stopped state and making it")
		fmt.Println("visible in the default session list.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	if identifier == "" {
		out.Error("usage: session unarchive <id|title>", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	if !inst.IsArchived() {
		out.Error(fmt.Sprintf("session '%s' is not archived", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	inst.ArchivedAt = time.Time{}

	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Unarchived session: %s", inst.Title), map[string]interface{}{
		"success":  true,
		"id":       inst.ID,
		"title":    inst.Title,
		"archived": false,
	})
}
