package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionSwitchAccount switches a Claude session to another named
// account (#924) and migrates the conversation file into the target account's
// config dir, so the restarted session resumes with full context. The
// migration is copy-only: the old account keeps its copy.
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
	targetDir := userConfig.GetProfileClaudeConfigDir(account)
	if targetDir == "" {
		available := configuredAccountNames(userConfig)
		hint := "none configured"
		if len(available) > 0 {
			hint = strings.Join(available, ", ")
		}
		out.Error(fmt.Sprintf("account %q has no [profiles.%s.claude].config_dir in %s (configured accounts: %s)",
			account, account, effectiveUserConfigPathForHelp(), hint), ErrCodeInvalidOperation)
		os.Exit(1)
	}

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
	if inst.Tool != "claude" {
		out.Error(fmt.Sprintf("switch-account only supports claude sessions (tool: %s)", inst.Tool), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// IDs are synced from tmux (same pattern as `stop`) BEFORE the ambiguity
	// preflight below, since that check reads inst.ClaudeSessionID.
	wasRunning := inst.Exists()
	if wasRunning {
		inst.SyncSessionIDsFromTmux()
	}

	// Capture the source dir BEFORE mutating the account field — afterwards
	// the resolver would already return the target.
	srcDir := session.GetClaudeConfigDirForInstance(inst)
	// #1571: a pre-account-tracking session (empty account field) resolves to
	// env/profile/global defaults, which can equal the target dir — the old
	// code then declared "nothing to migrate" and the restarted resume died
	// with "No conversation found". The disk is authoritative: scan every
	// configured config dir for the conversation file and treat the dir that
	// actually contains it as the source.
	//
	// Review finding on #1830: this ambiguity preflight runs BEFORE the
	// running session is stopped below. Locating the conversation is a
	// read-only disk scan that does not need the session dead, and running
	// it first means an ambiguous-discovery abort leaves the session
	// untouched instead of stopping it for a switch that never happens.
	locatedDir, locatedSID, srcSize := session.LocateConversationConfigDir(userConfig, inst, srcDir)
	if locatedDir != "" {
		srcDir = locatedDir
		if inst.ClaudeSessionID == "" && locatedSID != "" {
			// #1815: with no recorded id, this is "the newest conversation in
			// the project dir" — a guess that a shared working directory can
			// answer with a NEIGHBOURING session's transcript. Copying first
			// and refusing to resume later is not good enough: the copy has
			// already carried someone else's conversation into another
			// account's config dir. Ambiguous discovery copies nothing.
			//
			// The operator can resolve the ambiguity explicitly and re-run.
			out.Error(fmt.Sprintf(
				"cannot identify %s's conversation: it has no recorded conversation id, and the only "+
					"candidate is the newest transcript in its working directory (%s), which may belong "+
					"to another session there. Nothing was copied and the account was NOT switched, "+
					"and the session was left running untouched. "+
					"Name the conversation explicitly if it is this session's own "+
					"(agent-deck session set claude-session-id <uuid> %s) and re-run.",
				inst.Title, locatedSID, inst.Title), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// Stop a running session so its conversation file is final before the
	// copy, now that the ambiguity preflight above has passed.
	if wasRunning {
		if err := inst.Kill(); err != nil {
			out.Error(fmt.Sprintf("failed to stop session before switch: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		// Re-locate for the final, post-stop size: Kill flushes the
		// transcript, so the preflight's size snapshot may be stale.
		if locatedDir != "" {
			if freshDir, _, freshSize := session.LocateConversationConfigDir(userConfig, inst, srcDir); freshDir != "" {
				locatedDir, srcDir, srcSize = freshDir, freshDir, freshSize
			}
		}
	}
	migrated, migErr := session.MigrateConversationFrom(inst, srcDir, targetDir)
	if migErr != nil && !errors.Is(migErr, session.ErrNoConversation) {
		// Conversation intact in the source account; account field unchanged.
		out.Error(fmt.Sprintf("conversation migration failed, account not switched: %v", migErr), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Post-switch verification (#1571): when a source conversation demonstrably
	// exists on disk, the target dir must contain it (size within ~1%) before
	// the account field flips. Fresh sessions (no conversation anywhere) keep
	// the lenient path.
	if locatedDir != "" {
		if verifyErr := session.VerifyConversationInDir(inst, targetDir, srcSize); verifyErr != nil {
			out.Error(fmt.Sprintf("conversation not verified in target config dir, account not switched: %v", verifyErr), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	// #1815 Guard 2: a failed transcript verification must STOP the sequence,
	// not annotate it.
	if blocked, why := switchAccountRestartUnsafe(inst, locatedDir, wasRunning, *noRestart); blocked {
		out.Error(fmt.Sprintf("conversation verification failed for %s: %s", inst.Title, why), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Pre-seed the folder-trust entry for (target config dir, project path)
	// (#1571 root cause 4): the first launch of a fresh (config_dir, cwd)
	// pair blocks interactively on "Do you trust this folder?", stalling the
	// restarted session. Best-effort — a warning must not abort the switch.
	if trustErr := session.PreAcceptClaudeTrust(filepath.Join(targetDir, ".claude.json"), inst.EffectiveWorkingDir()); trustErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not pre-accept folder trust in target config dir: %v\n", trustErr)
	}

	oldAccount, postCommit, setErr := session.SetField(inst, session.FieldAccount, account, nil)
	if setErr != nil {
		out.Error(setErr.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}
	if postCommit != nil {
		postCommit()
	}

	restarted := false
	var startErr error
	if wasRunning && !*noRestart {
		if startErr = inst.Start(); startErr == nil {
			inst.LastStartedAt = time.Now()
			restarted = true
		}
	}

	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	if startErr != nil {
		out.Error(fmt.Sprintf("account switched to %q and conversation migrated, but restart failed: %v (start it manually with: agent-deck session start %s)",
			account, startErr, inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	conversation := "no conversation to migrate (fresh session)"
	if migrated != "" {
		conversation = fmt.Sprintf("conversation migrated to %s", migrated)
	} else if migErr == nil {
		if locatedDir != "" {
			conversation = "conversation already present in the target config dir (nothing to migrate)"
		} else {
			conversation = "no conversation found on disk (nothing to migrate)"
		}
	}
	out.Success(fmt.Sprintf("Switched %s: account %q -> %q; %s", inst.Title, oldAccount, account, conversation), map[string]interface{}{
		"success":           true,
		"id":                inst.ID,
		"title":             inst.Title,
		"old_account":       oldAccount,
		"new_account":       account,
		"migrated_path":     migrated,
		"claude_session_id": inst.ClaudeSessionID,
		"restarted":         restarted,
	})
}

// switchAccountRestartUnsafe reports whether switch-account must ABORT rather
// than restart the session, and why (#1815 Guard 2).
//
// In the reported incident the switch correctly diagnosed that it had no
// transcript to move ("no conversation to migrate, fresh session") for a
// session that had demonstrably been running a conversation — and then
// restarted it anyway. The restart's disk-discovery prelude, with no id to
// work from, adopted the newest transcript filed under the shared working
// directory (one belonging to a different session) and resumed it. A failed
// verification has to stop the sequence.
//
// The discriminator is ClaudeDetectedAt: a session that never bound a
// conversation id has nothing to lose and nothing to hijack (the Start
// prelude's #608 gate skips discovery for it). A session that HAS run,
// arriving here with no recorded id and no conversation located in ANY
// configured config dir, is exactly the unsafe state.
//
// The abort is scoped to the case where this command would itself perform the
// restart; --no-restart (or an already-stopped session) is the documented way
// through, and the resume-time identity guard covers the later manual start.
func switchAccountRestartUnsafe(inst *session.Instance, locatedDir string, wasRunning, noRestart bool) (bool, string) {
	if inst == nil || !wasRunning || noRestart {
		return false, ""
	}
	if locatedDir != "" || inst.ClaudeSessionID != "" || inst.ClaudeDetectedAt.IsZero() {
		return false, ""
	}
	return true, "this session previously held a Claude conversation, but none was found in any " +
		"configured config dir and it has no recorded conversation id; account not switched and " +
		"session NOT restarted (restarting in this state can adopt a transcript belonging to another " +
		"session in the same directory). Re-run with --no-restart to switch the account only, then " +
		"start it manually once the conversation is located"
}

// configuredAccountNames lists profile names that have a Claude config_dir —
// i.e. the valid <account> values for switch-account / `set account`.
func configuredAccountNames(cfg *session.UserConfig) []string {
	if cfg == nil {
		return nil
	}
	var names []string
	for name := range cfg.Profiles {
		if cfg.GetProfileClaudeConfigDir(name) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
