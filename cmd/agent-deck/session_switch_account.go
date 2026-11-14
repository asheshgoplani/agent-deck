package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// switchAccountExecute is session.SwitchAccount, a seam for tests.
var switchAccountExecute = session.SwitchAccount

// handleSessionSwitchAccount switches a Claude session to another named
// account (#924) and migrates the conversation file into the target account's
// config dir, so the restarted session resumes with full context. The
// migration is copy-only: the old account keeps its copy.
//
// The flow itself lives in session.SwitchAccount, shared with the TUI's Edit
// Session dialog; this handler only parses flags, resolves the session,
// persists the result and renders output.
func handleSessionSwitchAccount(profile string, args []string) {
	fs := flag.NewFlagSet("session switch-account", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	noRestart := fs.Bool("no-restart", false, "Do not restart a running session after the switch")
	noSettle := fs.Bool("no-settle", false, "After the restart, do not run switch-settle (ready, known prompts, plugins, kick, verify)")
	settleStrict := fs.Bool("settle-strict", false, "Exit 4 (3: stopped at a prompt) when the switch succeeded but the settle did not end healthy")
	archiveDestination := fs.Bool("archive-destination", false, "Install the source conversation even when the destination holds a newer or undated copy; the destination copy is archived next to it, never deleted")

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
		fmt.Println("After the restart the switch settles hands-free (see `session switch-settle`):")
		fmt.Println("it answers only the known first-start prompts, checks plugins and MCPs, sends a")
		fmt.Println("continue message and verifies the account in use, then prints one receipt line")
		fmt.Println("(\"settle <title>: ok|FAILED|STOPPED ...\"; JSON: \"settle\"). The exit code stays 0")
		fmt.Println("for a completed switch; --settle-strict exits 4 (3: stopped at a prompt) when the")
		fmt.Println("settle is not healthy. --no-settle skips it.")
		fmt.Println()
		fmt.Println("Run from inside the session it targets, the switch is handed to a detached")
		fmt.Println("agent-deck (the restart ends the calling pane): the command prints that")
		fmt.Println("process's pid and log file and exits 0; the switch's own result is in the log.")
		fmt.Println()
		fmt.Println("Claude Code may key one working directory under several project directories")
		fmt.Println("(the path as typed, its macOS /private form, its realpath). Every copy of the")
		fmt.Println("conversation under those keys in BOTH accounts is considered; the newest by")
		fmt.Println("last event (tie: longest) wins, is installed under every key in the target,")
		fmt.Println("and each copy it replaces is backed up next to it. A newer target copy is")
		fmt.Println("kept, never overwritten. The receipt names the chosen copy; --json carries")
		fmt.Println("every candidate under \"transcript\".")
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

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	if fs.NArg() < 2 {
		fs.Usage()
		fmt.Fprintln(os.Stderr, "Error: session switch-account needs <id|title> and <account>")
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	account := strings.TrimSpace(fs.Arg(1))
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	userConfig, _ := session.LoadUserConfig()

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		switchAccountError(out, *jsonOutput, err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}
	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		switchAccountError(out, *jsonOutput, errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	// #2897: from inside the target, the switch's first stop ends this
	// process; hand it to a detached child. A refusal or a no-op stops
	// nothing, so those still answer here.
	handOff := true
	if callerRunsInsideTarget(inst) {
		preview := session.PreviewSwitch(userConfig, inst, session.SwitchPreviewTarget{Harness: inst.Tool, Account: account})
		handOff = preview.Refusal == nil && preview.TargetAccount != inst.Account
	}
	if handOff && handOffIfSelfTarget(out, profile, "switch-account", args, inst) {
		return
	}

	// The tree the restart replaces; settle waits for it to be gone.
	var oldPIDs []int
	if !*noRestart && !*noSettle {
		oldPIDs = session.SnapshotPaneProcesses(inst)
	}
	result, switchErr := switchAccountExecute(userConfig, inst, account, session.AccountSwitchOptions{
		NoRestart:          *noRestart,
		Storage:            storage,
		ArchiveDestination: *archiveDestination,
	})
	if result == nil {
		// Every abort path leaves the instance untouched, so there is nothing
		// to persist.
		message := switchErr.Error()
		if errors.Is(switchErr, session.ErrSwitchDestinationDivergent) {
			message += "; re-run with --archive-destination to archive it and switch anyway"
		}
		switchAccountError(out, *jsonOutput, message, ErrCodeInvalidOperation)
		exitCLI(1)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}

	if err := session.CommitAccountSwitch(storage, inst, result); err != nil {
		switchAccountError(out, *jsonOutput, fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	if switchErr != nil {
		// The account WAS switched and the conversation migrated (both now
		// persisted); only the restart failed.
		if errors.Is(switchErr, session.ErrAccountSwitchRestartFailed) {
			switchAccountError(out, *jsonOutput, fmt.Sprintf("account switched to %q and conversation migrated, but %v", account, switchErr),
				ErrCodeInvalidOperation)
		} else {
			switchAccountError(out, *jsonOutput, switchErr.Error(), ErrCodeInvalidOperation)
		}
		exitCLI(1)
	}

	var settle *session.SettleReport
	var settleErr error
	if result.Restarted && !*noSettle && session.IsClaudeCompatible(inst.Tool) {
		settle, settleErr = settleAfterSwitch(inst, quietMode || *jsonOutput, oldPIDs, func() error { return saveSessionData(storage, instances, groups) })
	}
	summary := fmt.Sprintf("Switched %s: account %q -> %q; %s", inst.Title, result.OldAccount, result.NewAccount, result.Conversation)
	if settle != nil {
		summary += "\n" + settle.Receipt()
	}
	defer func() {
		if code := switchSettleExit(*settleStrict, settle, settleErr); code != 0 {
			fmt.Fprintf(os.Stderr, "Error: switched, but the settle did not end healthy: %s\n", settle.Receipt())
			exitCLI(code)
		}
	}()
	out.Success(summary,
		map[string]interface{}{
			"settle":               settle,
			"success":              true,
			"id":                   inst.ID,
			"title":                inst.Title,
			"old_account":          result.OldAccount,
			"new_account":          result.NewAccount,
			"migrated_path":        result.MigratedPath,
			"destination_archived": result.DestinationArchived,
			"transcript":           result.Transcript,
			"claude_session_id":    inst.ClaudeSessionID,
			"restarted":            result.Restarted,
		})
}

// switchAccountError reports a failure as the command's error output and,
// in --json mode (where that output is JSON on stdout), also as one line on
// stderr: every non-zero exit of switch-account leaves a readable reason on
// stderr, which a caller filtering stdout (or a log of stderr) still keeps.
func switchAccountError(out *CLIOutput, jsonMode bool, message, code string) {
	// One line: a tmux or storage error can carry its tool's multi-line
	// output.
	message = strings.Join(strings.Fields(message), " ")
	out.Error(message, code)
	if jsonMode {
		fmt.Fprintf(os.Stderr, "Error: %s\n", message)
	}
}
