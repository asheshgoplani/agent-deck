package session

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// buildClaudeCommand builds the claude command with session capture
// For new sessions: captures session ID via print mode, stores in tmux env, then resumes
// This ensures we always know the session ID for fork/restart features
// Respects: CLAUDE_CONFIG_DIR, dangerous_mode from user config, and [shell].env_files
func (i *Instance) buildClaudeCommand(baseCommand string) string {
	envPrefix := i.buildEnvSourceCommand()
	cmd := i.buildClaudeCommandWithMessage(baseCommand, "")
	return envPrefix + cmd
}

// buildClaudeCommandWithMessage builds the command with optional initial message
// Respects ClaudeOptions from instance if set, otherwise falls back to config defaults
func (i *Instance) buildClaudeCommandWithMessage(baseCommand, message string) string {
	if !IsClaudeCompatible(i.Tool) {
		return baseCommand
	}

	// Default empty baseCommand to "claude" so the Claude-build branch below
	// runs. An Instance row with tool=claude and an empty Command field
	// (e.g. a session whose tool_data lost its ClaudeSessionID and was
	// never assigned an explicit Command) otherwise falls all the way
	// through to the custom-command branch and returns just the env
	// prefix — pane runs `export ...;` and exits, status loops to error.
	// See feature/sessions-dispear-on-restart, Smithy repro 2026-04-27.
	if baseCommand == "" {
		baseCommand = "claude"
	}

	// Get the configured Claude command (e.g., "claude", "cdw", "cdp"),
	// resolved per instance: conductor > group (ancestor-walk) > global.
	claudeCmd := GetClaudeCommandForInstance(i)

	// #1791/#1822 F5: every branch below (continue/-c, resume/--resume,
	// resume-picker/-r, and fresh --session-id) shares a single `export ...;`
	// prefix rather than each building its own inline `KEY=val cmd` form.
	// A bare `KEY=val cmd` prefix scopes the variable to that ONE command
	// only (standard shell semantics) — when exit_to_shell (#1161) then
	// appends `; exec "$SHELL" -i` after the agent exits, that fallback
	// shell does NOT inherit a non-exported prefix var, so CLAUDE_CONFIG_DIR
	// silently reverts to the ambient ~/.claude root the moment the user
	// exits Claude and gets dropped into the pane's shell — the exact
	// wrong-account symptom #1791 exists to fix, just one step later than
	// the original repro (Codex review, PR #1822). `export` persists the
	// variable in the wrapping bash process's own environment, so it
	// survives into anything that process execs afterward, including the
	// exit-to-shell fallback. See buildBashExportPrefix for what it emits
	// (AGENTDECK_INSTANCE_ID/PROFILE always; CLAUDE_CONFIG_DIR only when
	// IsClaudeConfigDirExplicitForInstance(i) AND claudeCmd is the literal
	// "claude" binary — #1822 F3: when claudeCmd is a custom alias like
	// "cdw"/"cdp", the deck must not also export CLAUDE_CONFIG_DIR here, or
	// it overrides the alias's own resolution of which account to use).
	bashExportPrefix := i.buildBashExportPrefix(claudeCmd != "claude")

	// Get options - either from instance or create defaults from config
	opts := i.GetClaudeOptions()
	if opts == nil {
		// Fall back to config defaults
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}

	// S8 (v1.7.40) defense-in-depth: non-channel-owning claude spawns
	// wrap the final exec in `env -u TELEGRAM_*` so the child process
	// is guaranteed to start without telegram env even if the shell
	// unset in buildEnvSourceCommand is somehow bypassed. #1133
	// broadens the flag list from TELEGRAM_STATE_DIR alone to every
	// var in telegramEnvVarsToStrip. Empty string for conductors,
	// explicit telegram channel owners, --inherit-telegram-env opt-in,
	// and non-claude tools (see telegramStateDirStripExpr predicate).
	execEnvPrefix := ""
	if flags := telegramExecEnvStripFlags(i); flags != "" {
		execEnvPrefix = "env " + flags + " "
	}

	// If baseCommand is just "claude", build the appropriate command
	if baseCommand == "claude" {
		// Build extra flags string from options (includes --add-dir if ParentProjectPath set)
		extraFlags := i.buildClaudeExtraFlags(opts)

		// Handle different session modes
		switch opts.SessionMode {
		case "continue":
			// Simple -c mode: continue last session.
			//
			// #1815: `-c` asks Claude to pick "the latest conversation in this
			// directory" — the same mtime guess that started the incident,
			// made inside the CLI where this guard cannot see it. When the
			// session has a conversation id of its own, resume THAT instead:
			// same intent, verified identity. `-c` is left as-is only when
			// there is no owned id to prefer, since it is then the operator's
			// explicit instruction and there is nothing better to offer.
			//
			// KNOWN OPEN RESIDUAL (review finding on #1830, not yet fixed):
			// this function (buildClaudeCommand) is only dispatched to when
			// i.ClaudeSessionID == "" (Start/StartWithMessage/the restart
			// recreate fallback all route to buildClaudeResumeCommand
			// whenever the id is non-empty), and recordedClaudeSessionID()
			// derives from that same field. So on every REACHABLE production
			// path `recorded` is always "" here and this mitigation branch
			// never fires -- `claude -c` still ships, leaving the CLI's own
			// newest-in-directory guess as the residual exposure this branch
			// was meant to close. Only the test that calls this builder
			// directly exercises it. Fixing this needs buildClaudeResumeCommand
			// (or the three dispatch call sites) reworked to route continue-mode
			// through a point where the id can be non-empty, which is deferred
			// as a follow-up rather than attempted blind here.
			if recorded := i.recordedClaudeSessionID(); recorded != "" {
				return fmt.Sprintf(`%sexec %s%s --resume %s%s`,
					bashExportPrefix, execEnvPrefix, claudeCmd, shellescape.Quote(recorded), extraFlags)
			}
			// #2301: with no owned id, `-c` used to ship unconditionally and
			// let the CLI itself pick "the newest conversation in this
			// directory" — even when there is no conversation at all, which
			// makes the CLI print "No conversation found to continue" and
			// exit instead of starting. Check transcript existence first so
			// a directory that has never run Claude falls through to a
			// fresh session instead of a guaranteed-dead `-c`.
			//
			// The existence check walks every config-dir root this instance
			// could plausibly have written to (account/conductor/group/env
			// slots, plus every other configured account slot, plus the
			// default), not just the process-wide default dir — an
			// account/conductor/group-scoped instance's transcript lives
			// elsewhere, and a single-root check would wrongly declare "no
			// transcript" and mint a fresh session over a resumable one.
			// A root that can't be read (uncertain) fails towards resume:
			// only a confirmed absence across every root starts fresh.
			if i.TranscriptIsResolvableLocally() {
				roots := claudeConfigDirRootsForInstance(i)
				found, uncertain := transcriptEvidenceAcrossRoots(i.EffectiveWorkingDir(), roots)
				if !found && !uncertain {
					sessionLog.Info("resume: none reason=continue_mode_no_transcript",
						slog.String("instance_id", logging.SanitizeValue(i.ID)),
						slog.String("path", logging.SanitizeValue(i.EffectiveWorkingDir())),
						slog.String("reason", "continue_mode_no_transcript"))
					freshID := generateUUID()
					i.ClaudeSessionID = freshID
					i.markClaudeSessionIDVerified()
					return fmt.Sprintf(`%sexec %s%s --session-id "%s"%s`,
						bashExportPrefix, execEnvPrefix, claudeCmd, freshID, extraFlags)
				}
			}
			sessionLog.Warn("resume: continue_mode_unverifiable",
				slog.String("instance_id", logging.SanitizeValue(i.ID)),
				slog.String("path", logging.SanitizeValue(i.ProjectPath)),
				slog.String("reason", "continue_flag_picks_newest_conversation_in_dir"))
			return fmt.Sprintf(`%sexec %s%s -c%s`, bashExportPrefix, execEnvPrefix, claudeCmd, extraFlags)

		case "resume":
			// Resume specific session by ID
			if opts.ResumeSessionID != "" {
				// #1815: identity first (the id must be this session's own),
				// then the conversation-data / project-dir check. Both live
				// behind canResumeClaudeSession.
				if canResumeClaudeSession(i, opts.ResumeSessionID) {
					// Session has conversation history - use normal --resume
					return fmt.Sprintf(`%sexec %s%s --resume %s%s`,
						bashExportPrefix, execEnvPrefix, claudeCmd, shellescape.Quote(opts.ResumeSessionID), extraFlags)
				}
				// Session was never interacted with - use --session-id with same UUID.
				// CLAUDE_SESSION_ID is propagated via host-side SyncSessionIDsToTmux after start.
				//
				// #1815: when the refusal was an IDENTITY refusal the id may
				// belong to another session, so it must not be reused via
				// --session-id either (a shared id also makes the duplicate
				// sweeper kill one of the pair). Mint a fresh one.
				freshID := opts.ResumeSessionID
				if !i.resumeIdentityAllowed(opts.ResumeSessionID).Allow {
					freshID = i.replaceRefusedClaudeSessionID()
				}
				// freshID may be the session's own (settable) id: keep the
				// `"<uuid>"` text for an ordinary id, but shell-quote anything
				// else, since $(...) still runs inside double quotes.
				sessionIDArg := `"` + freshID + `"`
				if q := shellescape.Quote(freshID); q != freshID {
					sessionIDArg = q
				}
				return fmt.Sprintf(
					`%sexec %s%s --session-id %s%s`,
					bashExportPrefix, execEnvPrefix, claudeCmd, sessionIDArg, extraFlags)
			}
			// No bound picker choice exists until Claude accepts the selection.
			extraFlags = i.buildClaudeExtraFlagsWithName(opts, "")
			return fmt.Sprintf(`%sexec %s%s -r%s`, bashExportPrefix, execEnvPrefix, claudeCmd, extraFlags)
		}

		// Default: new session with capture-resume pattern
		// 1. Starts Claude in print mode to get session ID
		// 2. Stores session ID in tmux environment (if capture succeeded)
		// 3. Resumes that session interactively
		// Fallback ensures Claude starts (without fork/restart support) rather than failing completely
		//
		// NOTE: These commands get wrapped in `bash -c` for fish compatibility (#47),
		// so shell aliases won't work — but real binaries/scripts are fine.
		// bashExportPrefix is the shared export prefix built above.

		// Pre-generate UUID in Go to avoid shell uuidgen (may be absent in Docker sandbox).
		// CLAUDE_SESSION_ID is also propagated via host-side SetEnvironment after tmux start.
		// Use `exec` before the final claude invocation so that when this compound
		// command is wrapped in `bash -c` (for fish compatibility), exec replaces
		// the bash process with claude, enabling proper job control (Ctrl+Z suspend / fg resume).
		sessionUUID := generateUUID()
		i.ClaudeSessionID = sessionUUID
		// #1815: minted here for this session — vouched ownership.
		i.markClaudeSessionIDVerified()

		// Startup query (#725, v1.7.67): appended as one shell-quoted
		// positional arg so multi-word queries survive bash -c. Empty
		// string means no suffix — do NOT emit empty quotes (claude would
		// treat them as an empty prompt and block).
		startupQuerySuffix := ""
		if i.StartupQuery != "" {
			startupQuerySuffix = " " + shellescape.Quote(i.StartupQuery)
		}

		var baseCmd string
		// Use pre-generated literal UUID with --session-id flag.
		// CLAUDE_SESSION_ID is propagated via host-side SetEnvironment after tmux start.
		baseCmd = fmt.Sprintf(
			`%sexec %s%s --session-id "%s"%s%s`,
			bashExportPrefix, execEnvPrefix, claudeCmd, sessionUUID, extraFlags, startupQuerySuffix)

		// If message provided, append wait-and-send logic in background.
		if message != "" {
			// Escape single quotes in message for bash
			escapedMsg := strings.ReplaceAll(message, "'", "'\"'\"'")

			// The background subshell runs independently; exec replaces
			// the current shell with claude for proper job control.
			baseCmd = fmt.Sprintf(
				`(sleep 2; SESSION_NAME=$(tmux display-message -p '#S'); `+
					`while ! tmux capture-pane -p -t "$SESSION_NAME" | tail -5 | grep -qE "^>"; do sleep 0.2; done; `+
					`tmux send-keys -l -t "$SESSION_NAME" -- '%s' \; send-keys -t "$SESSION_NAME" Enter) & `+
					`%sexec %s%s --session-id "%s"%s`,
				escapedMsg,
				bashExportPrefix, execEnvPrefix, claudeCmd, sessionUUID, extraFlags)
		}

		return baseCmd
	}

	// For custom commands (e.g., fork commands or conductor wrappers), prepend
	// the env-source prefix (CFG-03) and the bash export prefix (CFG-02) so
	// group env_file exports AND CLAUDE_CONFIG_DIR both land in the spawn env
	// before exec'ing the wrapper. baseCommand here is the per-session
	// Command field itself (a distinct axis from GetClaudeCommandForInstance,
	// which never sees an instance-level custom command — see its doc
	// comment), so the F3 custom-command gate does not apply: pass false.
	return i.buildEnvSourceCommand() + i.buildBashExportPrefix(false) + baseCommand
}

