package session

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// buildGenericCommand builds commands for custom tools defined in [tools.*]
// If the tool has session resume config, builds capture-resume command similar to Claude/Gemini
// Otherwise returns the base command as-is
// Also sources .env files from [shell].env_files and [tools.X].env_file
//
// Config fields used:
//   - resume_flag: CLI flag to resume (e.g., "--resume")
//   - session_id_env: tmux env var name (e.g., "VIBE_SESSION_ID")
//   - session_id_json_path: jq path to extract ID (e.g., ".session_id")
//   - output_format_flag: flag to get JSON output (e.g., "--output-format json")
//   - dangerous_flag: flag to skip confirmations (e.g., "--auto-approve")
//   - dangerous_mode: whether to enable dangerous flag by default
//   - env_file: .env file to source for this tool
func (i *Instance) buildGenericCommand(baseCommand string) string {
	envPrefix := i.buildEnvSourceCommand()

	toolDef := GetToolDef(i.Tool)
	if toolDef == nil {
		return envPrefix + baseCommand // No custom config, return with env prefix
	}

	// Resume needs resume_flag; the conversation id may come from live
	// session_id_env and/or persisted generic_session_id (reboot-safe).
	if toolDef.ResumeFlag == "" {
		// No session resume support, just add dangerous flag if configured
		if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
			return envPrefix + fmt.Sprintf("%s %s", baseCommand, toolDef.DangerousFlag)
		}
		return envPrefix + baseCommand
	}

	// Prefer live tmux env, else persisted generic_session_id (reboot-safe).
	existingSessionID := i.GetGenericSessionID()

	// Build dangerous flag if enabled
	dangerousFlag := ""
	if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
		dangerousFlag = toolDef.DangerousFlag
	}

	// If we have an existing session ID, just resume.
	// The session ID env var is propagated via host-side SetEnvironment after tmux start.
	if existingSessionID != "" {
		return envPrefix + formatGenericResumeCommand(
			baseCommand, toolDef.ResumeFlag, existingSessionID, dangerousFlag)
	}

	// No existing session ID - need to capture it on first run
	// This requires output_format_flag and session_id_json_path
	if toolDef.OutputFormatFlag == "" || toolDef.SessionIDJsonPath == "" {
		// Can't capture session ID, just start normally
		if dangerousFlag != "" {
			return envPrefix + baseCommand + " " + dangerousFlag
		}
		return envPrefix + baseCommand
	}

	// Build capture-resume command similar to Claude/Gemini
	// Pattern:
	// 1. Run tool with minimal prompt to get session ID
	// 2. Extract ID using jq
	// 3. Resume that session
	// Note: session ID env var is set via host-side SyncSessionIDsToTmux() once detected.
	// Fallback: If capture fails, start tool fresh.
	// Captured $session_id is shell-quoted via "$session_id"; resume_flag that
	// ends with "=" glues without a space (equals-form CLIs).
	resumeInvoke := formatGenericResumeShellVar(baseCommand, toolDef.ResumeFlag, "session_id", dangerousFlag)
	freshInvoke := baseCommand
	if dangerousFlag != "" {
		freshInvoke += " " + dangerousFlag
	}
	// Publish the captured id into the tmux environment before using it.
	//
	// Without this the id exists only as a shell variable inside the pane: it
	// resumes this one launch and is gone, so a tool that reports its id in
	// JSON but exports no env var of its own got no durability at all — the
	// exact gap this feature is meant to close. SyncSessionIDsFromTmux and
	// GetGenericSessionID read the variable back and write it to tool_data,
	// so a reboot can still find the conversation.
	//
	// Failure is swallowed: publishing is bookkeeping, and a tmux hiccup must
	// not stop the tool from starting. The id reaches the variable via the
	// shell, never via string interpolation, so nothing here is injectable.
	//
	// Only for a LOCAL session. prepareCommand hands this whole fragment to
	// wrapForSSH for an --ssh session, so the tmux call would run on the remote
	// host, find no tmux server there, and be swallowed by the `|| true` -- a
	// silent no-op dressed as durability. The controller's tmux is the one that
	// has to hold the value, and nothing inside the remote shell can reach it.
	publish := ""
	if !i.IsSSH() {
		publish = fmt.Sprintf(`tmux set-environment %s "$session_id" >/dev/null 2>&1 || true; `,
			genericCapturedSessionIDEnv)
	}
	return envPrefix + fmt.Sprintf(
		`session_id=$(%s %s "." 2>/dev/null | jq -r '%s' 2>/dev/null) || session_id=""; `+
			`if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then `+
			`%s%s; `+
			`else %s; fi`,
		baseCommand, toolDef.OutputFormatFlag, toolDef.SessionIDJsonPath,
		publish, resumeInvoke, freshInvoke)
}

