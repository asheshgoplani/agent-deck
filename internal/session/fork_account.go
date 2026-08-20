package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Fork account override (#924 follow-up).
//
// A fork inherits its parent's account because the transcript it resumes lives
// in that account's home — `claude --resume <id>` is a pure file lookup under
// <config-dir>/projects/<encoded-cwd>/<id>.jsonl, so pointing the fork at a
// different CLAUDE_CONFIG_DIR without moving the file resumes nothing.
//
// `session fork --account <name>` used to set the account on the RECORD after
// the one-shot fork command had already been baked against the parent's home.
// The record then claimed an account the process was not running under for the
// whole life of that process, and the override only took effect on the fork's
// first restart — which drops --resume and strands the conversation.
//
// Rebaking the command against the override home instead is worse, not better:
// the transcript is not there, so the fork launches into
// "No conversation found with session ID: <id>" and dies. Right conversation
// under the wrong account is a lie; no conversation at all is data loss.
//
// The fix is the shape `session switch-account` already uses in production
// (#1377): carry the conversation into the target account's home FIRST, verify
// it landed, and only then build the command that will run there. The account
// on the record and the account the process launches under are then the same
// value by construction — the command is derived from the record, not set
// beside it.

// ForkAccountOverride reports what ApplyForkAccountOverride did, for the
// caller's user-facing output.
type ForkAccountOverride struct {
	// Account is the account the fork was moved onto.
	Account string
	// Home is that account's config home for the fork's tool.
	Home string
	// EnvVar is the variable Home is exported as at launch.
	EnvVar string
	// SourceHome is the config home the conversation was copied FROM.
	SourceHome string
	// MigratedPath is where the conversation now lives under Home, or "" when
	// it was already there (an override naming the account the fork already
	// inherited).
	MigratedPath string
	// LoggedOut is true when Home carries no credential marker, so the fork
	// will launch into the tool's own login flow. A warning, not an error:
	// CLAUDE_CONFIG_DIR relocates claude's own config file, so a freshly
	// created account home legitimately needs its first login.
	LoggedOut bool
	// TrustWarning carries a non-fatal failure to pre-accept the folder-trust
	// entry for (Home, the fork's working dir).
	TrustWarning error
}

// ApplyForkAccountOverride moves a freshly created, NOT YET STARTED fork onto
// an account other than the one it inherited from its parent, and returns nil
// when account is empty (no override requested).
//
// It must be called after CreateForkedInstanceForTool and before forked.Start().
// On any error nothing is persisted and the fork must not be started: the
// caller is expected to abort, so that no record is ever saved claiming an
// account its process would not run under.
//
// Steps, in order, each of which must succeed before the next runs:
//
//  1. Refuse anything the migration cannot honour — an unknown account, an
//     account with no home for this tool, a non-claude parent, a parent with
//     no conversation on disk.
//  2. Copy the parent's conversation into the target account's home and verify
//     it arrived (copy-only; the parent's account keeps its copy).
//  3. Write the account onto the fork's record.
//  4. Rebuild the one-shot fork command, which reads the account back off the
//     record through the config-dir resolver.
func ApplyForkAccountOverride(parent, forked *Instance, cfg *UserConfig, account string, opts *ClaudeOptions) (*ForkAccountOverride, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, nil
	}
	if parent == nil || forked == nil {
		return nil, fmt.Errorf("account override needs both the parent and the forked instance")
	}

	family, hasFamily := AccountFamilyForTool(forked.Tool)
	if !hasFamily {
		return nil, fmt.Errorf("tool %q has no account family, so --account means nothing for this fork", forked.Tool)
	}
	// The conversation migration below is claude's transcript layout. A codex
	// or deepseek fork would need its own rollout-moving primitive; until one
	// exists, refusing is the only honest answer — recording the account
	// without moving the history is the defect this function exists to fix.
	if parent.Tool != "claude" || forked.Tool != "claude" {
		return nil, fmt.Errorf("--account on a fork is supported for claude sessions only (tool: %s): moving a fork onto another account means carrying its conversation into that account's home, and only claude's transcript layout supports that today",
			forked.Tool)
	}

	home := AccountHomeForTool(cfg, account, forked.Tool)
	if home == "" {
		return nil, fmt.Errorf("account %q has no home configured for tool %q (configured: %s) — refusing rather than recording an account the fork would not run under",
			account, forked.Tool, accountNamesOrNone(cfg, forked.Tool))
	}

	resumeID := strings.TrimSpace(parent.ClaudeSessionID)
	if resumeID == "" {
		return nil, fmt.Errorf("cannot move this fork onto account %q: the parent has no recorded conversation id, so there is no transcript to carry into that account's home",
			account)
	}

	// Locate the transcript the baked --resume names. Scanning every
	// configured home (rather than trusting the parent's account field) is the
	// #1571 lesson: a pre-account session's record can name a different dir
	// than the one the file is actually in.
	srcHome := GetClaudeConfigDirForInstance(parent)
	locatedHome, _, srcSize := LocateConversationConfigDir(cfg, parent, srcHome)
	if locatedHome == "" {
		return nil, fmt.Errorf("cannot move this fork onto account %q: no conversation file for %s was found in any configured config dir, so nothing can be carried into that account's home and the fork's --resume would find nothing there",
			account, resumeID)
	}

	// MigrateConversationFrom falls back to "newest file in the project dir"
	// when the recorded id has no file, adopting a different id onto the
	// instance as it goes. That cannot fire here: LocateConversationConfigDir
	// returned locatedHome precisely because <resumeID>.jsonl is in it, so the
	// exact-id path is taken and parent.ClaudeSessionID — the id already baked
	// into the fork command as --resume — is left alone.
	migrated, err := MigrateConversationFrom(parent, locatedHome, home)
	if err != nil && !errors.Is(err, ErrNoConversation) {
		return nil, fmt.Errorf("conversation migration into account %q failed, fork not created: %w", account, err)
	}
	if err := VerifyConversationInDir(parent, home, srcSize); err != nil {
		return nil, fmt.Errorf("conversation not verified in account %q's home, fork not created: %w", account, err)
	}

	result := &ForkAccountOverride{
		Account:      account,
		Home:         home,
		EnvVar:       family.EnvVar,
		SourceHome:   locatedHome,
		MigratedPath: migrated,
	}
	if family.loginProbe != nil && family.loginProbe(home) == AccountLoginOut {
		result.LoggedOut = true
	}

	// Pre-seed folder trust for the (target home, fork working dir) pair
	// (#1571 root cause 4): the first launch of a fresh pair otherwise blocks
	// interactively on "Do you trust this folder?", stalling the fork. Best
	// effort — reported, never fatal.
	if trustErr := PreAcceptClaudeTrust(filepath.Join(home, ".claude.json"), forked.EffectiveWorkingDir()); trustErr != nil {
		result.TrustWarning = trustErr
	}

	// Record first, command second. buildClaudeForkCommandForTarget builds its
	// env prefix by resolving the TARGET's account, so the launched
	// CLAUDE_CONFIG_DIR is derived from the field `session show` and
	// `accounts list` read back. They cannot drift apart, because there is
	// only one value.
	previousAccount := forked.Account
	forked.Account = account
	cmd, buildErr := parent.buildClaudeForkCommandForTarget(forked, opts)
	if buildErr != nil {
		forked.Account = previousAccount
		return nil, fmt.Errorf("rebuilding the fork command under account %q failed, fork not created: %w", account, buildErr)
	}
	forked.Command = cmd
	// #745: Start() must run this command verbatim rather than rebuilding it,
	// or --resume/--fork-session are dropped. Already set by the fork
	// constructor; reasserted because the command has just been replaced.
	forked.IsForkAwaitingStart = true

	return result, nil
}

// accountNamesOrNone renders the accounts configured for a tool, for error
// hints. Mirrors the CLI's accountNamesHint without importing it.
func accountNamesOrNone(cfg *UserConfig, tool string) string {
	names := AccountNamesForTool(cfg, tool)
	if len(names) == 0 {
		return "none configured"
	}
	return strings.Join(names, ", ")
}