// buildBashExportPrefix builds the export prefix used in bash -c commands.
// Always exports AGENTDECK_INSTANCE_ID. CLAUDE_CONFIG_DIR is exported only
// when the user has an explicit config_dir resolved for this instance AND
// skipConfigDirForCustomCommand is false; when that gate is open, a
// prepared WorkerScratchConfigDir overrides the resolved value — same
// priority as buildClaudeCommandWithMessage and buildClaudeResumeCommand.
// See the comment there (issue #949) for why the gate is required.
//
// skipConfigDirForCustomCommand (#1822 F3): pass true when the command this
// prefix is about to precede resolves to a custom Claude alias (e.g.
// "cdw"/"cdp" from GetClaudeCommandForInstance) rather than the literal
// "claude" binary — the alias is expected to set CLAUDE_CONFIG_DIR itself
// (that's the point of aliasing), so exporting the deck's own resolved
// value here would fight it. Pass false when the caller always execs the
// literal "claude" binary (e.g. the fork-command builder) or when
// baseCommand is an unrelated instance-level custom command string
// (GetClaudeCommandForInstance never sees those — see its doc comment) —
// in both cases there is no alias downstream to defer to.
func (i *Instance) buildBashExportPrefix(skipConfigDirForCustomCommand bool) string {
	prefix := fmt.Sprintf("export AGENTDECK_INSTANCE_ID=%s; export AGENTDECK_PROFILE=%s; ", shellescape.Quote(i.ID), shellescape.Quote(sessionProfileEnvValue()))
	if IsClaudeConfigDirExplicitForInstance(i) && !skipConfigDirForCustomCommand {
		// Issue #922 (reporter @bautrey): see applyWorkerScratchOverride.
		configDir := i.applyWorkerScratchOverride(GetClaudeConfigDirForInstance(i))
		// configDirShellExpr quotes the resolved config_dir — it lands in the
		// same `bash -c` payload as the quoted AGENTDECK_RESOLVED_* exports
		// below, and a config_dir containing ;/$() would otherwise inject
		// (audit F2) — and, for an --ssh session, expresses a local-home path
		// relative to $HOME so it resolves against the REMOTE user's home
		// instead of a local path that does not exist there (#1858).
		prefix += fmt.Sprintf("export CLAUDE_CONFIG_DIR=%s; ", i.configDirShellExpr(configDir))
	}
	if i.Account != "" && !skipConfigDirForCustomCommand {
		prefix += fmt.Sprintf("export CLAUDE_SECURESTORAGE_CONFIG_DIR=%s; ", i.configDirShellExpr(GetClaudeConfigDirForInstance(i)))
	}
	prefix += i.buildResolvedAccountHintExports()
	prefix += i.promptSuggestionExport()
	return prefix
}

// buildResolvedAccountHintExports emits the three "intended account"
// hint env vars introduced by issue #925 (reporter @bautrey): the
// resolved config dir, group path, and source label from the priority
// chain. These mirror the user's *intent* and intentionally bypass
// the worker-scratch override applied to CLAUDE_CONFIG_DIR — consumer
// scripts (statusline, custom prompts, telemetry, hooks) need a stable
// label of which account this session belongs to, not agent-deck's
// per-session scratch path. Always emitted for claude-compatible
// instances (including when source resolves to "default") so consumers
// can rely on the vars being present.
//
// AGENTDECK_RESOLVED_CONFIG_DIR goes through configDirShellExpr for the same
// reason CLAUDE_CONFIG_DIR does (#1858, review finding F6): the consumers are
// hooks and statusline scripts running on the host the session runs on, so for
// an --ssh session a literal local-home path names a directory that does not
// exist there — and shipping two config-dir vars that disagree, with the
// readable one wrong, is worse than shipping one. The $HOME indirection is
// resolved by the shell that reads it, so a local session is byte-identical.
func (i *Instance) buildResolvedAccountHintExports() string {
	resolved, source := GetClaudeConfigDirSourceForInstance(i)
	return fmt.Sprintf(
		"export AGENTDECK_RESOLVED_CONFIG_DIR=%s; export AGENTDECK_RESOLVED_GROUP=%s; export AGENTDECK_RESOLVED_SOURCE=%s; ",
		i.configDirShellExpr(resolved),
		shellescape.Quote(i.GroupPath),
		shellescape.Quote(source),
	)
}