// genericCapturedSessionIDEnv is the tmux environment variable agent-deck owns
// for a custom tool's conversation id. It is the fallback for tools that expose
// an id in their JSON output but declare no session_id_env of their own; a tool
// that declares one keeps precedence (see genericSessionEnvNames).
const genericCapturedSessionIDEnv = "AGENTDECK_TOOL_SESSION_ID"

// formatGenericResumeCommand builds:
//
//	<base> <resume_flag> <quoted-id>[ <dangerous_flag>]
//
// When resume_flag ends with "=", the id is glued without a space
// (`tool --session='id'`). The conversation id is always shellescape-quoted
// because the resulting string is executed under bash -lc (tmux respawn /
// start); operator-set or tool-exported ids may contain shell metacharacters.
// Mirrors hermes/opencode/codex session-id quoting.
func formatGenericResumeCommand(baseCommand, resumeFlag, sessionID, dangerousFlag string) string {
	quotedID := shellescape.Quote(sessionID)
	var cmd string
	if strings.HasSuffix(resumeFlag, "=") {
		cmd = fmt.Sprintf("%s %s%s", baseCommand, resumeFlag, quotedID)
	} else {
		cmd = fmt.Sprintf("%s %s %s", baseCommand, resumeFlag, quotedID)
	}
	if dangerousFlag != "" {
		cmd += " " + dangerousFlag
	}
	return cmd
}

// formatGenericResumeShellVar is the capture-path variant that resumes using a
// shell variable (already double-quoted by the caller template) rather than a
// Go-side literal id.
func formatGenericResumeShellVar(baseCommand, resumeFlag, varName, dangerousFlag string) string {
	ref := "\"$" + varName + "\""
	var cmd string
	if strings.HasSuffix(resumeFlag, "=") {
		cmd = fmt.Sprintf("%s %s%s", baseCommand, resumeFlag, ref)
	} else {
		cmd = fmt.Sprintf("%s %s %s", baseCommand, resumeFlag, ref)
	}
	if dangerousFlag != "" {
		cmd += " " + dangerousFlag
	}
	return cmd
}

