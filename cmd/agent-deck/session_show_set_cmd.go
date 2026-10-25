package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// sessionShowStatusFields is the status pass `session show` makes and the
// status keys it reports: status, substate, substate_detail and
// background_work (issue #2473). It warms the tmux pane-title cache and loads
// the hook status so the result matches the TUI and /api/menu (issue #610),
// updates the status, then reads the substate (the pass's pane capture, which
// can settle the status under hook lag, session/hook_lag.go). Optional keys
// are omitted when empty so existing consumers see byte-stable output.
func sessionShowStatusFields(inst *session.Instance) map[string]interface{} {
	session.RefreshInstancesForCLIStatus([]*session.Instance{inst})
	_ = inst.UpdateStatus()
	substate := string(inst.Substate())
	fields := map[string]interface{}{"status": StatusString(inst.Status)}
	if substate != "" {
		fields["substate"] = substate
	}
	if exitCode := inst.ExitCode(); exitCode != nil {
		fields["exit_code"] = *exitCode
	}
	if detail := inst.SubstateDetail(); detail != "" {
		fields["substate_detail"] = detail
	}
	if work := inst.BackgroundWorkJSON(); work != nil {
		fields["background_work"] = work
	}
	return fields
}

// handleSessionShow shows session details
func handleSessionShow(profile string, args []string) {
	fs := flag.NewFlagSet("session show", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session show [id|title] [options]")
		fmt.Println()
		fmt.Println("Show session details. If no ID is provided, auto-detects current session.")
		fmt.Println("Account: shows the quoted stored account slot, not a resolved account or login identity.")
		fmt.Println(`JSON always includes the raw "account" string, including "" when no slot is stored.`)
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	// Resolve session (allow current session detection)
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		// If no identifier was provided and we're in tmux, try fallback detection
		if identifier == "" && os.Getenv("TMUX") != "" {
			// First try current profile
			inst = findSessionByTmux(instances)
			if inst == nil {
				// Search ALL profiles for matching tmux session
				var foundProfile string
				inst, foundProfile = findSessionByTmuxAcrossProfiles()
				if inst != nil && foundProfile != profile {
					// Found in a different profile - reload its session/group
					// data too, or groupTree below is built from the wrong
					// profile and SessionPosition can't find inst (order: -1).
					profile = foundProfile
					storage, instances, groupsData, err = loadSessionData(profile)
					if err != nil {
						out.Error(err.Error(), ErrCodeNotFound)
						exitCLI(1)
					}
				}
			}
			if inst == nil {
				// Still not found, show raw tmux info
				showTmuxSessionInfo(out, *jsonOutput)
				return
			}
		} else {
			out.Error(errMsg, errCode)
			if errCode == ErrCodeNotFound {
				exitCLI(2)
			}
			exitCLI(1)
		}
	}

	statusFields := sessionShowStatusFields(inst)

	// #2080: surface the raw hook-driven status and its freshness alongside
	// the derived "status" field. `--defer-if-busy` and the send verification
	// loop (#1578, #2273) already treat a FRESH hook status of
	// "running"/"starting" as the authoritative busy/interactive signal — it
	// covers an open AskUserQuestion picker (whose PreToolUse event never
	// advances to Stop until the human answers) even while "status" still
	// reads "waiting". Callers that need to gate on real interactive state
	// (e.g. the conductor heartbeat guard) read these two fields directly
	// instead of re-deriving it from a raw pane-text capture.
	hookStatus, hookStatusFresh := inst.GetHookStatus()

	// Get MCP info if Claude session
	var mcpInfo *session.MCPInfo
	if session.IsClaudeCompatible(inst.Tool) {
		mcpInfo = inst.GetMCPInfo()
	}

	// Prepare JSON output. "order" is the position in the group's session
	// slice as the storage layer sorts it (what `session set <id> order <n>`
	// consumes), not the raw sort_order, which ties at 0 on every
	// launch-appended row.
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	jsonData := map[string]interface{}{
		"id":                   inst.ID,
		"title":                inst.Title,
		"profile":              profile,
		"path":                 inst.ProjectPath,
		"group":                inst.GroupPath,
		"order":                groupTree.SessionPosition(inst),
		"pin":                  string(inst.Pin),
		"parent_session_id":    inst.ParentSessionID,
		"parent_project_path":  inst.ParentProjectPath,
		"no_transition_notify": inst.NoTransitionNotify,
		"title_locked":         inst.TitleLocked,
		"tool":                 inst.Tool,
		"account":              inst.Account,
		"created_at":           inst.CreatedAt.Format(time.RFC3339),
		// Always present (see the #1924 "wrapper" reasoning above): an
		// absent key would be ambiguous with "the hook never fired", when
		// what actually happened is "this build predates the field".
		"hook_status":       hookStatus,
		"hook_status_fresh": hookStatusFresh,
	}
	// Durable queued sends, the same entries as `session queue list --json`.
	if *jsonOutput {
		queue, err := sessionQueueEntries(storage, inst.ID)
		if err != nil {
			out.Error(fmt.Sprintf("cannot list queued sends: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		jsonData["queue"] = queue
	}
	for k, v := range statusFields {
		jsonData[k] = v
	}
	modelInfo := inst.LaunchModelInfo()
	addModelInfoJSON(jsonData, modelInfo)
	addEffortJSON(jsonData, inst)
	addClaudeOptionsJSON(jsonData, inst)
	addAutoNameJSON(jsonData, inst)

	if inst.Command != "" {
		jsonData["command"] = inst.Command
	}

	// #1924: always present, even when empty. `session set <id> wrapper …` is
	// the natural thing to verify with `session show --json`, and this key was
	// missing entirely — so `.wrapper` read back as null and a write that had
	// in fact persisted looked like silent data loss. Same reasoning the
	// channels field states below: omitting when empty makes absence-of-field
	// ambiguous with absence-of-value, and here that ambiguity cost a user a
	// bug report against the wrong component.
	jsonData["wrapper"] = inst.Wrapper
	if inst.Favorite {
		jsonData["favorite"] = true
	}
	// Omitted when none or expired (see session.IntendedSwitch).
	if rec := inst.ActiveIntendedSwitchJSON(time.Now()); rec != nil {
		jsonData["intended_switch"] = rec
	}

	// macapp-core-needs §3: the live native transcript and every native id
	// seen for this session (Codex re-creates its rollout after the trust
	// prompt). Omitted when unknown, so older consumers see no change.
	if p := session.LiveTranscriptPath(inst, instances); p != "" {
		jsonData["transcript_path"] = p
	}
	if ids := session.TranscriptIDs(inst, instances); len(ids) > 0 {
		jsonData["transcript_ids"] = ids
	}

	if session.SupportsNativeFork(inst.Tool) {
		jsonData["can_fork"] = inst.CanFork()
	}
	if inst.Tool == "opencode" {
		jsonData["opencode_session_id"] = inst.OpenCodeSessionID
	}
	if session.IsClaudeCompatible(inst.Tool) {
		jsonData["claude_session_id"] = inst.ClaudeSessionID
		jsonData["can_restart"] = inst.CanRestart()

		if mcps := mcpInfoForJSON(mcpInfo); mcps != nil {
			jsonData["mcps"] = mcps
		}

		// Always include channels for claude sessions — omitting when empty
		// would make absence-of-field ambiguous with absence-of-value. Match
		// the `list --json` emitter which surfaces this field unconditionally.
		if len(inst.Channels) > 0 {
			jsonData["channels"] = inst.Channels
		}

		// Plugins (RFC docs/rfc/PLUGIN_ATTACH.md §10.5) — surface when
		// non-empty so downstream tooling can introspect per-session
		// enabledPlugins state without parsing the scratch settings.json.
		if len(inst.Plugins) > 0 {
			jsonData["plugins"] = inst.Plugins
		}
		// Surface the auto-link opt-out (RFC §4.7) when set, so tooling
		// can distinguish "user disabled auto-link" from "no plugins".
		if inst.PluginChannelLinkDisabled {
			jsonData["plugin_channel_link_disabled"] = true
		}
		// AutoLinkedChannels (RFC §4.7, G4/C2 fix) — internal-ish state
		// for ownership tracking, but exposing in JSON helps downstream
		// tooling distinguish auto-linked vs user-managed channels.
		if len(inst.AutoLinkedChannels) > 0 {
			jsonData["auto_linked_channels"] = inst.AutoLinkedChannels
		}
	}
	addCodexMetadataJSON(jsonData, inst)
	// Say when a codex session's light is content detection only (no notify
	// hook in its CODEX_HOME); unknown for a remote session.
	codexHooksState, codexHooksConfig := codexHooksStateForInstance(inst)
	if codexHooksState != "" {
		jsonData["codex_hooks"] = codexHooksState
		if codexHooksConfig != "" {
			jsonData["codex_hooks_config"] = codexHooksConfig
		}
	}

	if tmuxSession := inst.GetTmuxSession(); tmuxSession != nil {
		jsonData["tmux_session"] = tmuxSession.Name
	}
	// Who else has this session open (shared attach). Present only when tmux
	// answered: [] is "nobody", an absent key is "unknown".
	viewers, viewersKnown := inst.Viewers(context.Background())
	if viewersKnown {
		jsonData["viewers"] = viewers
	}

	// #1580: surface a spawn-failure diagnostic when the session errored at
	// startup (bare "error" with no pane). Include the structured record in
	// --json so tooling can read it too.
	spawnFailure := inst.SpawnFailure()
	if spawnFailure != nil {
		jsonData["spawn_failure"] = spawnFailureJSON(spawnFailure)
	}

	// An auth hold explains a bare "error" that no restart can clear, and tells
	// automation (conductors, watchdogs reading --json) to stop retrying.
	authHold := inst.AuthHold()
	if authHold != nil {
		jsonData["auth_hold"] = map[string]interface{}{
			"reason":        authHold.Reason,
			"remedy":        authHold.Remedy(),
			"evidence":      authHold.Evidence,
			"boot_attempts": authHold.BootAttempts,
			"ts":            authHold.Timestamp,
		}
	}

	// A conductor whose channel plugin has no state (typically right after an
	// account switch) never starts its bot; say so loudly.
	channelWarnings := inst.ChannelStateWarnings()
	if len(channelWarnings) > 0 {
		jsonData["channel_warnings"] = channelWarnings
	}

	// Build human-readable output
	var sb strings.Builder

	for _, w := range channelWarnings {
		sb.WriteString(fmt.Sprintf("⚠  WARNING: %s\n", w))
	}
	sb.WriteString(fmt.Sprintf("Session: %s\n", inst.Title))
	sb.WriteString(fmt.Sprintf("Profile: %s\n", profile))
	sb.WriteString(fmt.Sprintf("ID:      %s\n", inst.ID))
	sb.WriteString(fmt.Sprintf("Status:  %s %s\n", StatusSymbol(inst.Status), StatusString(inst.Status)))
	if exitCode := inst.ExitCode(); exitCode != nil {
		sb.WriteString(fmt.Sprintf("Exit:    process exited (%d)\n", *exitCode))
	}
	sb.WriteString(fmt.Sprintf("Path:    %s\n", FormatPath(inst.ProjectPath)))

	if inst.GroupPath != "" {
		sb.WriteString(fmt.Sprintf("Group:   %s\n", inst.GroupPath))
	}

	sb.WriteString(fmt.Sprintf("Tool:    %s\n", inst.Tool))
	sb.WriteString(fmt.Sprintf("Account: %s\n", strconv.Quote(inst.Account)))
	if modelInfo.ModelID != "" {
		if modelInfo.Model != "" {
			sb.WriteString(fmt.Sprintf("Model:   %s\n", modelInfo.Model))
		}
		if modelInfo.Version != "" {
			sb.WriteString(fmt.Sprintf("Version: %s\n", modelInfo.Version))
		}
		sb.WriteString(fmt.Sprintf("ModelID: %s\n", modelInfo.ModelID))
	} else if session.SupportsLaunchModel(inst.Tool) {
		sb.WriteString("Model:   tool default\n")
	}

	if inst.Command != "" {
		sb.WriteString(fmt.Sprintf("Command: %s\n", inst.Command))
	}

	if session.IsClaudeCompatible(inst.Tool) {
		if inst.ClaudeSessionID != "" {
			truncatedID := inst.ClaudeSessionID
			if len(truncatedID) > 36 {
				truncatedID = truncatedID[:36] + "..."
			}
			canForkStr := "no"
			if inst.CanFork() {
				canForkStr = "yes"
			}
			sb.WriteString(fmt.Sprintf("Claude:  session_id=%s (can fork: %s)\n", truncatedID, canForkStr))
		} else {
			sb.WriteString("Claude:  no session ID detected\n")
		}

		if mcpInfo != nil && mcpInfo.HasAny() {
			var mcpParts []string
			for _, name := range mcpInfo.Local() {
				mcpParts = append(mcpParts, name+" (local)")
			}
			for _, name := range mcpInfo.Global {
				mcpParts = append(mcpParts, name+" (global)")
			}
			for _, name := range mcpInfo.Project {
				mcpParts = append(mcpParts, name+" (project)")
			}
			sb.WriteString(fmt.Sprintf("MCPs:    %s\n", strings.Join(mcpParts, ", ")))
		}

		// Channels and Plugins (RFC docs/rfc/PLUGIN_ATTACH.md). Surfaced
		// for claude sessions so users can verify per-session topology
		// without parsing state.db or the scratch settings.json.
		if len(inst.Channels) > 0 {
			sb.WriteString(fmt.Sprintf("Channels:%s\n", " "+strings.Join(inst.Channels, ", ")))
		}
		if len(inst.Plugins) > 0 {
			sb.WriteString(fmt.Sprintf("Plugins: %s\n", strings.Join(inst.Plugins, ", ")))
			if inst.PluginChannelLinkDisabled {
				sb.WriteString("         (auto-channel-link disabled — RFC §4.7)\n")
			}
		}
	}

	if codexHooksState != "" {
		sb.WriteString(fmt.Sprintf("Codex:   %s\n", codexHooksLine(codexHooksState, codexHooksConfig)))
	}

	if inst.NoTransitionNotify {
		sb.WriteString("Notify:  transition events suppressed\n")
	}
	sb.WriteString(fmt.Sprintf("Created: %s\n", inst.CreatedAt.Format("2006-01-02 15:04:05")))

	if !inst.LastAccessedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("Accessed: %s\n", inst.LastAccessedAt.Format("2006-01-02 15:04:05")))
	}

	if inst.Exists() {
		tmuxSession := inst.GetTmuxSession()
		if tmuxSession != nil {
			sb.WriteString(fmt.Sprintf("Tmux:    %s\n", tmuxSession.Name))
		}
		sb.WriteString(fmt.Sprintf("Viewers: %s\n", formatViewersLine(viewers, viewersKnown)))
	}

	// #1580: print the spawn-failure block so `session show` on an errored
	// session explains why it died instead of leaving the user with a bare
	// "error".
	if spawnFailure != nil {
		sb.WriteString("\n")
		sb.WriteString(spawnFailure.FormatForDisplay())
	}

	// The auth block goes LAST so it is the final thing on screen: it is the only
	// one of these diagnostics that names an action the user must take.
	if authHold != nil {
		sb.WriteString("\n")
		sb.WriteString(authHold.FormatForDisplay())
	}

	out.Print(sb.String(), jsonData)
}

func mcpInfoForJSON(mcpInfo *session.MCPInfo) map[string]interface{} {
	if mcpInfo == nil || !mcpInfo.HasAny() {
		return nil
	}
	return map[string]interface{}{
		"local":   mcpInfo.Local(),
		"global":  mcpInfo.Global,
		"project": mcpInfo.Project,
	}
}

// handleSessionSet updates a session property
func handleSessionSet(profile string, args []string) {
	fs := flag.NewFlagSet("session set", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	intendedReason := fs.String("intended", "", "Mark this account/model switch as intended, with a reason (account and model only)")
	intendedFor := fs.Duration("intended-for", session.DefaultIntendedSwitchTTL, "How long the --intended mark lasts (max 168h)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session set <id|title> <field> <value> [options]")
		fmt.Println()
		fmt.Println("Update a session property.")
		fmt.Println()
		fmt.Println("Fields:")
		fmt.Println("  title              Session title")
		fmt.Println("  path               Project path")
		fmt.Println("  command            Command to run")
		fmt.Println("  tool               Tool type (claude, gemini, shell, etc.)")
		fmt.Println("  wrapper            Wrapper command (use {command} to include tool command; an unquoted {command} after a prefix gets a compound command (&&, ;, |, export ...) as one bash -c argv, so for remote use --ssh or quote {command})")
		fmt.Println("  channels           Comma-separated plugin channel ids (claude only)")
		fmt.Printf("  plugins            Comma-separated plugin catalog names (claude only) — see [plugins.<name>] in %s\n", effectiveUserConfigPathForHelp())
		fmt.Println("  extra-args         Extra claude CLI tokens (claude only; use `-- --flag value` for tokens starting with -; persisted plaintext — no secrets)")
		fmt.Println("  model              Per-session model override (e.g. opus/sonnet/haiku or a gemini model); persists across restart (#1436). Empty clears it.")
		fmt.Println("  color              Optional TUI row tint: '#RRGGBB' or ANSI '0'..'255' or '' (issue #391)")
		fmt.Println("  claude-session-id  Claude conversation ID")
		fmt.Println("  favorite           Favourite flag: true or false (favorite in list/show --json)")
		fmt.Println("  gemini-session-id  Gemini conversation ID")
		fmt.Println("  tool-session-id    Custom [tools.*] conversation ID (for resume_flag after reboot)")
		fmt.Println("  account            Named account slot (#924) — resolves via [profiles.<account>.claude].config_dir; restart required")
		fmt.Println("  idle-timeout       Auto-stop after no tmux output for this duration (#1143; Go duration: 30m, 1h, 24h; 0 disables)")
		fmt.Println("  context-level      Harness context-level override: none, primer, or full (#2260; empty clears; global < group < session; see `session primer`); restart required")
		fmt.Println("  prompt-suggestions Claude prompt suggestions: children-off (off when the session has a parent), on, or off (empty clears; session > profile > [launch]); restart required")
		fmt.Println("  order              0-based position in the group (see `session show --json` .order); clamps to the end")
		fmt.Println("  intended-switch    Mark the CURRENT account+model as switched on purpose (value = reason; 24h); '' clears")
		fmt.Println()
		fmt.Println("Intended switches:")
		fmt.Println("  `account`/`model` with --intended \"<reason>\" record the switch as intended until --intended-for")
		fmt.Println("  elapses; a later account/model set without --intended clears the record. list --json and")
		fmt.Println("  session show --json report it as intended_switch {account, model, reason, at, expires_at}")
		fmt.Println("  while unexpired. Guard contract: a switch is intended when the session's current account")
		fmt.Println("  and model equal intended_switch's and now < expires_at.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session set my-project title \"New Title\"")
		fmt.Println("  agent-deck session set my-project claude-session-id \"abc123-def456\"")
		fmt.Println("  agent-deck session set my-project path /new/path/to/project")
		fmt.Println("  agent-deck session set my-project wrapper \"nvim +'terminal {command}'\"")
		fmt.Println("  agent-deck session set my-project color \"#ff00aa\"     # truecolor hex tint")
		fmt.Println("  agent-deck session set my-project color 203              # ANSI 256-palette pink")
		fmt.Println("  agent-deck session set my-project color \"\"              # clear (opt-out)")
		fmt.Println("  agent-deck session set my-project context-level primer")
		fmt.Println("  agent-deck session set my-project context-level \"\"       # clear (inherit group/global)")
		fmt.Println("  agent-deck session set my-project account work --intended \"acme blocked\" --intended-for 48h")
		fmt.Println("  agent-deck session set my-project model opus --intended \"Fable weekly cap\"")
		fmt.Println("  agent-deck session set my-project intended-switch \"switched in the TUI\"")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	if fs.NArg() < 3 {
		fs.Usage()
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	field := fs.Arg(1)
	value := fs.Arg(2)
	// For extra-args: accept an arbitrary number of positional tokens after
	// the field name. Use `--` terminator so Go's flag package leaves tokens
	// starting with `-` alone, e.g.:
	//   agent-deck session set <id> extra-args -- --model opus
	extraArgTokens := fs.Args()[2:]
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// --intended / --intended-for are validated before anything is loaded so
	// a rejected call changes nothing.
	intendedSet, intendedForSet := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "intended":
			intendedSet = true
		case "intended-for":
			intendedForSet = true
		}
	})
	reason := strings.TrimSpace(*intendedReason)
	if intendedSet || intendedForSet {
		switch {
		case field != session.FieldAccount && field != session.FieldModel:
			out.Error(fmt.Sprintf("--intended and --intended-for apply only to the account and model fields (got %q)", field), ErrCodeInvalidOperation)
			exitCLI(1)
		case !intendedSet:
			out.Error("--intended-for requires --intended \"<reason>\"", ErrCodeInvalidOperation)
			exitCLI(1)
		case reason == "":
			out.Error("--intended needs a non-empty reason", ErrCodeInvalidOperation)
			exitCLI(1)
		}
		if err := session.ValidateIntendedSwitchTTL(*intendedFor); err != nil {
			out.Error("invalid --intended-for: "+err.Error(), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}

	// Load sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	// A title change takes a (title, location) pair exactly as `add` does, so it
	// runs under the same profile registration lock and re-reads the instance
	// list INSIDE it. Without that, two concurrent renames onto one title both
	// see it free and both apply. Only the title field needs it; every other
	// field is per-session state that cannot collide.
	if field == session.FieldTitle {
		regLock, regLockErr := session.AcquireRegistrationLock(profile)
		if regLockErr != nil {
			out.Error(fmt.Sprintf("failed to acquire session registration lock: %v", regLockErr), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		defer regLock.Release()
		freshInstances, freshGroups, reloadErr := reloadForRegistration(storage)
		if reloadErr != nil {
			out.Error(reloadErr.Error(), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		instances, groupsData = freshInstances, freshGroups
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

	// #1853: `session set <id> title` had no collision check on either side of
	// the SetField call, so it could rename a session onto a title another
	// session already holds at the same location — the exact state `add` and
	// `launch` refuse. Two sessions sharing a title at one location are then
	// both unaddressable by title, because ResolveSession can only report
	// ErrCodeAmbiguous. Same predicate and same ALREADY_EXISTS code as `add` and
	// `rename`, naming the existing session's ID so the user can act on it.
	if field == session.FieldTitle {
		if msg, code := checkTitleConflict(instances, inst, value); msg != "" {
			out.Error(msg, code)
			exitCLI(1)
		}
	}
	// #2683: an archived record holding this Claude id wins the duplicate-id
	// guard on save, so the set would print success and then revert.
	if field == session.FieldClaudeSessionID {
		if msg, code := checkArchivedClaudeIDConflict(instances, inst, value); msg != "" {
			out.Error(msg, code)
			exitCLI(1)
		}
	}

	// "order" needs the group tree, which SetField (instance-only by
	// contract) cannot see, so it is handled here like set-parent is.
	if field == "order" {
		n, perr := strconv.Atoi(value)
		if perr != nil || n < 0 {
			out.Error(fmt.Sprintf("invalid order %q: expected a non-negative integer", value), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
		oldPos := strconv.Itoa(groupTree.SessionPosition(inst))
		groupTree.SetSessionOrder(inst, n)
		if err := storage.SaveWithGroups(instances, groupTree); err != nil {
			out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		newPos := strconv.Itoa(groupTree.SessionPosition(inst))
		out.Success(fmt.Sprintf("Updated order: %q -> %q", oldPos, newPos), map[string]interface{}{
			"success":   true,
			"id":        inst.ID,
			"title":     inst.Title,
			"field":     field,
			"old_value": oldPos,
			"new_value": newPos,
		})
		return
	}

	// #924 follow-up: the conversation follows the account. Capture the old
	// account's config dir before SetField mutates resolution.
	var preAccountConfigDir string
	if field == session.FieldAccount && inst.Tool == "claude" {
		preAccountConfigDir = session.GetClaudeConfigDirForInstance(inst)
	}

	// Delegate to session.SetField so CLI and TUI share validation. The
	// extraArgTokens slice carries pre-tokenized argv for extra-args (CLI
	// preserves values with spaces); SetField ignores it for other fields.
	oldValue, postCommit, setErr := session.SetField(inst, field, value, extraArgTokens)
	if setErr != nil {
		out.Error(setErr.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}
	// SetField cleared any earlier intent for an account/model change; record
	// the new one against the resulting account and model.
	if intendedSet {
		inst.MarkIntendedSwitch(reason, *intendedFor, time.Now())
	}
	// #1706: SetField canonicalizes a project path (expand + absolutize), so
	// report what was actually stored rather than the raw argument.
	if field == session.FieldPath {
		value = inst.ProjectPath
	}
	// Custom-tool conversation id: sticky MergeToolDataExtras preserves
	// generic_session_id when a full Save omits the key. CLI does not always
	// register statedb.SetGlobal, so write through the open Storage DB before
	// SaveWithGroups (set and intentional clear).
	if field == session.FieldToolSessionID {
		if err := session.PersistGenericSessionBinding(storage.GetDB(), inst); err != nil {
			out.Error(fmt.Sprintf("failed to persist tool-session-id: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}
	// CLI holds no lock — run tmux side effects inline. TUI defers them
	// until after instancesMu.Unlock.
	if postCommit != nil {
		postCommit()
	}

	// Copy the conversation into the new account's config dir so the
	// restart-required switch resumes with full context. Copy-only; a fresh
	// session (no conversation yet) is not an error.
	if preAccountConfigDir != "" {
		targetDir := session.GetClaudeConfigDirForInstance(inst)
		if migrated, merr := session.MigrateConversationFrom(inst, preAccountConfigDir, targetDir); merr != nil {
			if !errors.Is(merr, session.ErrNoConversation) {
				fmt.Fprintf(os.Stderr, "Warning: account set, but conversation not migrated: %v\n", merr)
			}
		} else if migrated != "" && !quietMode && !*jsonOutput {
			fmt.Printf("Conversation migrated to %s\n", migrated)
		}
		// A conductor's channel plugin state follows it (link or point only).
		for _, note := range session.CarryConductorChannelState(inst, preAccountConfigDir, targetDir) {
			fmt.Fprintf(os.Stderr, "Note: %s\n", note)
		}
		// The restart that follows meets the target account's own trust
		// list and onboarding state; seed them (SWFB1018).
		if _, seedErr := session.PreseedClaudeFirstStart(preAccountConfigDir, targetDir, inst.EffectiveWorkingDir(), session.SessionRunsBypass(inst, loadUserConfigOrNil())); seedErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: folder trust pre-seed for the new account failed: %v\n", seedErr)
		}
	}

	// Save
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Output success
	result := map[string]interface{}{
		"success":   true,
		"id":        inst.ID,
		"title":     inst.Title,
		"field":     field,
		"old_value": oldValue,
		"new_value": value,
	}
	if rec := inst.ActiveIntendedSwitchJSON(time.Now()); rec != nil {
		result["intended_switch"] = rec
	}
	out.Success(fmt.Sprintf("Updated %s: %q -> %q", field, oldValue, value), result)

	maybeEmitSessionSetTelegramWarnings(os.Stderr, session.GetClaudeConfigDirForGroup(inst.GroupPath), inst, field)
}

// maybeEmitSessionSetTelegramWarnings is the post-mutation telegram-topology
// hook for `agent-deck session set` (v1.7.22 / #658). Gated to wrapper and
// channels — other fields are silent. claudeCfgDir lets tests inject a temp
// dir without touching the real ~/.claude lookup.
func maybeEmitSessionSetTelegramWarnings(out io.Writer, claudeCfgDir string, inst *session.Instance, field string) {
	if field != "wrapper" && field != "channels" {
		return
	}
	globalTelegramEnabled, _ := readTelegramGloballyEnabled(claudeCfgDir)
	emitTelegramWarnings(out, session.TelegramValidatorInput{
		GlobalEnabled:   globalTelegramEnabled,
		SessionChannels: inst.Channels,
		SessionWrapper:  inst.Wrapper,
	})
}

// findSessionByTmuxAcrossProfiles searches all profiles for a session matching current tmux session
// Returns the instance and the profile it was found in
func findSessionByTmuxAcrossProfiles() (*session.Instance, string) {
	profiles, err := session.ListProfiles()
	if err != nil {
		return nil, ""
	}

	for _, p := range profiles {
		_, instances, _, err := loadSessionData(p)
		if err != nil {
			continue
		}
		if inst := findSessionByTmux(instances); inst != nil {
			return inst, p
		}
	}
	return nil, ""
}

// findSessionByTmux tries to find a session by matching tmux session name or working directory
func findSessionByTmux(instances []*session.Instance) *session.Instance {
	// Get current tmux session name (bounded — see tmuxProbeTimeout)
	output, err := tmuxProbeBounded("display-message", "-p", "#{session_name}\t#{pane_current_path}")
	if err != nil {
		return nil
	}

	parts := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(parts) < 2 {
		return nil
	}

	sessionName := parts[0]
	currentPath := parts[1]

	// Parse agent-deck session name: agentdeck_<title>_<id>
	if strings.HasPrefix(sessionName, "agentdeck_") {
		// Extract title (everything between agentdeck_ and the last _id)
		withoutPrefix := strings.TrimPrefix(sessionName, "agentdeck_")
		lastUnderscore := strings.LastIndex(withoutPrefix, "_")
		if lastUnderscore > 0 {
			title := withoutPrefix[:lastUnderscore]

			// Try to find by title
			for _, inst := range instances {
				if strings.EqualFold(inst.Title, title) {
					return inst
				}
			}

			// Try to find by sanitized title (replace - with space, etc.)
			normalizedTitle := strings.ReplaceAll(title, "-", " ")
			for _, inst := range instances {
				if strings.EqualFold(inst.Title, normalizedTitle) {
					return inst
				}
			}

			// For agentdeck sessions, we have the title - don't fall back to path matching
			// as that could match a different session with same path in another profile
			return nil
		}
	}

	// Try to find by path (only for non-agentdeck tmux sessions). The pane's cwd
	// is a LOCAL path, so only a local session can own it — a remote session's
	// ProjectPath is a placeholder that frequently equals the controller's
	// working directory (#1852 site 4).
	return localSessionForPaneCwd(instances, currentPath)
}

// showTmuxSessionInfo shows information about the current tmux session (unregistered)
func showTmuxSessionInfo(out *CLIOutput, jsonOutput bool) {
	// Get tmux session info (bounded — see tmuxProbeTimeout)
	output, err := tmuxProbeBounded("display-message", "-p",
		"#{session_name}\t#{pane_current_path}\t#{session_created}\t#{window_name}")
	if err != nil {
		out.Error("failed to get tmux session info", ErrCodeNotFound)
		exitCLI(1)
	}

	parts := strings.Split(strings.TrimSpace(string(output)), "\t")
	sessionName := ""
	currentPath := ""
	windowName := ""
	if len(parts) >= 1 {
		sessionName = parts[0]
	}
	if len(parts) >= 2 {
		currentPath = parts[1]
	}
	if len(parts) >= 4 {
		windowName = parts[3]
	}

	// Parse title from session name
	title := sessionName
	idFragment := ""
	if strings.HasPrefix(sessionName, "agentdeck_") {
		withoutPrefix := strings.TrimPrefix(sessionName, "agentdeck_")
		lastUnderscore := strings.LastIndex(withoutPrefix, "_")
		if lastUnderscore > 0 {
			title = withoutPrefix[:lastUnderscore]
			idFragment = withoutPrefix[lastUnderscore+1:]
		}
	}

	jsonData := map[string]interface{}{
		"tmux_session": sessionName,
		"title":        title,
		"path":         currentPath,
		"window":       windowName,
		"registered":   false,
	}
	if idFragment != "" {
		jsonData["id_fragment"] = idFragment
	}

	var sb strings.Builder
	sb.WriteString("⚠ Session not registered in agent-deck\n")
	sb.WriteString(fmt.Sprintf("Tmux:    %s\n", sessionName))
	sb.WriteString(fmt.Sprintf("Title:   %s\n", title))
	if idFragment != "" {
		sb.WriteString(fmt.Sprintf("ID:      %s (stale)\n", idFragment))
	}
	sb.WriteString(fmt.Sprintf("Path:    %s\n", FormatPath(currentPath)))
	if windowName != "" {
		sb.WriteString(fmt.Sprintf("Window:  %s\n", windowName))
	}
	sb.WriteString("\nTo register this session:\n")
	sb.WriteString(fmt.Sprintf("  agent-deck add -t \"%s\" -g <group> -c claude %s\n", title, currentPath))

	out.Print(sb.String(), jsonData)
}

// handleSessionSetParent links a session as a sub-session of another
func handleSessionSetParent(profile string, args []string) {
	fs := flag.NewFlagSet("session set-parent", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	// #786: post-hoc set-parent must not silently rewrite the child's
	// group. Inheritance is opt-in via this flag.
	inheritGroup := fs.Bool("inherit-group", false,
		"Also rewrite child's group to match parent's (off by default; #786)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session set-parent <session> <parent> [--inherit-group]")
		fmt.Println()
		fmt.Println("Link a session as a sub-session of another session.")
		fmt.Println("The session's group is preserved by default; pass --inherit-group")
		fmt.Println("to also adopt the parent's group.")
		fmt.Println("This works for any session, including those created with --no-parent.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	if fs.NArg() < 2 {
		fs.Usage()
		exitCLI(1)
	}

	sessionID := fs.Arg(0)
	parentID := fs.Arg(1)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	// Resolve the session to be linked
	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		exitCLI(2)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Resolve the parent session
	parentInst, errMsg, errCode := ResolveSession(parentID, instances)
	if parentInst == nil {
		out.Error(errMsg, errCode)
		exitCLI(2)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Validate: can't set self as parent
	if inst.ID == parentInst.ID {
		out.Error("cannot set session as its own parent", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Validate: parent can't be a sub-session (single level only)
	if parentInst.IsSubSession() {
		out.Error("cannot set parent to a sub-session (single level only)", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Validate: session can't already have sub-sessions
	for _, other := range instances {
		if other.ParentSessionID == inst.ID {
			out.Error(
				fmt.Sprintf("session '%s' already has sub-sessions, cannot become a sub-session", inst.Title),
				ErrCodeInvalidOperation,
			)
			exitCLI(1)
		}
	}

	// Set parent (with project path for --add-dir access). Group is only
	// rewritten on explicit --inherit-group opt-in; see #786.
	inst.SetParentWithPath(parentInst.ID, parentInst.ProjectPath)
	if *inheritGroup {
		inst.GroupPath = parentInst.GroupPath
	}

	// Save
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	out.Success(fmt.Sprintf("Linked '%s' as sub-session of '%s'", inst.Title, parentInst.Title), map[string]interface{}{
		"success":         true,
		"session_id":      inst.ID,
		"session_title":   inst.Title,
		"parent_id":       parentInst.ID,
		"parent_title":    parentInst.Title,
		"group":           inst.GroupPath,
		"group_inherited": *inheritGroup,
	})
}

// resolveSessionUpdateAlias maps `session update <id>` invocations with
// CRUD-style flags onto the existing canonical handlers. Returns the
// canonical verb (`unset-parent` or `set-parent`) and the rewritten args
// that handler expects.
//
// Issue #974: `session update <id> --no-parent` should behave the same as
// `session unset-parent <id>`; `session update <id> --parent <pid>` should
// behave the same as `session set-parent <id> <pid>`. If neither flag is
// present we route to the generic `set` handler so the verb stays useful
// for other field updates.
//
// Pure function — no I/O, safe to unit test.
func resolveSessionUpdateAlias(args []string) (canonical string, newArgs []string) {
	hasNoParent := false
	hasParent := false
	parentVal := ""
	filtered := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--no-parent" || a == "-no-parent":
			hasNoParent = true
		case a == "--parent" || a == "-parent":
			if i+1 < len(args) {
				parentVal = args[i+1]
				i++
			}
			hasParent = true
		case strings.HasPrefix(a, "--parent="):
			parentVal = strings.TrimPrefix(a, "--parent=")
			hasParent = true
		case strings.HasPrefix(a, "-parent="):
			parentVal = strings.TrimPrefix(a, "-parent=")
			hasParent = true
		default:
			filtered = append(filtered, a)
		}
	}

	switch {
	case hasNoParent:
		// `set-parent` and `--no-parent` together is contradictory; prefer
		// the explicit detach (`--no-parent`) — matches the user's stated
		// intent in the issue reproducer.
		return "unset-parent", filtered
	case hasParent:
		return "set-parent", append(filtered, parentVal)
	default:
		return "set", filtered
	}
}

// handleSessionUpdate dispatches `session update <id> [flags]` to the
// appropriate canonical handler. See resolveSessionUpdateAlias for the
// mapping rationale.
func handleSessionUpdate(profile string, args []string) {
	canonical, rewritten := resolveSessionUpdateAlias(args)
	switch canonical {
	case "unset-parent":
		handleSessionUnsetParent(profile, rewritten)
	case "set-parent":
		handleSessionSetParent(profile, rewritten)
	default:
		handleSessionSet(profile, rewritten)
	}
}

// handleSessionUnsetParent removes the sub-session link
func handleSessionUnsetParent(profile string, args []string) {
	fs := flag.NewFlagSet("session unset-parent", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session unset-parent <session>")
		fmt.Println()
		fmt.Println("Remove the sub-session link from a session.")
		fmt.Println("The session will remain in its current group.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	if fs.NArg() < 1 {
		fs.Usage()
		exitCLI(1)
	}

	sessionID := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Load sessions
	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	// Resolve the session
	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		exitCLI(2)
		return // unreachable, satisfies staticcheck SA5011
	}

	// Check if it's actually a sub-session
	if !inst.IsSubSession() {
		out.Error(fmt.Sprintf("session '%s' is not a sub-session", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Get parent title for output
	var parentTitle string
	for _, other := range instances {
		if other.ID == inst.ParentSessionID {
			parentTitle = other.Title
			break
		}
	}

	// Clear parent
	inst.ClearParent()

	// Save
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	out.Success(
		fmt.Sprintf("Removed sub-session link from '%s' (was linked to '%s')", inst.Title, parentTitle),
		map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"former_parent": parentTitle,
		},
	)
}

// handleSessionSetTransitionNotify enables or disables transition notifications for a session
func handleSessionSetTransitionNotify(profile string, args []string) {
	fs := flag.NewFlagSet("session set-transition-notify", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session set-transition-notify <session> <on|off>")
		fmt.Println()
		fmt.Println("Enable or disable transition event notifications for a session.")
		fmt.Println("When off, the transition daemon will not send tmux messages to the")
		fmt.Println("parent session when this session changes status (e.g., running → waiting).")
		fmt.Println("This does not affect the parent link itself.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session set-transition-notify worker off")
		fmt.Println("  agent-deck session set-transition-notify worker on")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	if fs.NArg() < 2 {
		fs.Usage()
		exitCLI(1)
	}

	sessionID := fs.Arg(0)
	value := strings.ToLower(strings.TrimSpace(fs.Arg(1)))
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	var suppress bool
	switch value {
	case "on":
		suppress = false
	case "off":
		suppress = true
	default:
		out.Error(fmt.Sprintf("invalid value %q: must be 'on' or 'off'", value), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		exitCLI(2)
		return
	}

	inst.NoTransitionNotify = suppress

	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	stateStr := "on"
	if suppress {
		stateStr = "off"
	}
	out.Success(fmt.Sprintf("Transition notifications for '%s': %s", inst.Title, stateStr), map[string]interface{}{
		"success":              true,
		"session_id":           inst.ID,
		"session_title":        inst.Title,
		"no_transition_notify": suppress,
	})
}

// handleSessionSetTitleLock toggles Instance.TitleLocked (#697). When on, the
// claude-hook name-sync path (applyClaudeTitleSync) is a no-op for this
// session, preserving the conductor-assigned title across Claude renames.
func handleSessionSetTitleLock(profile string, args []string) {
	fs := flag.NewFlagSet("session set-title-lock", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session set-title-lock <session> <on|off|true|false>")
		fmt.Println()
		fmt.Println("Lock or unlock a session's title from Claude session-name sync (#697).")
		fmt.Println("When locked, Claude's --name / /rename will not overwrite the")
		fmt.Println("agent-deck title. Conductors rely on this so semantic titles like")
		fmt.Println("'SCRUM-351' survive Claude's auto-generated summaries.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session set-title-lock SCRUM-351 on")
		fmt.Println("  agent-deck session set-title-lock SCRUM-351 off")
		fmt.Println("  agent-deck session set-title-lock worker true")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	if fs.NArg() < 2 {
		fs.Usage()
		exitCLI(1)
	}

	sessionID := fs.Arg(0)
	value := strings.ToLower(strings.TrimSpace(fs.Arg(1)))
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	var locked bool
	switch value {
	case "on", "true", "1", "yes":
		locked = true
	case "off", "false", "0", "no":
		locked = false
	default:
		out.Error(fmt.Sprintf("invalid value %q: must be 'on' or 'off' (also true/false/1/0)", value), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	storage, instances, groupsData, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	inst, errMsg, errCode := ResolveSession(sessionID, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		exitCLI(2)
		return
	}

	inst.TitleLocked = locked

	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		out.Error(fmt.Sprintf("failed to save: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	stateStr := "off"
	if locked {
		stateStr = "on"
	}
	out.Success(fmt.Sprintf("Title lock for '%s': %s", inst.Title, stateStr), map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"title_locked":  locked,
	})
}

// isValidSessionColor is a thin delegator to session.IsValidSessionColor
// (issue #391). The validator now lives in the session package so the TUI
// EditSessionDialog and CLI session_set share one source of truth; this
// wrapper stays so cmd-package callers and the existing
// TestIsValidSessionColor table in session_color_test.go keep working.
func isValidSessionColor(v string) bool {
	return session.IsValidSessionColor(v)
}