// sessionProfileEnvValue returns the effective profile name to inject as
// AGENTDECK_PROFILE into a spawned session's environment, alongside
// AGENTDECK_INSTANCE_ID. Without it, a bare `agent-deck` command run *inside* a
// non-default-profile session has no AGENTDECK_PROFILE in its shell, so
// GetEffectiveProfile falls through to "default" — resolving the wrong profile
// and silently orphaning auto-parent routing (resolveAutoParentInstance looks
// up the caller's instance against the wrong profile's session list). The deck
// process is single-profile (one Storage, one state.db), so this must match
// storage.Profile() for every session it manages. It routes through
// ResolveProfileForStorage rather than a bare GetEffectiveProfile("") because
// the host process itself may have hit the #1790 guard (an inferred,
// nonexistent CLAUDE_CONFIG_DIR-derived name) and fallen back to the
// configured default profile: injecting the raw inferred name here would
// re-open that exact hole from inside every pane the deck spawns — the pane
// classifies AGENTDECK_PROFILE as ProfileSourceEnv (explicit), bypasses the
// guard unconditionally, and silently creates/opens a different state.db
// than the host is using. We inject it explicitly at each spawn site
// (rather than relying on shell inheritance) so a child spawned from a
// child carries its own profile and not a stale inherited one.
func sessionProfileEnvValue() string {
	return resolvedProcessProfile()
}

// resolvedProcessProfile returns the profile this process actually opened
// storage under, routing through the same #1790 guard as the host's own
// startup path (ResolveProfileForStorage) rather than a bare
// GetEffectiveProfile(""). Any caller that uses a resolved profile name to
// look up or select per-profile state (spawned-pane env, per-profile Codex
// config, etc.) must go through this, not GetEffectiveProfile("") directly:
// the guard falls the host back to the configured default when
// CLAUDE_CONFIG_DIR infers a profile that doesn't exist, and
// GetEffectiveProfile("") alone recomputes the rejected inferred name from
// the same unchanged environment, bypassing the fallback (#1822 F1, and its
// Codex-tool analogue in getCodexHomeDir/isCodexHomeExplicit below).
func resolvedProcessProfile() string {
	resolved, err := ResolveProfileForStorage("")
	if err != nil {
		sessionLog.Warn("resolve_profile_env_failed", slog.String("error", err.Error()))
		return GetEffectiveProfile("")
	}
	return resolved
}

// interactiveShellEnv returns the NAME=value pairs (raw, unquoted) a shell
// session's interactive shell must start with, or nil when the pane's shell
// is not agent-deck's to set up.
//
// A plain shell session (tool "shell", no command) is the one kind of
// session whose pane process gets no command prefix at all: every other
// tool exports AGENTDECK_INSTANCE_ID, AGENTDECK_PROFILE and the identity
// file (identity_injection.go) in front of its command. `env | grep
// AGENTDECK` in such a session was empty, on a remote and locally alike
// (remote parity walk on g14, 2026-09-18), so a bare `agent-deck` run there
// could not tell which session it was in, and the identity block never
// reached it. These pairs give it the same environment; the identity file is
// written by the agent-deck that spawns the pane, so on a remote it is the
// remote's own. A shell session running a custom command gets its account
// only: its environment otherwise comes from its own command line. A wrapper
// (`--wrapper`, or the tool's) owns the pane and already carries the account
// export inside its `bash -c` (prepareCommand), so it gets nothing here.
func (i *Instance) interactiveShellEnv() []string {
	if i.Tool != "shell" || i.tmuxSession == nil || i.tmuxSession.RunCommandAsInitialProcess || i.hasEffectiveWrapper() {
		return nil
	}
	plainShell := strings.TrimSpace(i.Command) == ""
	var env []string
	if plainShell {
		env = append(env,
			"AGENTDECK_INSTANCE_ID="+i.ID,
			"AGENTDECK_PROFILE="+sessionProfileEnvValue(),
			"AGENTDECK_TOOL="+i.Tool,
			"AGENTDECK_TITLE="+identityOneLine(i.Title))
	}
	if i.Account != "" {
		env = append(env, "AGENTDECK_ACCOUNT="+i.Account)
	}
	if plainShell {
		if _, file, ok := i.ensureIdentityFile(); ok {
			env = append(env, IdentityFileEnv+"="+file)
		}
	}
	return env
}

// setInteractiveShellLaunchEnv hands interactiveShellEnv to tmux so the
// pane's shell is born with it (new-session -e) and nothing is typed into
// the pane (#2817: every new shell visibly ran a long ` export AGENTDECK_*`
// line). It must run right before each tmuxSession.Start, after
// RunCommandAsInitialProcess is decided; it also clears a previous spawn's
// pairs so a restart never reuses stale ones.
func (i *Instance) setInteractiveShellLaunchEnv() {
	if i.tmuxSession == nil {
		return
	}
	i.tmuxSession.LaunchEnv = nil
	if i.shellEnvAtLaunch() {
		i.tmuxSession.LaunchEnv = i.interactiveShellEnv()
	}
}

// shellEnvAtLaunch reports whether the pane's interactive shell can be given
// interactiveShellEnv at launch. Not on a tmux without new-session -e
// (< 3.2), and not for an --ssh session: there the shell the user works in
// is the remote one behind the ssh client, which a local launch environment
// never reaches (ssh forwards no AGENTDECK_* variables).
func (i *Instance) shellEnvAtLaunch() bool {
	return !i.IsSSH() && tmux.SupportsLaunchEnv()
}

// ensureInteractiveShellEnv is the fallback for a shell that
// setInteractiveShellLaunchEnv cannot reach (see shellEnvAtLaunch). A tmux
// option cannot change a running shell's env, so the exports are typed into
// it. Everywhere else it does nothing.
func (i *Instance) ensureInteractiveShellEnv() {
	if i.shellEnvAtLaunch() {
		return
	}
	env := i.interactiveShellEnv()
	if len(env) == 0 {
		return
	}
	exports := make([]string, 0, len(env))
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		exports = append(exports, name+"="+shellescape.Quote(value))
	}
	// The leading space keeps the line out of a history that ignores
	// space-prefixed commands (bash HISTCONTROL=ignorespace, zsh
	// HIST_IGNORE_SPACE); it is agent-deck's line, not the user's.
	if err := i.tmuxSession.SendKeysAndEnter(" export " + strings.Join(exports, " ")); err != nil {
		sessionLog.Warn("set_interactive_shell_env_failed", slog.String("error", err.Error()))
	}
}

// ensureProfileEnv sets AGENTDECK_PROFILE host-side on the instance's tmux
// session so a bare `agent-deck` command run inside the session resolves the
// session's own profile rather than falling back to "default". It is the
// tool-agnostic safety net complementing the inline command-prefix injection in
// the spawn-command builders (which only some tools carry — e.g. gemini/opencode/
// generic respawn rebuild a bare resume command with no AGENTDECK_PROFILE prefix).
// Must run on every spawn/respawn success path, including the Restart()
// respawn-pane branches that return early before reaching the fallback recreate
// path. Best-effort: a failure is logged, not fatal.
func (i *Instance) ensureProfileEnv() {
	if i.tmuxSession == nil {
		return
	}
	if i.Account != "" {
		if err := i.tmuxSession.SetEnvironment("AGENTDECK_ACCOUNT", i.Account); err != nil {
			sessionLog.Warn("set_account_failed", slog.String("error", err.Error()))
		}
	} else if err := i.tmuxSession.UnsetEnvironment("AGENTDECK_ACCOUNT"); err != nil {
		sessionLog.Warn("unset_account_failed", slog.String("error", err.Error()))
	}
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_PROFILE", sessionProfileEnvValue()); err != nil {
		sessionLog.Warn("set_profile_failed", slog.String("error", err.Error()))
	}
}

// ensureClaudeConfigDirEnv sets CLAUDE_CONFIG_DIR host-side on the instance's
// tmux session (#1791), mirroring ensureProfileEnv. Without this, the var was
// only ever applied as an inline command-prefix (CLAUDE_CONFIG_DIR=x claude
// ...) or, on some paths, not applied to the persistent pane env at all — a
// fleet survey found it missing from the pane shell in 14/40 sampled panes.
// A hand-restart inside the pane (Ctrl-C, then `claude`) then falls back to
// the default ~/.claude root: wrong billed account, and (when config roots
// share a `projects/` symlink) a transcript the deck can no longer find to
// resume. Same gate as buildBashExportPrefix/buildClaudeCommandWithMessage:
// only set when the instance has an explicit, resolved config_dir AND the
// resolved Claude command is the literal "claude" binary, not a custom
// alias (#1822 F3) — an alias like "cdw"/"cdp" is expected to set
// CLAUDE_CONFIG_DIR itself, and asserting it host-side here would override
// the alias's own resolution the next time it runs in this pane. A bare
// CLAUDE_CONFIG_DIR="" would otherwise mask the profile-scoped identity of
// panes intentionally left on the ambient ~/.claude root. Must run on every
// spawn/respawn success path alongside ensureProfileEnv. Best-effort: a
// failure is logged, not fatal.
func (i *Instance) ensureClaudeConfigDirEnv() {
	if i.tmuxSession == nil {
		return
	}
	// #1822 F7: any early return past this point means CLAUDE_CONFIG_DIR
	// should NOT be set for this instance right now. Clear a stale value a
	// previous call may have left in the tmux session env (e.g. the tool
	// changed away from Claude, or an account override was removed) so a
	// later respawn doesn't inherit an env stuck pointing at the wrong
	// account — the same class of bug #1791 fixes, just in reverse.
	if !IsClaudeCompatible(i.Tool) {
		i.clearClaudeConfigDirEnv()
		return
	}
	// #1822 F3: a custom command alias handles CLAUDE_CONFIG_DIR itself.
	if GetClaudeCommandForInstance(i) != "claude" {
		i.clearClaudeConfigDirEnv()
		return
	}
	if !IsClaudeConfigDirExplicitForInstance(i) {
		i.clearClaudeConfigDirEnv()
		return
	}
	configDir := i.applyWorkerScratchOverride(GetClaudeConfigDirForInstance(i))
	if configDir == "" {
		i.clearClaudeConfigDirEnv()
		return
	}
	if err := i.tmuxSession.SetEnvironment("CLAUDE_CONFIG_DIR", configDir); err != nil {
		sessionLog.Warn("set_claude_config_dir_failed", slog.String("error", err.Error()))
	}
}