// buildShellPassthroughCommand builds the launch command for a Tool=="shell"
// instance. Any instance that isn't SubcommandPassthrough — which is every
// Tool=="shell" instance except the ones resolveSessionCommand itself just
// routed here — is returned byte-identical to the pre-#1821 behavior.
//
// #1800/#1821: resolveSessionCommand (cmd/agent-deck/cli_utils.go) routes
// "<tool> <subcommand> ..." invocations here — Tool="shell", Command=raw,
// SubcommandPassthrough=true — specifically to skip agent-deck's own
// --session-id/permission-flag injection (those flags are never valid after
// a subcommand). The spawned binary in that case is still claude or codex,
// though, so this restores the same AGENTDECK_*/CLAUDE_CONFIG_DIR/
// CODEX_HOME/TELEGRAM-strip treatment their own builders apply on an
// equivalent passthrough (buildClaudeCommandWithMessage's custom-command
// branch, buildCodexCommand's `trimmed != "codex"` branch) — without
// resurrecting any of the claude/codex-specific status-detection or hook
// dispatch that motivated routing through Tool="shell" in the first place
// (those stay gated on IsClaudeCompatible/IsCodexCompatible(i.Tool)
// elsewhere, which is false here).
//
// The gate is on SubcommandPassthrough (explicit provenance recorded once
// at creation), not on re-running MatchTool against Command at every spawn.
// MatchTool's builtin claude/codex classification is a whole-line substring
// match (detectSubstrings, toolregistry.go Match()), so classifying by
// re-matching an arbitrary Tool=="shell" command at spawn time would also
// fire for a pre-existing/TUI-created shell session whose command merely
// CONTAINS "claude"/"codex" anywhere in the line — e.g. a custom command
// `claude --resume <id>` persisted before this field existed, or an
// everyday `tail -f ~/.claude/logs/x` / `cd /x && claude`. Routing those
// through account/binary substitution on every future restart could
// silently start them under a different account than the one they were
// created under (Claude review, PR #1821 HIGH #1), and — for the compound
// `cd /x && claude` shape — inject inline env assignments ahead of a `cd`
// that a POSIX shell would simply ignore (Claude review, PR #1821 LOW #4).
// SubcommandPassthrough sidesteps both: it is true only when
// resolveSessionCommand already checked that the FIRST token of Command is
// a real claude/codex subcommand invocation.
func (i *Instance) buildShellPassthroughCommand(baseCommand string) string {
	if !i.SubcommandPassthrough {
		return baseCommand
	}

	// Classify off the FIRST TOKEN only, not MatchTool(baseCommand) against
	// the whole line — see the function doc above (Claude review, PR #1821
	// MEDIUM #3). SubcommandPassthrough guarantees fields[0] is the literal
	// claude/codex tool name or an explicit path containing it (resolveSessionCommand
	// validated that before setting the field), so this is strictly narrower
	// than the whole-line match, never broader.
	fields := strings.Fields(baseCommand)
	if len(fields) == 0 {
		return baseCommand
	}
	matched := MatchTool(fields[0])
	if matched == "shell" {
		// Defensive: should be unreachable given SubcommandPassthrough's
		// invariant, but never treat an unrecognized leading token as
		// claude/codex-shaped.
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	switch matched {
	case "claude":
		instanceIDPrefix := fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_PROFILE=%s ",
			shellescape.Quote(i.ID), shellescape.Quote(sessionProfileEnvValue()))
		// claudeCmd/hasCustomClaudeCommand mirrors buildClaudeCommandWithMessage's
		// own gate exactly (instance.go, "the alias handles it"): when a
		// configured [claude].command (conductor/group/global, e.g. "cdw") is
		// a custom alias rather than the literal binary, CLAUDE_CONFIG_DIR is
		// skipped because the alias manages its own config dir selection.
		// This shell-passthrough branch calls GetClaudeCommandForInstance(i)
		// again below for substituteResolvedBinary, so the custom-command
		// information was already available but previously unused for this
		// gate — forcing CLAUDE_CONFIG_DIR onto an alias broke parity with
		// the direct spawn path for any session combining a configured
		// account-switcher command with an explicit config_dir (CodeRabbit
		// review, PR #1821).
		claudeCmd := GetClaudeCommandForInstance(i)
		hasCustomClaudeCommand := claudeCmd != "claude"
		configDirPrefix := ""
		if !hasCustomClaudeCommand && IsClaudeConfigDirExplicitForInstance(i) {
			configDir := i.applyWorkerScratchOverride(GetClaudeConfigDirForInstance(i))
			// Shell-quote: configDir is a filesystem path and may contain
			// spaces (e.g. a macOS $HOME with a space in the username).
			configDirPrefix = fmt.Sprintf("CLAUDE_CONFIG_DIR=%s ", i.configDirShellExpr(configDir))
		}
		secureStoragePrefix := ""
		if !hasCustomClaudeCommand && i.Account != "" {
			secureStoragePrefix = fmt.Sprintf("CLAUDE_SECURESTORAGE_CONFIG_DIR=%s ", i.configDirShellExpr(GetClaudeConfigDirForInstance(i)))
		}
		execEnvPrefix := ""
		if flags := telegramExecEnvStripFlags(i); flags != "" {
			execEnvPrefix = "env " + flags + " "
		}
		// Codex bot review (PR #1821): the normal claude spawn path
		// (buildClaudeCommandWithMessage) resolves the configured
		// conductor/group/global claude command (e.g. an account-switcher
		// alias like "cdw"/"cdp") via GetClaudeCommandForInstance and honors
		// it even for a literal "claude" the user typed — losing that here
		// would silently run the wrong binary/account for a subcommand-shaped
		// --cmd. substituteResolvedBinary swaps only the leading word, and
		// ONLY when that leading word is the bare literal "claude" — exactly
		// mirroring buildClaudeCommandWithMessage's own `if baseCommand ==
		// "claude"` gate above, which likewise never touches an explicit
		// non-canonical path (its "custom commands" branch below returns
		// baseCommand untouched). MatchTool detects "claude"-shaped via a
		// substring match, so `matched == "claude"` alone is not sufficient
		// to gate substitution: it also fires for an explicitly configured
		// non-canonical executable (e.g. "/opt/canary/claude",
		// "codex-nightly") that merely contains the tool name. On this
		// maintainer's machines the configured account/group command is
		// often itself an account-selection wrapper, so overwriting an
		// operator-chosen path with it can silently start the subcommand
		// under the wrong account (Codex review, PR #1821 P1). Detection
		// here only classifies ("this is claude-shaped, route accordingly");
		// it must never rewrite an operator's explicit path out from under
		// them — leaving the subcommand + its args (the whole reason this
		// path exists) untouched. It also promises the same
		// AGENTDECK_RESOLVED_CONFIG_DIR/GROUP/SOURCE hint vars the normal
		// path exports for statusline/hook/fleet-placement consumers — kept
		// in this function's inline `VAR=val ` style (not
		// buildResolvedAccountHintExports' `export VAR=val;` style) because
		// envPrefix ends in `&& `, and `;` has lower precedence than `&&` in
		// bash: interleaving `export ...;` here would let this vars-and-command
		// tail run even when an earlier env-source step in envPrefix failed.
		resolvedConfigDir, resolvedSource := GetClaudeConfigDirSourceForInstance(i)
		resolvedHintPrefix := fmt.Sprintf(
			"AGENTDECK_RESOLVED_CONFIG_DIR=%s AGENTDECK_RESOLVED_GROUP=%s AGENTDECK_RESOLVED_SOURCE=%s ",
			shellescape.Quote(resolvedConfigDir), shellescape.Quote(i.GroupPath), shellescape.Quote(resolvedSource))
		resolvedCommand := substituteResolvedBinary(baseCommand, "claude", claudeCmd)
		return envPrefix + instanceIDPrefix + configDirPrefix + secureStoragePrefix + resolvedHintPrefix + execEnvPrefix + resolvedCommand
	case "codex":
		// AGENTDECK_TOOL uses `matched` ("codex"), not i.Tool (which is
		// "shell" for this passthrough instance) — buildCodexCommand's
		// equivalent injection uses the real codex identity so hook
		// subprocesses that key off AGENTDECK_TOOL=="codex" still find it.
		// AGENTDECK_TITLE is shell-quoted (not Go-%q-quoted): i.Title is
		// user-editable and %q does not stop $(...) / backtick command
		// substitution inside the double quotes it produces — a title like
		// `x$(touch /tmp/pwned)` would execute at every spawn otherwise.
		agentdeckEnvPrefix := fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%s AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ",
			shellescape.Quote(i.ID), shellescape.Quote(i.Title), matched, shellescape.Quote(sessionProfileEnvValue()))
		// CODEX_HOME selects which account's Codex config/credentials the
		// spawned process reads — injecting it ahead of an explicitly
		// configured non-canonical binary (e.g. "codex-nightly", itself
		// potentially an account-selection wrapper) risks the process
		// starting under the wrong account, same class of bug as
		// substituteResolvedBinary below (Codex review, PR #1821 follow-up).
		// buildCodexCommand's own custom-command early-return already
		// skips this injection entirely for an explicit command; mirror
		// that here by only injecting when the user typed the bare
		// literal "codex".
		codexHomePrefix := ""
		if isLiteralToolInvocation(baseCommand, "codex") && isCodexHomeExplicit() {
			if codexHome := strings.TrimSpace(getCodexHomeDir()); codexHome != "" {
				codexHomePrefix = "CODEX_HOME=" + shellescape.Quote(codexHome) + " "
			}
		}
		// Same rationale as the claude case above: honor [codex].command
		// (e.g. a wrapper/account-switcher) instead of always running the
		// literal binary name the user typed after "codex" — but ONLY when
		// the user typed the bare literal "codex"; an explicit non-canonical
		// path (e.g. "codex-nightly") is never rewritten. See the extended
		// comment on the "claude" case above.
		resolvedCommand := substituteResolvedBinary(baseCommand, "codex", GetCodexCommand())
		return envPrefix + agentdeckEnvPrefix + codexHomePrefix + resolvedCommand
	default:
		return envPrefix + baseCommand
	}
}

