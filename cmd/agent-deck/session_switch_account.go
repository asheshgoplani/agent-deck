package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionSwitchAccount switches a Claude session to another named
// account (#924) and migrates the conversation file into the target account's
// config dir, so the restarted session resumes with full context. The
// migration is copy-only: the old account keeps its copy.
//
// The flow itself lives in session.SwitchAccount, shared with the TUI's Edit
// Session dialog; this handler only parses flags, resolves the session,
// persists the result and renders output.
func handleSessionSwitchAccount(profile string, args []string) {
	fs := flag.NewFlagSet("session switch-account", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	noRestart := fs.Bool("no-restart", false, "Do not restart a running session after the switch")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session switch-account <id|title> <account> [options]")
		fmt.Println()
		fmt.Println("Switch a Claude session to another account and carry the conversation over.")
		fmt.Println()
		fmt.Printf("<account> must have a [profiles.<account>.claude].config_dir block in %s.\n", effectiveUserConfigPathForHelp())
		fmt.Println("Run `agent-deck accounts` to list the configured accounts.")
		fmt.Println("The conversation file is COPIED into the target account's config dir (the")
		fmt.Println("source account keeps its copy), the session's account field is updated, and")
		fmt.Println("a running session is restarted so `claude --resume` continues the")
		fmt.Println("conversation under the new account.")
		fmt.Println()
		fmt.Println("If the session has no recorded conversation id and the only candidate is the")
		fmt.Println("newest transcript in its working directory (which may belong to another")
		fmt.Println("session sharing that directory), the switch is refused instead of guessing:")
		fmt.Println("nothing is copied, the account is not switched, and a running session is left")
		fmt.Println("untouched. Name the conversation explicitly with")
		fmt.Println("`agent-deck session set claude-session-id <uuid>` and re-run.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session switch-account my-project work")
		fmt.Println("  agent-deck session switch-account my-project personal --no-restart")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	account := strings.TrimSpace(fs.Arg(1))
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	userConfig, _ := session.LoadUserConfig()

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
		return // unreachable, satisfies staticcheck SA5011
	}

	result, switchErr := session.SwitchAccount(userConfig, inst, account, session.AccountSwitchOptions{
		NoRestart: *noRestart,
	})
	if result == nil {
		// Every abort path leaves the instance untouched, so there is nothing
		// to persist.
		out.Error(switchErr.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}

	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if switchErr != nil {
		// The account WAS switched and the conversation migrated (both now
		// persisted); only the restart failed.
		if errors.Is(switchErr, session.ErrAccountSwitchRestartFailed) {
			out.Error(fmt.Sprintf("account switched to %q and conversation migrated, but %v", account, switchErr),
				ErrCodeInvalidOperation)
		} else {
			out.Error(switchErr.Error(), ErrCodeInvalidOperation)
		}
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Switched %s: account %q -> %q; %s", inst.Title, result.OldAccount, result.NewAccount, result.Conversation),
		map[string]interface{}{
			"success":           true,
			"id":                inst.ID,
			"title":             inst.Title,
			"old_account":       result.OldAccount,
			"new_account":       result.NewAccount,
			"migrated_path":     result.MigratedPath,
			"claude_session_id": inst.ClaudeSessionID,
			"restarted":         result.Restarted,
		})
}