// clearClaudeConfigDirEnv unsets CLAUDE_CONFIG_DIR from the tmux session env.
// Best-effort: a failure (e.g. no prior value, session already gone) is
// logged at debug, not warn — unlike SetEnvironment failing, there is no
// stale-account risk if this is a no-op.
func (i *Instance) clearClaudeConfigDirEnv() {
	if i.tmuxSession == nil {
		return
	}
	if err := i.tmuxSession.UnsetEnvironment("CLAUDE_CONFIG_DIR"); err != nil {
		sessionLog.Debug("unset_claude_config_dir_noop", slog.String("error", err.Error()))
	}
}

// logClaudeConfigResolution emits the CFG-07 observability line documenting
// which priority level resolved CLAUDE_CONFIG_DIR for this session.
// Owns the single CFG-07 slog message literal for this package.
//
// Callers MUST gate on IsClaudeCompatible(i.Tool). The helper does not
// re-gate — keeping the guard at each call site makes the three emission
// sites grep-auditable.
//
// Called from: Start, StartWithMessage, Restart.
// NOT called from: Fork (Fork may trigger a subsequent Start() on the
// forked instance which will log), or from any builder function.
func (i *Instance) logClaudeConfigResolution() {
	resolvedPath, source := GetClaudeConfigDirSourceForInstance(i)
	sessionLog.Info("claude config resolution",
		slog.String("session", i.ID),
		slog.String("group", i.GroupPath),
		slog.String("resolved", resolvedPath),
		slog.String("source", source),
	)
}

// ValidateClaudeExtraArgToken rejects a single --extra-arg token that looks
// like a flag mashed together with its value (issue #1431b). Each --extra-arg
// is shell-quoted as ONE argument, so `--extra-arg "--model opus"` reaches
// claude as the literal single arg '--model opus' (embedded space) — an
// unknown flag that makes claude exit on startup and leaves a dead pane the
// registry still reports as running. A token that starts with '-' AND contains
// whitespace is almost always two tokens the user meant to pass separately;
// surfacing it as an error at spawn time beats the silent tmux death. Clean
// flags ("--model") and clean values ("opus", "be concise") pass.
func ValidateClaudeExtraArgToken(token string) error {
	if strings.HasPrefix(token, "-") && strings.ContainsAny(token, " \t\n\r") {
		return fmt.Errorf(
			"--extra-arg %q looks like a flag and its value combined; pass them as separate --extra-arg tokens (e.g. --extra-arg \"--model\" --extra-arg \"opus\"), or use the first-class --model flag",
			token,
		)
	}
	return nil
}

// extraArgsSupplyModel reports whether the persisted --extra-arg tokens already
// carry a `--model` override. ValidateClaudeExtraArgToken forces flag and value
// into separate tokens, so a user-supplied model appears as a bare "--model"
// (or "--model=..." form) token. When present we must NOT also inject
// [claude].default_model, or the launch command would carry two --model flags.
func extraArgsSupplyModel(extraArgs []string) bool {
	for _, tok := range extraArgs {
		if tok == "--model" || strings.HasPrefix(tok, "--model=") {
			return true
		}
	}
	return false
}

// buildClaudeExtraFlags builds extra command-line flags string from ClaudeOptions
// Also handles instance-level flags like --add-dir for subagent access
func (i *Instance) buildClaudeExtraFlags(opts *ClaudeOptions) string {
	launchName := i.ClaudeLaunchName()
	if opts != nil && ((opts.SessionMode == "continue" && i.recordedClaudeSessionID() == "") || (opts.SessionMode == "resume" && opts.ResumeSessionID == "" && i.recordedClaudeSessionID() == "")) {
		launchName = "" // An interactive/latest selector has no bound target yet.
	}
	return i.buildClaudeExtraFlagsWithName(opts, launchName)
}

func (i *Instance) buildClaudeExtraFlagsWithName(opts *ClaudeOptions, launchName string) string {
	var flags []string

	// Instance-level flags (not from ClaudeOptions)
	// --add-dir: Grant subagent access to parent's project directory (for worktrees, etc.)
	if i.ParentProjectPath != "" {
		// shellescape: directory names may legally contain $()/`/;/space; the
		// path is re-parsed by the inner `bash -c` (see bashCWrap), so quote it
		// like --model below. Audit F1.
		flags = append(flags, "--add-dir "+shellescape.Quote(i.ParentProjectPath))
	}

	// Multi-repo: pass all project paths via --add-dir (deduplicated, excluding cwd)
	if i.MultiRepoEnabled {
		seen := make(map[string]bool)
		if i.ParentProjectPath != "" {
			seen[resolveRealPath(i.ParentProjectPath)] = true // already added above
		}
		seen[resolveRealPath(i.EffectiveWorkingDir())] = true // exclude cwd
		for _, p := range i.AllProjectPaths() {
			real := resolveRealPath(p)
			if seen[real] {
				continue
			}
			seen[real] = true
			flags = append(flags, "--add-dir "+shellescape.Quote(p)) // audit F1
		}
	}

	// Launch model resolution (#1431). An explicit per-session opts.Model
	// wins; otherwise fall back to [claude].default_model. The fallback is the
	// load-bearing fix: a session that persisted ANY other Claude option
	// (skip-permissions / chrome / teammate-mode) has a non-nil ClaudeOptions
	// with an empty Model, which bypasses the NewClaudeOptions default_model
	// fallback in the callers. Without resolving the default here, both spawn
	// and restart dropped --model entirely and the child silently booted on
	// Claude's built-in default (Fable, unavailable account-wide) while the
	// registry still showed it running. Because every start/restart/resume
	// command delegates flag assembly here, this single point keeps them all
	// in lockstep.
	// A user-supplied --model via --extra-arg (the form the
	// ValidateClaudeExtraArgToken error message recommends) is an explicit
	// override that must stand alone: the extra-arg tokens are appended verbatim
	// below, so emitting any resolved --model here too would produce a duplicate
	// --model on the command line. claude is last-wins so it would be harmless,
	// but it is confusing and the operator's intent is unambiguous. Suppress the
	// resolved model entirely (whether it came from opts.Model, the
	// NewClaudeOptions default, or [claude].default_model) when extra-args carry
	// their own.
	if !extraArgsSupplyModel(i.ExtraArgs) {
		launchModel := ""
		if opts != nil {
			launchModel = opts.Model
		}
		// Conductor/group model chain (#8): explicit opts.Model wins, then the
		// per-conductor then per-group [*.claude].model overrides.
		launchModel = i.resolveClaudeLaunchModel(launchModel)
		// Finally fall back to the global [claude].default_model (#1437).
		if launchModel == "" {
			if cfg, _ := LoadUserConfig(); cfg != nil {
				launchModel = cfg.Claude.DefaultModel
			}
		}
		if launchModel != "" {
			flags = append(flags, "--model "+shellescape.Quote(launchModel))
		}
	}

	if opts != nil && strings.TrimSpace(opts.Effort) != "" {
		flags = append(flags, "--effort "+shellescape.Quote(strings.TrimSpace(opts.Effort)))
	}

	// Options-level flags
	if opts != nil {
		if opts.SkipPermissions {
			flags = append(flags, "--dangerously-skip-permissions")
		} else if opts.AutoMode {
			flags = append(flags, "--permission-mode auto")
		} else if opts.AllowSkipPermissions {
			flags = append(flags, "--allow-dangerously-skip-permissions")
		}
		if opts.UseChrome {
			flags = append(flags, "--chrome")
		}
		if opts.UseTeammateMode {
			flags = append(flags, "--teammate-mode tmux")
		}
	}

	// Plugin channels: subscribe the claude session to inbound messages from
	// each listed plugin channel. Persisted on Instance.Channels and refreshed
	// on every Start/Restart/resume because every command-build flows here.
	// Heal first: a conductor whose persisted Channels lost the telegram
	// entry (index wipe, record rebuild) is restored from conductor config
	// so the wiring can't silently disappear (telegram_reliability.go).
	reconcileConductorTelegramChannel(i)
	if len(i.Channels) > 0 {
		flags = append(flags, "--channels "+shellescape.Quote(strings.Join(i.Channels, ","))) // audit F1
	}

	// Pass the exact title as one argument to the same startup process whose
	// account and conversation identity the caller selected.
	if launchName != "" {
		flags = append(flags, "--name "+shellescape.Quote(launchName))
	}

	// Identity injection (identity_injection.go): tell the model which
	// agent-deck session it is and how to use the CLI. Emitted here because
	// every fresh/resume/fork claude spawn assembles its flags in this one
	// place; --append-system-prompt-file leaves claude's own prompt intact.
	if identityFlag := i.claudeIdentityFlag(); identityFlag != "" {
		flags = append(flags, identityFlag)
	}

	// User-supplied extra args: each token is shellescape-quoted before
	// re-emission so values with spaces survive the `bash -c` wrapper
	// without being re-tokenized. Appended last so user flags can override
	// defaults claude accepts in last-wins ordering.
	//
	// KNOWN OPEN RESIDUAL (review finding on #1830, not yet fixed): these
	// tokens are appended AFTER canResumeClaudeSession has already decided
	// --resume vs --session-id above, so an operator-configured ExtraArgs
	// entry of e.g. `--resume <foreign-uuid>` or `-c` still reaches the final
	// command line on a command the chokepoint just refused. #1407 copies a
	// parent's ExtraArgs onto every fork, so a misconfigured parent would
	// propagate the bypass to all children. ExtraArgs is operator-configured
	// (not an external input), which bounds the severity, but this flags is
	// not the "cannot be bypassed" guarantee this file's package doc claims.
	// Stripping/logging --resume/-r/-c/--fork-session/--session-id tokens
	// from ExtraArgs on the claude path is deferred as a follow-up.
	for _, tok := range i.ExtraArgs {
		flags = append(flags, shellescape.Quote(tok))
	}

	if len(flags) == 0 {
		return ""
	}
	return " " + strings.Join(flags, " ")
}