// substituteResolvedBinary replaces the leading whitespace-delimited word of
// baseCommand with resolvedBinary (the configured conductor/group/global
// override for the tool, e.g. an account-switcher alias) — but ONLY when
// that leading word is exactly literalName (the bare tool name, "claude" or
// "codex"), leaving the rest of baseCommand — the subcommand and its args —
// untouched.
//
// literalName gates the substitution deliberately: buildShellPassthroughCommand's
// caller classifies baseCommand via MatchTool, which detects "claude"/"codex"
// by a *substring* match — it also fires for an explicitly configured
// non-canonical executable that merely contains the tool name, e.g.
// "/opt/canary/claude" or "codex-nightly". If the caller chose that path on
// purpose (often itself an account-selection wrapper on this maintainer's
// machines), substituting resolvedBinary in its place would silently run a
// *different* binary/account than the one explicitly configured (Codex
// review, PR #1821 P1). Detection classifies; it must never rewrite an
// operator's explicit path. Substitution is therefore restricted to the one
// case where there is no explicit path to protect: the user typed the bare
// literal tool name, exactly mirroring buildClaudeCommandWithMessage's own
// `if baseCommand == "claude"` gate, whose "custom commands" branch likewise
// returns a non-literal baseCommand untouched.
//
// A no-op (returns baseCommand unchanged) when resolvedBinary is empty, the
// leading word isn't literalName, or it already equals resolvedBinary; this
// keeps the common no-override case byte-identical to before.
func substituteResolvedBinary(baseCommand, literalName, resolvedBinary string) string {
	resolvedBinary = strings.TrimSpace(resolvedBinary)
	if resolvedBinary == "" {
		return baseCommand
	}
	if !isLiteralToolInvocation(baseCommand, literalName) {
		return baseCommand
	}
	trimmed := strings.TrimSpace(baseCommand)
	fields := strings.Fields(trimmed)
	if fields[0] == resolvedBinary {
		return baseCommand
	}
	rest := strings.TrimPrefix(trimmed, fields[0])
	return resolvedBinary + rest
}

