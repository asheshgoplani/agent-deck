package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// handleSessionOutput gets the last response from a session
func handleSessionOutput(profile string, args []string) {
	fs := flag.NewFlagSet("session output", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	copyFlag := fs.Bool("copy", false, "Copy output to system clipboard")
	// #1101: --pane returns tmux capture-pane content instead of the parsed transcript "last
	// response". The local TUI preview uses capture-pane; remote sessions
	// fetched via SSH need this same content to render claude-formatted output.
	paneFlag := fs.Bool("pane", false, "Return tmux capture-pane content (ANSI stripped in default text mode)")
	primaryPane := fs.Bool("primary", false, "With --pane, capture the managed first window")
	maxTokens := fs.Int("max-tokens", defaultOutputMaxTokens, "Maximum default text-output budget in approximate tokens (head+tail; full text retained on disk)")
	// #2481: conditional read for pollers. With the content_version of an
	// earlier --json read, an unchanged transcript answers {"unchanged":true}
	// from one stat: no transcript parse, no content, no read-log entry.
	ifVersion := fs.String("if-version", "", "With --json: answer {\"unchanged\":true} without content when the response source still has this content_version")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session output [id|title] [options]")
		fmt.Println()
		fmt.Println("Get the last response from a session. If no ID is provided, auto-detects current session.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Printf("Output budget: default text output (also with --pane) strips ANSI escapes and is capped at\n"+
			"--max-tokens (default %d, about %d bytes per token). Longer output keeps its beginning and end\n"+
			"around an explicit \"output omitted\" seam and ends with the path of the full output retained on\n"+
			"disk. --json, -q/--quiet and --copy always carry the complete, unstripped source.\n",
			defaultOutputMaxTokens, outputBytesPerToken)
		fmt.Println()
		fmt.Println("Change detection: --json carries content_version when the response is parsed from one\n" +
			"transcript file. Pass it back with --if-version to get {\"unchanged\":true} (no content, not a\n" +
			"logged read) while that file is unchanged; any change returns the full response and a new version.")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	if *maxTokens <= 0 {
		fmt.Fprintln(os.Stderr, "Error: --max-tokens must be greater than zero")
		exitCLI(1)
	}

	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	boundAgentOutput := shouldBoundAgentOutput(*jsonOutput, quietMode, *copyFlag)
	out := NewCLIOutput(*jsonOutput, quietMode)
	if *primaryPane && !*paneFlag {
		out.Error("--primary requires --pane", ErrCodeInvalidOperation)
		exitCLI(1)
	}
	if *ifVersion != "" && (!*jsonOutput || quietMode || *paneFlag || *copyFlag) {
		out.Error("--if-version requires --json and cannot be combined with -q, --pane or --copy", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Load sessions
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		exitCLI(1)
	}
	// The read log names the effective profile so an empty -p (env or
	// default profile) does not record as "".
	profile = storage.Profile()

	// Resolve session (allow current session detection)
	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}
	// Comms Ledger measurement: a session re-reading another one is the
	// call a ledger-fed parent should not need (#2482 target).
	if caller := callerSessionID(); caller != "" && caller != inst.ID {
		session.SpoolCommsCall(caller, comms.CallSessionOutput, inst.ID)
	}

	// Refresh session ID from tmux env before reading output.
	// The DB-stored ClaudeSessionID may be stale if /clear created a new session
	// or PostStartSync timed out. This matches the refresh in handleSessionSend.
	if session.IsClaudeCompatible(inst.Tool) {
		if freshID := inst.GetSessionIDFromTmux(); freshID != "" {
			inst.ClaudeSessionID = freshID
			// #1815: own pane env — weak vouch.
			session.NoteClaudeSessionIDFromOwnPane(inst)
			inst.ClaudeDetectedAt = time.Now()
		}
	}

	// #1101: --pane short-circuits the transcript path and returns the live
	// tmux pane capture so remote previews can render the same claude-formatted
	// content the local preview shows. We still emit a ResponseOutput-shaped
	// JSON so the wire format is unchanged.
	if *paneFlag {
		var paneContent string
		var paneErr error
		if *primaryPane {
			paneContent, paneErr = inst.PreviewPrimaryFull()
		} else {
			paneContent, paneErr = inst.PreviewFull()
		}
		if paneErr != nil {
			out.Error(fmt.Sprintf("failed to capture pane: %v", paneErr), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		emitted := boundSessionOutputOrExit(out, profile, inst.ID, "pane", paneContent, *maxTokens, boundAgentOutput)
		jsonData := map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"tool":          inst.Tool,
			"role":          "pane",
			"content":       emitted,
		}
		if quietMode {
			fmt.Println(emitted)
			return
		}
		out.Print(emitted, jsonData)
		return
	}

	// A Codex row launch left unbound reads its exact rollout once the live
	// process names the thread, instead of falling back to pane text (#2396).
	adoptLiveCodexIdentity(storage, inst)

	// Get the last response (best-effort fallback for smoother CLI reads).
	// Collision-checked (#1400): multiple live instances sharing one
	// claude_session_id resolve to the SAME transcript, so the parsed "last
	// response" (-q / --json / default / --copy) would be byte-identical for
	// all of them. Refuse the read instead — the same guard `session output
	// --stream` got in #1352. A colliding transcript has no content version,
	// so the #2481 stat-only shortcut below never bypasses that guard.
	version := inst.ResponseContentVersion(instances)
	if version != "" && version == *ifVersion {
		out.Print("unchanged", map[string]interface{}{
			"success":         true,
			"session_id":      inst.ID,
			"session_title":   inst.Title,
			"tool":            inst.Tool,
			"unchanged":       true,
			"content_version": version,
		})
		return
	}
	response, versioned, err := inst.GetLastResponseAtVersion(instances, version)
	if err != nil {
		out.Error(fmt.Sprintf("failed to get response: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	bounded := boundSessionOutputOrExit(out, profile, inst.ID, "response", response.Content, *maxTokens, boundAgentOutput)
	if resultReadShown(*jsonOutput, quietMode, *copyFlag, bounded == response.Content) {
		noteResultRead(inst.ID, response.AssistantUUID)
	}

	// Copy to clipboard mode
	if *copyFlag {
		termInfo := tmux.GetTerminalInfo()
		result, err := clipboard.Copy(response.Content, termInfo.SupportsOSC52)
		if err != nil {
			out.Error(fmt.Sprintf("clipboard: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		jsonData := map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"lines_copied":  result.LineCount,
			"bytes_copied":  result.ByteSize,
			"method":        result.Method,
		}
		out.Print(
			fmt.Sprintf("Copied %d lines to clipboard via %s (%s)", result.LineCount, result.Method, inst.Title),
			jsonData,
		)
		return
	}
	response.Content = bounded

	// Quiet mode: just print raw content
	if quietMode {
		fmt.Println(response.Content)
		return
	}

	// Build JSON data with tool-specific conversation session ID key
	jsonData := map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"tool":          response.Tool,
		"role":          response.Role,
		"content":       response.Content,
		"timestamp":     response.Timestamp,
	}
	if response.CodexTurnGeneration != "" {
		jsonData["codex_turn_generation"] = response.CodexTurnGeneration
	}
	if versioned {
		jsonData["content_version"] = version
	}
	// Add tool-specific conversation session ID
	if response.SessionID != "" {
		switch response.Tool {
		case "claude":
			jsonData["claude_session_id"] = response.SessionID
		case "gemini":
			jsonData["gemini_session_id"] = response.SessionID
		default:
			jsonData["conversation_id"] = response.SessionID
		}
	}

	// Build human-readable output
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session: %s (%s)\n", inst.Title, response.Tool))
	if response.Timestamp != "" {
		sb.WriteString(fmt.Sprintf("Time: %s\n", response.Timestamp))
	}
	sb.WriteString("---\n")
	sb.WriteString(response.Content)

	out.Print(sb.String(), jsonData)
}

// handleSessionCurrent shows current session and profile (auto-detected)
// Uses a fast path that reads session data without tmux initialization (LoadLite).
func handleSessionCurrent(profileArg string, args []string) {
	fs := flag.NewFlagSet("session current", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session current [options]")
		fmt.Println()
		fmt.Println("Show current session and profile (auto-detected from environment).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Check if we're in a tmux session
	if os.Getenv("TMUX") == "" {
		out.Error("not in a tmux session", ErrCodeNotFound)
		exitCLI(1)
	}

	// ═══════════════════════════════════════════════════════════════════
	// FAST PATH: Get current tmux session name (1 subprocess call)
	// Then match against session data without full tmux initialization
	// ═══════════════════════════════════════════════════════════════════
	tmuxSessionName, err := getCurrentTmuxSessionName()
	if err != nil {
		out.Error(fmt.Sprintf("failed to get current tmux session: %v", err), ErrCodeNotFound)
		exitCLI(1)
	}

	// Detect profile: use explicit arg if provided, otherwise auto-detect.
	// #1790/#1822: route the auto-detect path through ResolveProfileForStorage,
	// not the bare GetEffectiveProfile(""). The
	// result below is handed straight to findInstanceDataByTmuxFast, which
	// opens/creates storage for it (NewStorageWithProfile) — a bare
	// GetEffectiveProfile result would look like an explicit -p selection to
	// that call's own guard, bypassing it a second hop downstream, the same
	// class of bug fixed at the other call sites.
	detectedProfile := profileArg
	if detectedProfile == "" || detectedProfile == session.DefaultProfile {
		resolved, err := session.ResolveProfileForStorage("")
		if err != nil {
			out.Error(fmt.Sprintf("failed to resolve profile: %v", err), ErrCodeNotFound)
			exitCLI(1)
		}
		detectedProfile = resolved
	}

	// Try fast path: LoadLite + match by tmux session name
	instData, foundProfile := findInstanceDataByTmuxFast(tmuxSessionName, detectedProfile)

	if instData == nil {
		out.Error(
			"current tmux session is not an agent-deck session\nHint: Run 'agent-deck list' to see available sessions",
			ErrCodeNotFound,
		)
		exitCLI(1)
	}

	if foundProfile != "" {
		detectedProfile = foundProfile
	}

	// Quiet mode: just print session name
	if quietMode {
		fmt.Println(instData.Title)
		return
	}

	// Determine status from saved data (no live tmux check in fast path)
	status := StatusString(instData.Status)

	// Prepare JSON output
	// The JSON form is the machine-readable identity a session fetches from
	// inside (the injected identity block points here), so it carries the
	// full record: tool, account, parent and the identity file, not just the
	// human summary fields.
	// account and parent_session_id are always present (empty when unset)
	// so a caller can key on them without probing for absence.
	jsonData := map[string]interface{}{
		"session":           instData.Title,
		"title":             instData.Title,
		"profile":           detectedProfile,
		"id":                instData.ID,
		"path":              instData.ProjectPath,
		"status":            status,
		"tool":              instData.Tool,
		"account":           instData.Account,
		"parent_session_id": instData.ParentSessionID,
	}

	if instData.TmuxSession != "" {
		jsonData["tmux_session"] = instData.TmuxSession
	}

	if instData.GroupPath != "" {
		jsonData["group"] = instData.GroupPath
	}
	if instData.IsConductor {
		jsonData["is_conductor"] = true
	}
	if instData.WorktreeBranch != "" {
		jsonData["worktree_branch"] = instData.WorktreeBranch
	}
	if identityFile := os.Getenv(session.IdentityFileEnv); identityFile != "" {
		jsonData["identity_file"] = identityFile
	}

	// Build human-readable output
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session: %s\n", instData.Title))
	sb.WriteString(fmt.Sprintf("Profile: %s\n", detectedProfile))
	sb.WriteString(fmt.Sprintf("ID:      %s\n", instData.ID))
	sb.WriteString(fmt.Sprintf("Status:  %s %s\n", StatusSymbol(instData.Status), status))
	sb.WriteString(fmt.Sprintf("Path:    %s\n", FormatPath(instData.ProjectPath)))
	if instData.GroupPath != "" {
		sb.WriteString(fmt.Sprintf("Group:   %s\n", instData.GroupPath))
	}
	if instData.Tool != "" {
		sb.WriteString(fmt.Sprintf("Tool:    %s\n", instData.Tool))
	}
	if instData.Account != "" {
		sb.WriteString(fmt.Sprintf("Account: %s\n", instData.Account))
	}
	if instData.ParentSessionID != "" {
		sb.WriteString(fmt.Sprintf("Parent:  %s\n", instData.ParentSessionID))
	}

	out.Print(sb.String(), jsonData)
}

// handleSessionPrimer implements `agent-deck session primer [id] [--json]`
// (issue #2260): inspects the resolved context-level for a session
// (global < group < session precedence) and prints exactly the primer/
// identity text that session's harness receives, or would receive on its
// next start/restart — without inventing values for facts that aren't
// available (SSH/sandboxed sessions never inject; an unset field renders as
// the same "(none)" placeholder BuildIdentityPrompt/BuildPrimerPrompt use).
func handleSessionPrimer(profileArg string, args []string) {
	fs := flag.NewFlagSet("session primer", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session primer [id] [options]")
		fmt.Println()
		fmt.Println("Show the resolved context-level and the exact primer/identity text a session's harness receives (issue #2260).")
		fmt.Println("Precedence: global ([launch].context_level) < group ([groups.\"<path>\"].context_level) < session (`session set <id> context-level`).")
		fmt.Println("If no id is given, auto-detects the current session (like `session current`).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session primer                  # current session")
		fmt.Println("  agent-deck session primer my-project")
		fmt.Println("  agent-deck session primer my-project --json")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}

	out := NewCLIOutput(*jsonOutput, false)

	identifier := ""
	if fs.NArg() > 0 {
		identifier = fs.Arg(0)
	}

	_, instances, _, err := loadSessionData(profileArg)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	level, source := inst.EffectiveContextLevel()
	active := inst.ContextInjectionActive()

	skipReason := ""
	if level == session.ContextLevelNone {
		skipReason = "context level is none: no text is generated"
	}

	text := ""
	if active {
		text = inst.BuildContextPromptForLevel(level)
	}

	identityFile, _ := inst.IdentityFilePath()
	delivery := "file"
	if inst.IdentityRunsElsewhere() {
		// SSH/sandbox: the block rides inline in the launch command and the
		// file is written where the harness runs, so no path on this host.
		identityFile = ""
		delivery = "inline (file written where the harness runs: $XDG_RUNTIME_DIR/agent-deck/identity/<id>/identity.md, else ~/.local/share/agent-deck/identity/<id>/identity.md)"
	}

	jsonData := map[string]interface{}{
		"id":            inst.ID,
		"title":         inst.Title,
		"context_level": level,
		"source":        source,
		"active":        active,
		"identity_file": identityFile,
		"delivery":      delivery,
		"text":          text,
	}
	if skipReason != "" {
		jsonData["skip_reason"] = skipReason
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Session:       %s (%s)\n", inst.Title, inst.ID)
	fmt.Fprintf(&sb, "Context level: %s\n", level)
	fmt.Fprintf(&sb, "Source:        %s\n", source)
	fmt.Fprintf(&sb, "Identity file: %s\n", identityFile)
	fmt.Fprintf(&sb, "Delivery:      %s\n", delivery)
	if skipReason != "" {
		fmt.Fprintf(&sb, "Skipped:       %s\n", skipReason)
	}
	sb.WriteString("\n")
	if text == "" {
		sb.WriteString("(no primer/identity text)\n")
	} else {
		sb.WriteString(text)
	}

	out.Print(sb.String(), jsonData)
}

// remotePrimerUnsupported turns an older remote's "unknown session command"
// reply to `session primer` into one clear line, the same pattern as
// remoteMetricsUnsupported. Any other failure passes through as the remote
// printed it.
func remotePrimerUnsupported(remote string, args []string, code int, stderr string) (string, bool) {
	if code == 0 || !isSessionPrimerArgs(args) || !strings.Contains(stderr, "unknown session command: primer") {
		return "", false
	}
	return fmt.Sprintf("remote %q does not support 'session primer' (its agent-deck predates session primer; update it with 'agent-deck remote %s update')", remote, remote), true
}

func isSessionPrimerArgs(args []string) bool {
	return len(args) > 1 && args[0] == "session" && args[1] == "primer"
}