// buildGeminiCommand builds the gemini command with session capture
// For new sessions: captures session ID via stream-json, stores in tmux env, then resumes
// For sessions with known ID: uses simple resume
// This ensures we always know the session ID for restart features
// VERIFIED: gemini --output-format stream-json provides immediate session ID in first message
// Also sources .env files from [shell].env_files and [gemini].env_file
func (i *Instance) buildGeminiCommand(baseCommand string) string {
	if i.Tool != "gemini" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// Determine if YOLO mode is enabled (per-session overrides global config)
	yoloMode := false
	if i.GeminiYoloMode != nil {
		yoloMode = *i.GeminiYoloMode
	} else {
		// Check global config
		userConfig, _ := LoadUserConfig()
		if userConfig != nil {
			yoloMode = userConfig.Gemini.YoloMode
		}
	}

	yoloFlag := ""
	if yoloMode {
		yoloFlag = " --yolo"
	}

	// Determine model flag
	modelFlag := ""
	if i.GeminiModel != "" {
		modelFlag = " --model " + shellescape.Quote(i.GeminiModel)
	} else if i.GeminiSessionID == "" {
		// Only apply default model for NEW sessions (not resumes)
		userConfig, _ := LoadUserConfig()
		if userConfig != nil && userConfig.Gemini.DefaultModel != "" {
			modelFlag = " --model " + shellescape.Quote(userConfig.Gemini.DefaultModel)
		}
	}

	// If baseCommand is just "gemini", handle specially
	if baseCommand == "gemini" {
		cmd := GetToolCommand("gemini")
		// Identity injection: the per-session identity dir holds GEMINI.md,
		// which --include-directories loads as project memory
		// (identity_injection.go). Same on fresh and resume spawns. When the
		// identity root is not a gemini-trusted folder the flag is withheld
		// (it would raise a dialog that eats the initial message) and the
		// pane shows the rule to add instead.
		identityFlag, identityHint := i.geminiIdentityFlag()
		if identityHint != "" {
			envPrefix += paneWarning(identityHint) + " && "
		}
		// If we already have a session ID, use simple resume
		if i.GeminiSessionID != "" {
			// GEMINI_YOLO_MODE and GEMINI_SESSION_ID are propagated via host-side
			// SetEnvironment after tmux start. No inline tmux set-environment.
			return envPrefix + fmt.Sprintf(
				"%s --resume %s%s%s%s",
				cmd,
				shellescape.Quote(i.GeminiSessionID),
				yoloFlag,
				modelFlag,
				identityFlag,
			)
		}

		// Start Gemini fresh - session ID will be captured when user interacts
		// The previous capture-resume approach (gemini --output-format json ".") would hang
		// because Gemini processes the "." prompt which takes too long
		// GEMINI_YOLO_MODE is propagated via host-side SetEnvironment after tmux start.
		return envPrefix + fmt.Sprintf(
			`%s%s%s%s`,
			cmd,
			yoloFlag,
			modelFlag,
			identityFlag,
		)
	}

	// For custom commands (e.g., resume commands), return as-is
	return envPrefix + baseCommand
}

// buildOpenCodeCommand builds the command for OpenCode CLI
// OpenCode stores sessions in ~/.local/share/opencode/storage/session/
// Session IDs are in format: ses_XXXXX
// Resume: opencode -s <session-id> or opencode --session <session-id>
// Continue last: opencode -c or opencode --continue
// Model: opencode -m provider/model
// Agent: opencode --agent name
// Also sources .env files from [shell].env_files
func (i *Instance) buildOpenCodeCommand(baseCommand string) string {
	if i.Tool != "opencode" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// Resume through the stored launcher, including the explicit opencode2 alias.
	if baseCommand == "opencode" {
		baseCommand = i.openCodePersistentCommand()
	}
	if cmd, ok := i.openCodeLauncher(baseCommand); ok {
		var extraFlags string
		if i.openCodeRejectsV1LaunchFlags() {
			// V2 uses the shared service instead of a per-TUI --port server.
			i.setOpenCodePort(0)
			return envPrefix + i.buildOpenCodeV2Command(cmd)
		} else {
			extraFlags = i.buildOpenCodeExtraFlags() + i.buildOpenCodeSSEPortFlag()
		}

		// If we already have a session ID, use resume with -s flag.
		// OPENCODE_SESSION_ID is propagated via host-side SetEnvironment after tmux start.
		if i.OpenCodeSessionID != "" {
			return envPrefix + fmt.Sprintf("%s -s %s%s",
				cmd, shellescape.Quote(i.OpenCodeSessionID), extraFlags)
		}

		// Start OpenCode fresh - session ID will be captured async after startup
		return envPrefix + cmd + extraFlags
	}

	// For custom commands (e.g., fork commands), return as-is. No --port is
	// injected, so clear any stale port from a prior launch — otherwise the
	// SSE watcher could connect to a freed port later reused by an unrelated
	// process (issue #1614). Status falls back to tmux content sniffing.
	i.setOpenCodePort(0)
	return envPrefix + baseCommand
}

// buildOpenCodeExtraFlags returns extra CLI flags from OpenCodeOptions (model, agent).
// Returns a string with leading space, or empty string if no flags.
func (i *Instance) buildOpenCodeExtraFlags() string {
	opts := i.GetOpenCodeOptions()
	if opts == nil {
		// Fall back to global config defaults
		if config, err := LoadUserConfig(); err == nil && config != nil {
			opts = NewOpenCodeOptions(config)
		}
	}
	if opts == nil {
		return ""
	}

	var flags string
	if opts.Model != "" {
		flags += " -m " + shellescape.Quote(opts.Model)
	}
	if opts.Agent != "" {
		flags += " --agent " + shellescape.Quote(opts.Agent)
	}
	return flags
}

// buildOpenCodeSSEPortFlag allocates a localhost port for OpenCode's event
// server and returns " --port N" (issue #1614). With an explicit --port, the
// OpenCode TUI binds a real HTTP server whose /event SSE stream publishes
// session.status transitions; OpenCodeSSEWatcher consumes it for real-time
// status instead of tmux content sniffing. Returns "" (and clears the stored
// port) when disabled via [opencode].disable_sse_status or when no port could
// be allocated — status detection then falls back to tmux polling.
func (i *Instance) buildOpenCodeSSEPortFlag() string {
	if config, err := LoadUserConfig(); err == nil && config != nil && config.OpenCode.DisableSSEStatus {
		i.setOpenCodePort(0)
		return ""
	}
	port, err := allocateLocalPort()
	if err != nil {
		sessionLog.Warn("opencode_sse_port_alloc_failed", slog.String("error", err.Error()))
		i.setOpenCodePort(0)
		return ""
	}
	i.setOpenCodePort(port)
	return fmt.Sprintf(" --port %d", port)
}

// allocateLocalPort reserves a free 127.0.0.1 port by binding :0 and
// immediately releasing it. The tiny window before OpenCode re-binds it is
// the standard trade-off; a collision surfaces as a failed launch and the
// next restart picks a fresh port.
func allocateLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// buildCodexCommand builds the command for OpenAI Codex CLI
// resolveCodexYoloFlag returns " --yolo" if yolo mode is enabled (per-session override > global config), or "".
func (i *Instance) resolveCodexYoloFlag() string {
	opts := i.GetCodexOptions()
	if opts != nil && opts.YoloMode != nil {
		if *opts.YoloMode {
			return " --yolo"
		}
		return ""
	}
	// Fallback to global config
	if config, err := LoadUserConfig(); err == nil && config != nil {
		if config.Codex.YoloMode {
			return " --yolo"
		}
	}
	return ""
}