// isLiteralToolInvocation reports whether baseCommand's leading
// whitespace-delimited word is exactly literalName — i.e. the user typed
// the bare tool name ("claude"/"codex") rather than an explicit,
// potentially non-canonical, executable path or wrapper. Account-selection
// env/binary substitution in buildShellPassthroughCommand is gated on this:
// an explicit path is the operator's deliberate choice and must never be
// second-guessed, only the bare literal is eligible (see
// substituteResolvedBinary and the codexHomePrefix gate above).
func isLiteralToolInvocation(baseCommand, literalName string) bool {
	fields := strings.Fields(strings.TrimSpace(baseCommand))
	return len(fields) > 0 && fields[0] == literalName
}

// GetGenericSessionID resolves the conversation id for a custom [tools.*]
// tool. Preference order:
//  1. Live tmux env named by [tools.X].session_id_env (when set and present)
//  2. Live tmux env AGENTDECK_TOOL_SESSION_ID, which the capture path publishes
//     for tools that expose an id in their JSON output but export no env var of
//     their own (see buildGenericCommand)
//  3. Persisted GenericSessionID from tool_data (survives reboot)
//
// A live value is written through, so the next cold start resumes the same
// chat. A persisted value is only returned while its recorded tool and
// execution location still match this session — see generic_session_scope.go.
// Whitespace-only values are treated as empty (not resumable).
func (i *Instance) GetGenericSessionID() string {
	// The scope check comes FIRST, before the live pane is consulted at all.
	//
	// A pane outlives the settings it was launched under: change the tool, the
	// command, the project path or the SSH destination on a running session and
	// the pane keeps exporting the id the OLD tool captured. Reading the pane
	// first and adopting whatever it says rebinds that id to the new scope, and
	// one poll later an id this function had just refused is indistinguishable
	// from a legitimate one. Refusing before reading closes that laundering
	// path: while a recorded binding disagrees with the session as it stands,
	// nothing about this session is a source of conversation identity.
	if reason := i.genericSessionScopeMismatch(); reason != "" {
		i.logGenericResumeRefusal(reason)
		return ""
	}

	for _, envName := range i.genericSessionEnvNames() {
		if i.tmuxSession == nil {
			break
		}
		sessionID, err := i.tmuxSession.GetEnvironment(envName)
		if err != nil {
			continue
		}
		if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
			i.persistGenericSessionIDIfChanged(sessionID)
			return sessionID
		}
	}

	return strings.TrimSpace(i.GenericSessionID)
}

// genericSessionEnvNames lists the tmux environment variables that may carry a
// custom tool's live conversation id, most specific first.
//
// The configured session_id_env is the tool's own variable and wins. The
// agent-deck-owned fallback exists because a tool can declare
// output_format_flag + session_id_json_path (so agent-deck can read an id out
// of its JSON) while exporting nothing — before the fallback, that id was
// captured inside the pane's shell, used once, and lost, which is a reboot away
// from being no persistence at all.
func (i *Instance) genericSessionEnvNames() []string {
	return i.genericSessionEnvNamesFor(i.Tool)
}

// genericSessionEnvNamesFor is genericSessionEnvNames over a set of tools,
// deduplicated and always including the agent-deck-owned fallback.
//
// Erasing a binding needs the variables the tool that CAPTURED it declared, not
// only the ones the session's current tool declares — after a tool change those
// are different names, and the one left behind is the one the pane would hand
// back on the next read.
func (i *Instance) genericSessionEnvNamesFor(tools ...string) []string {
	names := make([]string, 0, len(tools)+1)
	seen := make(map[string]bool, len(tools)+1)
	for _, tool := range tools {
		toolDef := GetToolDef(strings.TrimSpace(tool))
		if toolDef == nil || toolDef.SessionIDEnv == "" || seen[toolDef.SessionIDEnv] {
			continue
		}
		seen[toolDef.SessionIDEnv] = true
		names = append(names, toolDef.SessionIDEnv)
	}
	return append(names, genericCapturedSessionIDEnv)
}

// DisplaySessionID returns the session ID the PREVIEW pane surfaces for this
// instance's tool, mirroring the per-tool branching in the right-pane render
// so a copy of the preview info carries the same ID the user sees. Returns ""
// when no session ID is known for the tool.
func (i *Instance) DisplaySessionID() string {
	switch {
	case IsClaudeCompatible(i.Tool):
		return i.ClaudeSessionID
	case i.Tool == "gemini":
		return i.GeminiSessionID
	case i.Tool == "opencode":
		return i.OpenCodeSessionID
	case i.Tool == "codex":
		return i.CodexSessionID
	default:
		return i.GetGenericSessionID()
	}
}

// CanRestartGeneric returns true if a custom tool can be restarted with session resume
func (i *Instance) CanRestartGeneric() bool {
	toolDef := GetToolDef(i.Tool)
	if toolDef == nil {
		return false
	}
	// resume_flag is required; the id may come from live session_id_env OR
	// from the persisted generic_session_id (post-reboot).
	if toolDef.ResumeFlag == "" {
		return false
	}
	return i.GetGenericSessionID() != ""
}

func (i *Instance) applyWrapper(command string) (string, error) {
	wrapper := i.Wrapper
	if wrapper == "" {
		if toolDef := GetToolDef(i.Tool); toolDef != nil {
			wrapper = toolDef.Wrapper
		}
	}
	if wrapper == "" {
		return command, nil
	}
	if strings.Contains(wrapper, wrapperPlaceholder) {
		return substituteWrapperCommand(wrapper, command), nil
	}
	return wrapper, nil
}