func (i *Instance) resolveCodexModelFlag() string {
	opts := i.GetCodexOptions()
	if opts != nil && strings.TrimSpace(opts.Model) != "" {
		return " --model " + shellescape.Quote(strings.TrimSpace(opts.Model))
	}
	return ""
}

func (i *Instance) resolveCodexReasoningEffortFlag() string {
	opts := i.GetCodexOptions()
	if opts != nil && strings.TrimSpace(opts.ReasoningEffort) != "" {
		value := "model_reasoning_effort=" + strings.TrimSpace(opts.ReasoningEffort)
		return " --config " + shellescape.Quote(value)
	}
	return ""
}

func (i *Instance) resolveCodexCommand(baseCommand string) string {
	command := strings.TrimSpace(baseCommand)
	if i.Tool == "codex" && (command == "" || command == "codex") {
		return GetCodexCommand()
	}
	if command == "" {
		return "codex"
	}
	return command
}

func codexHomeFromCommand(command string) string {
	rest := strings.TrimSpace(command)
	for rest != "" {
		token, remainder, ok := nextShellWord(rest)
		if !ok {
			return ""
		}
		if !isShellEnvAssignment(token) {
			return ""
		}
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			return ""
		}
		if key == "CODEX_HOME" && strings.TrimSpace(value) != "" {
			return ExpandPath(strings.TrimSpace(value))
		}
		rest = strings.TrimLeft(remainder, " \t\r\n")
	}
	return ""
}

func nextShellWord(s string) (word string, remainder string, ok bool) {
	s = strings.TrimLeft(s, " \t\r\n")
	if s == "" {
		return "", "", false
	}

	var b strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote == 0 {
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				return b.String(), s[i:], true
			}
			switch c {
			case '\'', '"':
				quote = c
			case '\\':
				if i+1 < len(s) {
					i++
					b.WriteByte(s[i])
				} else {
					b.WriteByte(c)
				}
			default:
				b.WriteByte(c)
			}
			continue
		}

		if c == quote {
			quote = 0
			continue
		}
		if quote == '"' && c == '\\' && i+1 < len(s) {
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(c)
	}
	if quote != 0 {
		return "", "", false
	}
	return b.String(), "", true
}

// accountCodexHomeDir returns the CODEX_HOME mapped to this session's account
// slot via [profiles.<account>.codex].config_dir, or "" when the session has no
// account or the account names no codex block.
//
// Mirrors the account level of resolveClaudeConfigDir (#924): the per-session
// account is the most specific thing the user can say about which credentials a
// session runs under, so it outranks the process-wide CODEX_HOME and the
// profile/global config. An account with no matching block falls through
// silently, matching the permissive style of the Claude chain.
func (i *Instance) accountCodexHomeDir() string {
	if i == nil || strings.TrimSpace(i.Account) == "" {
		return ""
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.GetProfileCodexConfigDir(i.Account)
}

// codexHomeToExport returns the CODEX_HOME this session must launch with, or ""
// when nothing should be exported and codex may use its own default.
//
// Both codex launch paths (normal and fork) call this so they cannot drift: a
// session that resolves its home one way and launches another is exactly the
// #1922 failure — the account was recorded and reported, but the process ran
// against ~/.codex.
func (i *Instance) codexHomeToExport() string {
	if accountHome := strings.TrimSpace(i.accountCodexHomeDir()); accountHome != "" {
		return accountHome
	}
	if isCodexHomeExplicit() {
		return strings.TrimSpace(getCodexHomeDir())
	}
	return ""
}

// codexHomeForCommand returns the CODEX_HOME the launched process will actually
// read and write under, for an already-resolved codex command line.
//
// This is the read-side mirror of codexHomeToExport and must stay in step with
// it: whatever that exports, this resolves to. codexHomeToExport returns "" when
// nothing needs exporting, in which case the process inherits the ambient
// CODEX_HOME or falls back to ~/.codex — exactly what getCodexHomeDir resolves
// to in that case.
//
// A resolver that disagrees with the launch is #1929: buildCodexCommand's
// resume gate looked for the rollout in the DEFAULT home while the launch wrote
// it under the ACCOUNT home, so the gate always missed, dropped the session id,
// and every restart silently opened a fresh conversation.
func (i *Instance) codexHomeForCommand(command string) string {
	// A CODEX_HOME= embedded in the command is the user typing it by hand for
	// this session, so it stays the most explicit signal of all — and the shell
	// agrees, since it is emitted after any exported CODEX_HOME.
	if codexHome := codexHomeFromCommand(command); codexHome != "" {
		return codexHome
	}
	if accountHome := strings.TrimSpace(i.accountCodexHomeDir()); accountHome != "" {
		return accountHome
	}
	return getCodexHomeDir()
}

func (i *Instance) getCodexHomeDir() string {
	if i == nil {
		return getCodexHomeDir()
	}
	return i.codexHomeForCommand(i.resolveCodexCommand(i.Command))
}

// ResolvedCodexHome exposes the local rollout root for structured consumers.
// Remote SSH sessions intentionally return empty: their path is meaningful on
// the remote host and must not be mistaken for a readable local directory.
func (i *Instance) ResolvedCodexHome() string {
	if i == nil || !IsCodexCompatible(i.Tool) || strings.TrimSpace(i.SSHHost) != "" {
		return ""
	}
	return i.getCodexHomeDir()
}

// Codex stores sessions in ~/.codex/sessions/YYYY/MM/DD/*.jsonl
// Resume: codex resume <session-id> or codex resume --last
// Also sources .env files from [shell].env_files
func (i *Instance) buildCodexCommand(baseCommand string) string {
	if !IsCodexCompatible(i.Tool) {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// AGENTDECK_* env injection is required for the hook subprocesses spawned
	// by tools in the codex family to find this session's state, so it is
	// injected BEFORE the custom-command passthrough early-return below.
	// Dropping it on custom-command sessions was the design regression flagged
	// on #951 review — keep AGENTDECK_* on every codex-flavoured launch.
	// AGENTDECK_TITLE is shell-quoted, not Go-%q-quoted: %q wraps the title
	// in double quotes, inside which the shell still runs backticks and
	// $(...) (a title of `x` printed "bash: x: command not found" at every
	// spawn and lost the backticks). Same quoting as the passthrough branch.
	agentdeckEnvPrefix := fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%s AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ",
		shellescape.Quote(i.ID), shellescape.Quote(i.Title), i.Tool, shellescape.Quote(sessionProfileEnvValue()))
	envPrefix += agentdeckEnvPrefix

	// Passthrough: if the tool is literally "codex" and user gave a custom command
	// (not the bare "codex" name), return as-is without flag injection.
	// Codex-compatible tools (e.g., "my-codex" with CompatibleWith="codex") always
	// get the full treatment regardless of their command name.
	trimmed := strings.TrimSpace(baseCommand)
	if i.Tool == "codex" && trimmed != "codex" && trimmed != "" {
		return envPrefix + trimmed
	}
	if codexHome := i.codexHomeToExport(); codexHome != "" {
		if err := os.MkdirAll(codexHome, 0o755); err != nil {
			sessionLog.Warn("codex_home_mkdir_failed",
				slog.String("path", codexHome),
				slog.String("error", err.Error()))
		}
		envPrefix += "CODEX_HOME=" + shellescape.Quote(codexHome) + " "
	}

	yoloFlag := i.resolveCodexYoloFlag()
	modelFlag := i.resolveCodexModelFlag()
	reasoningFlag := i.resolveCodexReasoningEffortFlag()
	command := i.resolveCodexCommand(baseCommand)
	// Must be the home this launch exports (above), not the process-wide one:
	// the gates below decide whether a rollout exists, and looking in the wrong
	// home destroys a live binding (#1929).
	codexHome := i.codexHomeForCommand(command)
	// Identity injection: `-c developer_instructions=...` appends the session
	// block to codex's developer message (identity_injection.go), merged with
	// any developer_instructions the operator configured in this launch's
	// CODEX_HOME so the override never replaces their guidance. Carried on
	// fresh, resume and fork spawns alike since a -c override never persists.
	identityFlag := i.codexIdentityFlag(codexHome, i.EffectiveWorkingDir())

	// Issue #756: Gate `codex resume <sid>` on rollout-file existence.
	// If Codex died before flushing its rollout JSONL (tmux crash, kill -9
	// in the SessionStart→first-flush window), the captured session_id is
	// permanently unresumable. Without this check the bridge appends
	// `resume <stale-uuid>` on every restart and Codex exits immediately,
	// flipping the session back to error in an infinite loop. Drop the
	// stale ID, clear the .sid sidecar so the next hook tick rebinds
	// cleanly, and spawn fresh.
	if i.CodexSessionID != "" && !codexRolloutExistsInHome(i.CodexSessionID, codexHome) {
		sessionLog.Warn("codex_resume_stale_sid_dropped",
			slog.String("instance_id", i.ID),
			slog.String("title", i.Title),
			slog.String("sid", i.CodexSessionID),
			slog.String("codex_home", codexHome))
		i.CodexSessionID = ""
		i.CodexDetectedAt = time.Time{}
		ClearHookSessionAnchor(i.ID)
	}

	// Safety net (incident 2026-07-15): codex loads non-user child threads
	// via `resume` but refuses user-initiated turns on them — the TUI exits
	// status 1 with "turn/start failed in TUI" on the first typed message,
	// killing the tmux session in an error loop. Guardian review bindings
	// resume their verified user ancestor. Other child bindings use `codex fork`
	// to create a thread_source=user thread that accepts input. The live-process
	// probe then rebinds the instance to the fork's new id. See
	// codex_subagent_gate.go.
	if i.CodexSessionID != "" && codexSessionNeedsFork(i.CodexSessionID, codexHome) {
		if parentID := codexGuardianParentThreadID(i.CodexSessionID, codexHome); parentID != "" {
			oldID := i.CodexSessionID
			if err := i.bindCodexSessionFromHook(parentID, "guardian_parent_recovery"); err != nil {
				return envPrefix + fmt.Sprintf("%s%s%s%s%s fork %s",
					command, yoloFlag, modelFlag, reasoningFlag, identityFlag, shellescape.Quote(oldID))
			}
			_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
				InstanceID: i.ID, Tool: i.Tool, Action: "rebind",
				Source: "guardian_parent_recovery", OldID: oldID, NewID: parentID,
			})
		} else {
			sessionLog.Warn("codex_subagent_binding_forked",
				slog.String("instance_id", i.ID),
				slog.String("title", i.Title),
				slog.String("sid", i.CodexSessionID))
			return envPrefix + fmt.Sprintf("%s%s%s%s%s fork %s",
				command, yoloFlag, modelFlag, reasoningFlag, identityFlag, shellescape.Quote(i.CodexSessionID))
		}
	}

	if i.CodexSessionID != "" {
		return envPrefix + fmt.Sprintf("%s%s%s%s%s resume %s",
			command, yoloFlag, modelFlag, reasoningFlag, identityFlag, shellescape.Quote(i.CodexSessionID))
	}

	return envPrefix + command + yoloFlag + modelFlag + reasoningFlag + identityFlag
}