// substituteWrapperCommand replaces {command} in wrapper with command.
//
// A wrapper that puts a program or assignment in front of {command} ("env
// X=Y {command}", "nice {command}", "X=Y {command}") expects {command} to be
// ONE command. The launch command is usually a compound shell line — the env
// prelude from buildEnvSourceCommand ("export A=.. && source f && ...") plus
// "exec claude ..." — so a textual splice produced "env X=Y export A=.. &&
// ...": env tried to exec a program named "export", the prelude was skipped
// and the agent ran without X. In that case the command is handed over as a
// single `bash -c '<command>'` argv instead, so the wrapper's assignment is
// inherited by the whole line, prelude and agent alike. Such a prefix
// wrapper therefore receives the command as one argv; a wrapper that
// re-parses its arguments as a string ("ssh host {command}") should use
// --ssh or quote the placeholder.
//
// Text after {command} ("{command} --flag", issue #601) must still reach the
// agent, not become bash's $0/$1, so when there is any the payload ends in
// "$@" and bash is given an explicit $0: `bash -c '<command> "$@"' bash
// --flag` appends the flags exactly where the textual splice put them.
//
// A placeholder inside a quoted string (bash -lc "{command}", nvim
// +'terminal {command}') is already handed to a shell as a whole, so it is
// substituted in place, escaped for that quote context; the argv form there
// would break the wrapper's own quoting.
//
// An unquoted placeholder that is already a shell's command string (bash -lc
// {command}, sh -c {command}) gets the command as that one quoted word,
// without a second bash -c: wrapping it again made the outer shell run a bare
// "bash" with the payload as its $0, opening an empty shell instead of the
// agent, and a textual splice handed the shell only the first word.
//
// A wrapper that starts with {command}, or a plain single command, keeps the
// textual substitution unchanged.
func substituteWrapperCommand(wrapper, command string) string {
	parts := strings.Split(wrapper, wrapperPlaceholder)
	prefix := parts[0]
	sub := command
	if strings.TrimSpace(prefix) != "" && placeholderQuoteContext(prefix) == 0 {
		shellC := prefixEndsWithShellCommandFlag(prefix)
		if shellC || commandNeedsShell(command) {
			payload := command
			if strings.TrimSpace(strings.Join(parts[1:], "")) != "" {
				payload += ` "$@"`
			}
			sub = "'" + strings.ReplaceAll(payload, "'", `'"'"'`) + "'"
			if !shellC {
				sub = "bash -c " + sub
			}
			if payload != command {
				sub += " bash"
			}
		}
	}

	var b strings.Builder
	b.WriteString(prefix)
	for n, part := range parts[1:] {
		switch placeholderQuoteContext(strings.Join(parts[:n+1], wrapperPlaceholder)) {
		case '\'':
			b.WriteString(strings.ReplaceAll(command, "'", `'"'"'`))
		case '"':
			b.WriteString(doubleQuoteEscaper.Replace(command))
		default:
			b.WriteString(sub)
		}
		b.WriteString(part)
	}
	return b.String()
}

// doubleQuoteEscaper escapes the characters that stay special inside a
// double-quoted shell string.
var doubleQuoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")

// placeholderQuoteContext scans prefix with shell quoting rules and returns
// the quote character still open at its end (single or double), or 0.
func placeholderQuoteContext(prefix string) byte {
	var quote byte
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		switch quote {
		case '\'':
			if c == '\'' {
				quote = 0
			}
		case '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		default:
			if c == '\\' {
				i++
			} else if c == '\'' || c == '"' {
				quote = c
			}
		}
	}
	return quote
}

// wrapperShells are the shells whose -c option takes the next word as the
// command string (prefixEndsWithShellCommandFlag).
var wrapperShells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "mksh": true, "ash": true}

// prefixEndsWithShellCommandFlag reports whether the wrapper text before an
// unquoted {command} ends with a shell invocation whose short options include
// -c ("bash -lc ", "/bin/sh -c ", "zsh -l -c "), so the placeholder is that
// shell's command string.
func prefixEndsWithShellCommandFlag(prefix string) bool {
	if prefix == "" || (prefix[len(prefix)-1] != ' ' && prefix[len(prefix)-1] != '\t') {
		return false
	}
	fields := strings.Fields(prefix)
	sawC := false
	for n := len(fields) - 1; n >= 0; n-- {
		f := fields[n]
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && len(f) > 1 {
			if strings.Contains(f[1:], "c") {
				sawC = true
			}
			continue
		}
		return sawC && wrapperShells[filepath.Base(f)]
	}
	return false
}

// commandNeedsShell reports whether command only works when parsed by a
// shell: it has an operator or grouping (&&, ;, |, redirection, subshell) or
// starts with a builtin or VAR=val assignment that no program prefix can
// exec. False positives are harmless (bash -c runs a simple command the same).
func commandNeedsShell(command string) bool {
	if strings.ContainsAny(command, ";&|<>()`\n") {
		return true
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	if strings.Contains(fields[0], "=") {
		return true
	}
	switch fields[0] {
	case "export", "source", ".", "cd", "exec", "unset", "eval", "set",
		"if", "for", "while", "until", "case", "{", "[", "[[", "!", "time":
		return true
	}
	return false
}

// hasEffectiveWrapper returns true if the instance has a wrapper configured,
// either directly on the instance or via the tool definition in config.toml.
func (i *Instance) hasEffectiveWrapper() bool {
	if i.Wrapper != "" {
		return true
	}
	if toolDef := GetToolDef(i.Tool); toolDef != nil && toolDef.Wrapper != "" {
		return true
	}
	return false
}

// loadCustomPatternsFromConfig loads detection patterns from built-in defaults + config.toml
// overrides, and sets them on the tmux session for status detection and tool auto-detection.
// Works for ALL tools: built-in (claude, gemini, opencode, codex) and custom.
func (i *Instance) loadCustomPatternsFromConfig() {
	if i.tmuxSession == nil {
		return
	}

	// Merge built-in defaults with any user config overrides/extras
	raw := MergeToolPatterns(i.Tool)
	if raw != nil {
		resolved, err := tmux.CompilePatterns(raw)
		if err != nil {
			sessionLog.Warn("pattern_compile_error", slog.String("tool", i.Tool), slog.String("error", err.Error()))
		}
		if resolved != nil {
			i.tmuxSession.SetPatterns(resolved)
		}
	}

	// Keep detect patterns for DetectTool() (separate from busy/prompt detection)
	if toolDef := GetToolDef(i.Tool); toolDef != nil {
		i.tmuxSession.SetDetectPatterns(i.Tool, toolDef.DetectPatterns)
	}
}

// noteObservedRuntimeLocked records a DetectTool result without changing Tool.
// Status patterns follow the observed agent so a foreground OpenCode pane is
// classified with OpenCode's busy/prompt rules; they are restored to the
// configured tool when the observation drops back to shell. Caller holds i.mu.
// SetDetectPatterns is deliberately not used: it would pin customToolName and
// make the next DetectTool skip command-based observation.
func (i *Instance) noteObservedRuntimeLocked(observed string) {
	if observed == "" {
		return
	}
	if observed == i.observedTool {
		return
	}
	prev := i.runtimePatternToolLocked()
	i.observedTool = observed
	next := i.runtimePatternToolLocked()
	if next == prev || i.tmuxSession == nil {
		return
	}
	raw := MergeToolPatterns(next)
	if raw == nil {
		return
	}
	resolved, err := tmux.CompilePatterns(raw)
	if err != nil || resolved == nil {
		sessionLog.Warn("pattern_compile_error", slog.String("tool", next), slog.String("error", errString(err)))
		return
	}
	i.tmuxSession.SetPatterns(resolved)
}

func (i *Instance) runtimePatternToolLocked() string {
	if i.observedTool != "" && i.observedTool != "shell" {
		return i.observedTool
	}
	return i.Tool
}

// shellQuote wraps a string in single quotes, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