// buildCodexCommandWithPrompt builds the Codex launch command with an initial
// prompt delivered as Codex's own positional [PROMPT] argument, mirroring the
// claude-code startup query (#725). It reports whether the prompt was embedded;
// when it was not, the caller must fall back to the post-start typing path.
//
// Typing a large initial prompt into a live Codex TUI is unreliable: the message
// is sent as one literal `tmux send-keys -l` burst followed immediately by Enter,
// and Codex reads a large fast burst as a paste, swallowing the trailing Enter
// into it. The prompt then sits unsubmitted in the composer and the agent never
// starts. `codex [OPTIONS] [PROMPT]` starts the session with the prompt already
// in hand, so nothing is typed and there is no Enter to lose.
//
// The prompt is only embedded on the plain fresh-start path. It is NOT embedded
// when:
//   - the prompt is empty (nothing to deliver);
//   - the user supplied a custom command (buildCodexCommand passes it through
//     verbatim, and an arbitrary wrapper need not accept a positional prompt);
//   - the session resumes (`codex ... resume <sid>`), where a trailing operand
//     would not be the subcommand's prompt.
//
// In each of those cases the caller keeps the existing behaviour unchanged.
// redactEmbeddedSpawnPrompt removes a prompt that StartWithMessage deliberately
// placed in a launch command. The process still receives the original command;
// only the failure diagnostic uses this copy, so prompt delivery semantics stay
// unchanged. Both current positional-prompt builders append shellescape.Quote.
func redactEmbeddedSpawnPrompt(command, prompt string) string {
	for _, candidate := range []string{prompt, strings.TrimSpace(prompt)} {
		if candidate == "" {
			continue
		}
		quoted := shellescape.Quote(candidate)
		if strings.HasSuffix(command, " "+quoted) {
			return strings.TrimSuffix(command, " "+quoted) + " '[prompt redacted]'"
		}
	}
	return command
}

func (i *Instance) buildCodexCommandWithPrompt(baseCommand, prompt string) (string, bool) {
	command := i.buildCodexCommand(baseCommand)
	if strings.TrimSpace(prompt) == "" {
		return command, false
	}
	if !IsCodexCompatible(i.Tool) {
		return command, false
	}
	// Custom-command passthrough: buildCodexCommand returned the user's command
	// as-is, so we cannot assume it takes a positional prompt.
	trimmed := strings.TrimSpace(baseCommand)
	if i.Tool == "codex" && trimmed != "codex" && trimmed != "" {
		return command, false
	}
	// Resume path appends `resume <sid>`; leave it to the typing path.
	if i.CodexSessionID != "" {
		return command, false
	}
	return command + " " + shellescape.Quote(prompt), true
}

// piAgentDeckSessionDirExpr returns a target-shell expression for the Pi session
// directory Agent Deck owns for an instance. It intentionally uses target-side
// $HOME rather than resolving the Agent Deck process' home directory, keeping
// local, SSH, and sandbox launch paths consistent.
func piAgentDeckSessionDirExpr(instanceID string) string {
	return "${HOME}/.pi/agent-deck/" + shellescape.Quote(instanceID)
}

// buildPiCommand builds the command for the Pi CLI.
// Pi sessions are JSONL files, not externally named sessions like Claude/Codex.
// Scope Pi's session directory to the Agent Deck instance and always launch
// with --continue so restarts resume that instance without colliding with other
// Agent Deck Pi sessions in the same project.
func (i *Instance) buildPiCommand(baseCommand string) string {
	if i.Tool != "pi" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()
	cmd := strings.TrimSpace(baseCommand)
	if cmd == "" {
		cmd = "pi"
	}

	sessionDir := piAgentDeckSessionDirExpr(i.ID)
	quotedInstanceID := shellescape.Quote(i.ID)
	quotedProfile := shellescape.Quote(sessionProfileEnvValue())

	return envPrefix + fmt.Sprintf(
		"session_dir=%s; mkdir -p \"$session_dir\" && AGENTDECK_INSTANCE_ID=%s AGENTDECK_PROFILE=%s %s --continue --session-dir \"$session_dir\"%s",
		sessionDir,
		quotedInstanceID,
		quotedProfile,
		cmd,
		i.piIdentityFlag(),
	)
}

func (i *Instance) consumeForkStartCommand() string {
	command := i.Command
	if i.ForkStartCommand != "" {
		command = i.ForkStartCommand
		i.ForkStartCommand = ""
	}
	i.IsForkAwaitingStart = false
	return command
}

// cursorTrustWorkspacePath returns the workspace path Cursor should trust for
// this instance: container /workspace for sandboxes, SSHRemotePath for remote
// sessions, otherwise the effective local working directory.
func (i *Instance) cursorTrustWorkspacePath() string {
	if i.IsSandboxed() {
		return cursorSandboxWorkDir
	}
	if i.IsSSH() && i.SSHRemotePath != "" {
		return i.SSHRemotePath
	}
	return i.EffectiveWorkingDir()
}

// preAcceptCursorWorkspaceTrust seeds Cursor workspace trust for the session's
// workspace so interactive launches skip the trust prompt. Local, SSH, and
// sandbox sessions each write trust where Cursor will read it. Failures are
// logged and non-fatal.
func (i *Instance) preAcceptCursorWorkspaceTrust() {
	if i.Tool != "cursor" {
		return
	}
	dir := i.cursorTrustWorkspacePath()
	if dir == "" {
		return
	}
	var err error
	switch {
	case i.IsSandboxed() && i.SandboxContainer != "":
		err = PreAcceptCursorTrustInContainer(i.SandboxContainer, dir)
	case i.IsSSH():
		err = PreAcceptCursorTrustSSH(i.SSHHost, dir)
	default:
		err = PreAcceptCursorTrust(GetCursorConfigDir(), dir)
	}
	if err != nil {
		sessionLog.Warn("cursor_preaccept_trust_failed",
			slog.String("instance_id", i.ID),
			slog.String("dir", dir),
			slog.String("error", err.Error()))
	}
}

// buildCursorCommand builds the command for the Cursor Agent CLI.
// continuePrev adds --continue so Restart resumes the previous chat in the workspace.
// Env files from [shell].env_files are applied via buildEnvSourceCommand.
//
// Default entrypoint resolution prefers the standalone `agent` binary when it is
// on PATH, falling back to `cursor agent`. See DefaultCursorCommand.
func (i *Instance) buildCursorCommand(baseCommand string, continuePrev bool) string {
	if i.Tool != "cursor" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()
	cmd := strings.TrimSpace(baseCommand)
	if isDefaultCursorInvocation(cmd) {
		cmd = GetToolCommand("cursor")
	}

	out := envPrefix + cmd
	if continuePrev && !strings.Contains(strings.ToLower(cmd), "--continue") {
		out += " --continue"
	}
	return out
}

// buildCopilotCommand builds the command for GitHub Copilot CLI.
// If baseCommand is the bare "copilot" name, applies config command override + env prefix.
// Otherwise returns the custom command as-is with env prefix (passthrough).
func (i *Instance) buildCopilotCommand(baseCommand string) string {
	if i.Tool != "copilot" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	if baseCommand != "copilot" {
		return envPrefix + baseCommand
	}

	return envPrefix + GetToolCommand("copilot")
}

// buildClaudeResumeCommand builds the claude resume command with proper config options
// Respects: CLAUDE_CONFIG_DIR, dangerous_mode, and [shell].env_files + init_script
// CLAUDE_SESSION_ID is set via host-side SetEnvironment (called by SyncSessionIDsToTmux after restart)
func (i *Instance) buildClaudeResumeCommand() string {
	// Source env files and init_script so resumed sessions have the same
	// shell environment as freshly started ones (fixes #409).
	envPrefix := i.buildEnvSourceCommand()

	// Get the configured Claude command (e.g., "claude", "cdw", "cdp"),
	// resolved per instance: conductor > group (ancestor-walk) > global.
	// A per-session custom command (i.Command, e.g. from `launch -c
	// claude-dbx`) takes precedence — mirroring buildClaudeCommandWithMessage,
	// where the per-session Command field is a distinct axis that
	// GetClaudeCommandForInstance never sees (see its doc comment). Without
	// this, resume falls back to the literal "claude" binary and bypasses
	// the wrapper script that sets up gateway env vars.
	//
	// The --fork-session / --session-id exclusions reuse the same predicate
	// adoptExplicitClaudeSessionID uses for the same discrimination: a fork
	// target's Command holds the whole compound fork command (cd '<path>' &&
	// ... exec claude --session-id "<uuid>" --resume <parent-id> --fork-session)
	// baked by buildClaudeForkCommandForTarget and persisted to disk. The
	// IsForkAwaitingStart sentinel that routes the first Start() around this
	// function is transient (json:"-"), so it is gone after the first start —
	// a subsequent Restart() reaches here with that compound string in Command.
	// Treating it as a custom command splices it in as the claude binary:
	// --fork-session and the parent's id come back (double-counting the parent
	// conversation) and the pane dies with `exec: cd: not found` (exit 127)
	// because the string starts with `exec cd`. Same shape as the #1830
	// finding, one path over.
	claudeCmd := GetClaudeCommandForInstance(i)
	customCommand := i.Command != "" && i.Command != "claude" &&
		!commandHasToken(i.Command, "--fork-session") &&
		!commandHasToken(i.Command, "--session-id")
	if customCommand {
		claudeCmd = i.Command
	}

	// #1791/#1822 F5: use the shared `export ...;` prefix, not a bare
	// `KEY=val cmd` prefix — see the identical comment in
	// buildClaudeCommandWithMessage for why (exit_to_shell's trailing
	// `exec "$SHELL" -i` only inherits exported vars, not a single-command
	// prefix scoped to the claude invocation alone). #1822 F3: skip the
	// CLAUDE_CONFIG_DIR export when claudeCmd resolves to a config-level
	// alias — the alias handles it itself. A per-session custom command is
	// the same axis as the fresh-start custom-command path, so the F3 gate
	// does not apply there (pass false), matching buildClaudeCommandWithMessage.
	bashExportPrefix := i.buildBashExportPrefix(!customCommand && claudeCmd != "claude")

	// Get per-session permission settings (falls back to config if not persisted)
	opts := i.GetClaudeOptions()
	if opts == nil {
		userConfig, _ := LoadUserConfig()
		opts = NewClaudeOptions(userConfig)
	}

	// Check if session has actual conversation data.
	// If not, use --session-id instead of --resume to avoid "No conversation found" error.
	//
	// Issue #662: a bounded retry-once at this call site covers the
	// SessionEnd-flush race — the helper is called synchronously with
	// restart, and Claude may still be flushing its jsonl for a few
	// hundred milliseconds after the SessionEnd hook fires. Waiting 200ms
	// and re-checking turns a shipped-fresh-session into a resume for the
	// common flush-race case without slowing the happy path (retry only
	// fires when the first check comes back negative AND we have a
	// non-empty ClaudeSessionID).
	// #2683: an agent-view handoff ends the bound transcript with a
	// continued-in record; resume the successor, not the stalled copy.
	i.followClaudeContinuedIn()

	// #1815 identity guard: refuse outright when the id about to be resumed
	// is not this session's own recorded conversation id (missing recorded id,
	// or a mismatch). The refused id is replaced by a freshly minted one so
	// the session starts clean instead of claiming another session's id.
	if decision := i.resumeIdentityAllowed(i.ClaudeSessionID); !decision.Allow {
		refused := i.ClaudeSessionID
		i.logResumeRefusal(refused, decision.Reason)
		fresh := i.replaceRefusedClaudeSessionID()
		extraFlags := i.buildClaudeExtraFlags(opts)
		// `fresh` is minted here so it is trusted, but `refused` and the path
		// are session-supplied; sanitize both (CodeQL go/log-injection).
		sessionLog.Info("resume: id="+fresh+" reason=identity_guard_fresh_session",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("claude_session_id", fresh),
			slog.String("refused_session_id", logging.SanitizeValue(refused)),
			slog.String("path", logging.SanitizeValue(i.ProjectPath)),
			slog.String("reason", "identity_guard_fresh_session"))
		return fmt.Sprintf("%s%s%s --session-id %s%s",
			envPrefix, bashExportPrefix, claudeCmd, fresh, extraFlags)
	}

	// #1815 / CodeQL go/log-injection: i.ClaudeSessionID, i.ID and
	// i.ProjectPath are session-supplied, so every log call in this function
	// — including the two Debug sibling calls below, not only the final Info
	// record — sanitizes them. Computed once, before first use.
	safeSID := logging.SanitizeValue(i.ClaudeSessionID)
	safeInstID := logging.SanitizeValue(i.ID)
	safePath := logging.SanitizeValue(i.ProjectPath)

	useResume := canResumeClaudeSession(i, i.ClaudeSessionID)
	if !useResume && i.ClaudeSessionID != "" && i.TranscriptIsResolvableLocally() {
		time.Sleep(resumeCheckRetryDelay)
		useResume = canResumeClaudeSession(i, i.ClaudeSessionID)
		sessionLog.Debug(
			"session_data_retry_after_wait",
			slog.String("session_id", safeSID),
			slog.Duration("wait", resumeCheckRetryDelay),
			slog.Bool("use_resume_after_retry", useResume),
		)
	}
	sessionLog.Debug(
		"session_data_build_resume",
		slog.String("session_id", safeSID),
		slog.String("path", safePath),
		slog.Bool("use_resume", useResume),
	)

	// OBS-02: per-call grep-stable Info record. One emission per
	// buildClaudeResumeCommand call — NOT sync.Once'd. See CONTEXT Decision 2.
	// Every Start / StartWithMessage / Restart dispatch that routes through
	// this helper produces exactly one "resume: id=<id> reason=<why>" line.
	if useResume {
		sessionLog.Info("resume: id="+safeSID+" reason=conversation_data_present",
			slog.String("instance_id", safeInstID),
			slog.String("claude_session_id", safeSID),
			slog.String("path", safePath),
			slog.String("reason", "conversation_data_present"))
	} else {
		sessionLog.Info("resume: id="+safeSID+" reason=session_id_flag_no_jsonl",
			slog.String("instance_id", safeInstID),
			slog.String("claude_session_id", safeSID),
			slog.String("path", safePath),
			slog.String("reason", "session_id_flag_no_jsonl"))
	}

	// Delegate flag assembly to buildClaudeExtraFlags so restart stays in
	// lockstep with the start path. Handles permission modes (dangerous /
	// auto / allow), --add-dir for parented sessions, and --channels for
	// plugin channel subscriptions. Without this, any flag added to
	// buildClaudeExtraFlags silently disappears on session restart — the
	// phase-5 loopback regression (TestResumeCommandAppendsChannels).
	extraFlags := i.buildClaudeExtraFlags(opts)

	// CLAUDE_SESSION_ID is propagated via host-side SetEnvironment (SyncSessionIDsToTmux)
	// after the tmux session is restarted. No inline tmux set-environment in the shell string
	// (which silently fails inside Docker sandbox containers).
	if useResume {
		return fmt.Sprintf("%s%sexec %s --resume %s%s",
			envPrefix, bashExportPrefix, claudeCmd, shellescape.Quote(i.ClaudeSessionID), extraFlags)
	}
	// Session was never interacted with - use --session-id to create fresh session.
	return fmt.Sprintf("%s%sexec %s --session-id %s%s",
		envPrefix, bashExportPrefix, claudeCmd, shellescape.Quote(i.ClaudeSessionID), extraFlags)
}
