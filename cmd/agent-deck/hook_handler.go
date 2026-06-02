package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

var hookHandlerLog = logging.ForComponent(logging.CompSession)

// maxHookPayloadSize limits the size of JSON payloads read from stdin
// to prevent denial-of-service via oversized input.
const maxHookPayloadSize = 1 << 20 // 1 MB

// validInstanceID matches UUID-style instance IDs to prevent path traversal.
var validInstanceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// hookPayload represents the JSON payload Claude Code sends to hooks via stdin.
// Only the fields we need are decoded; unknown fields are ignored.
type hookPayload struct {
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	Source        string          `json:"source"`
	Matcher       json.RawMessage `json:"matcher,omitempty"`
	// Cwd is the session's working directory (PROJECT_DIR) as reported by
	// Claude Code on each hook event. Issue #1233: when a running session's
	// registered worktree is renamed/removed, this points at a path that no
	// longer exists; we use it to degrade gracefully rather than erroring on
	// every tool call. Empty when the agent doesn't send a cwd (older Claude
	// Code) — treated as "present" so behavior is unchanged.
	Cwd string `json:"cwd"`
	// StopHookActive is Claude Code's flag: true when this Stop is a
	// continuation induced by a previous Stop-hook block. Issue #1225 uses it
	// to bound consecutive inbox-drain blocks so the conductor cannot loop
	// forever (resets the budget on a genuine user turn boundary).
	//
	// Audit B8: a *bool (not bool) so we can distinguish ABSENT from explicit
	// false. A missing field must NOT be read as "fresh user turn" (which would
	// reset the loop guard every Stop); resolveStopHookActive fails safe to true.
	StopHookActive *bool `json:"stop_hook_active"`
}

// resolveStopHookActive fails safe (audit B8): an absent stop_hook_active is
// treated as active=true (this Stop counts against the MaxStopHookBlocks budget)
// rather than false (which would reset the budget). Only an EXPLICIT false — a
// genuine user turn boundary that Claude Code is asserting — resets the guard.
// This keeps the loop bounded even if Claude Code ever omits the field.
func resolveStopHookActive(p hookPayload) bool {
	return p.StopHookActive == nil || *p.StopHookActive
}

// hookStatusFile is the JSON written to ~/.agent-deck/hooks/{instance_id}.json
type hookStatusFile struct {
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
	Event     string `json:"event"`
	Timestamp int64  `json:"ts"`
	// DoneStatus/DoneSummary carry a worker-printed completion sentinel
	// detected on the Stop edge (issue #1186). omitempty so ordinary Stops
	// (no sentinel) leave the fields absent, which the daemon reads as
	// "no finished event to emit."
	DoneStatus  string `json:"done_status,omitempty"`
	DoneSummary string `json:"done_summary,omitempty"`
}

// mapEventToStatus maps a Claude Code hook event to an agent-deck status string.
// Status semantics in agent-deck:
//   - "running" = Claude is actively processing (green)
//   - "waiting" = Claude is at the prompt, waiting for user input (orange)
//   - "dead"    = Session ended
//
// Gemini mappings:
//   - "BeforeAgent" = running
//   - "AfterAgent"  = waiting
func mapEventToStatus(event string) string {
	switch event {
	case "SessionStart":
		return "waiting" // Claude at initial prompt, waiting for user input
	case "BeforeAgent":
		return "running" // Gemini received user input and is processing
	case "AfterAgent":
		return "waiting" // Gemini completed response, back to waiting
	// Hermes shell hook events
	case "pre_tool_call":
		return "running" // Hermes is executing a tool call
	case "post_tool_call":
		return "waiting" // Hermes finished a tool call, back at prompt
	case "on_session_start":
		return "waiting" // Hermes session started, waiting for first prompt
	case "on_session_end":
		return "dead" // Hermes session ended
	case "UserPromptSubmit":
		return "running" // User sent prompt, Claude is processing
	case "Stop":
		return "waiting" // Claude finished, back at prompt waiting for user
	case "PermissionRequest":
		return "waiting" // Claude needs permission approval
	case "Notification":
		// Notification events with permission_prompt|elicitation_dialog matcher
		// are mapped to "waiting" by the caller after checking the matcher.
		// Default notification is informational, treat as no status change.
		return ""
	case "SessionEnd":
		return "dead"
	case "PreCompact":
		return "" // Observability only; context-% monitoring handles /clear proactively
	default:
		return ""
	}
}

// handleHookHandler processes a Claude Code hook event.
// Reads JSON from stdin, maps the event to a status, and writes a status file.
// Always exits 0 to avoid blocking Claude Code.
func handleHookHandler() {
	instanceID := os.Getenv("AGENTDECK_INSTANCE_ID")
	if instanceID == "" {
		// No instance ID means this Claude session isn't managed by agent-deck.
		// Exit silently without error.
		return
	}

	// Validate instance ID to prevent path traversal via crafted env vars.
	if !validInstanceID.MatchString(instanceID) || strings.Contains(instanceID, "..") {
		return
	}

	// Read stdin with size limit to prevent DoS via oversized payloads.
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxHookPayloadSize))
	if err != nil || len(data) == 0 {
		return
	}

	var payload hookPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}

	// Issue #1233: gracefully degrade when the session's working directory
	// (PROJECT_DIR / cwd) has been renamed or removed out from under a running
	// session — e.g. a git worktree renamed while the session is live. Rather
	// than emitting a FATAL-class error on every single tool call, log a single
	// WARN (deduped per instance+path) that points at the moved path and
	// suggests `agent-deck session move`, then soft-skip this invocation.
	if projectDirMissing(payload.Cwd) {
		warnProjectDirMissingOnce(instanceID, payload.Cwd)
		return
	}

	// Map event to status
	status := mapEventToStatus(payload.HookEventName)

	// Special handling for Notification events: only map to "waiting" if
	// the matcher indicates a permission prompt or elicitation dialog
	if payload.HookEventName == "Notification" && payload.Matcher != nil {
		var matcher string
		if err := json.Unmarshal(payload.Matcher, &matcher); err == nil {
			if matcher == "permission_prompt" || matcher == "elicitation_dialog" {
				status = "waiting"
			}
		}
	}

	if status == "" {
		// Unknown or unhandled event, nothing to write
		return
	}

	// Issue #1186: on the Stop edge — the completion edge — scan the transcript
	// tail for a worker-printed completion sentinel. When present, persist the
	// parsed outcome into the hook status file so the daemon can emit a
	// distinct "finished" event to the parent instead of the conductor having
	// to poll artifacts. Absent on ordinary mid-task Stops, so the existing
	// "waiting" behavior is unchanged.
	if payload.HookEventName == "Stop" {
		if sig, ok := detectDoneSentinel(data); ok {
			writeHookStatus(instanceID, status, payload.SessionID, payload.HookEventName, sig)
		} else {
			writeHookStatus(instanceID, status, payload.SessionID, payload.HookEventName)
		}
	} else {
		writeHookStatus(instanceID, status, payload.SessionID, payload.HookEventName)
	}

	// #572: Sync agent-deck title from Claude Code's --name / /rename value.
	// Event-driven so user-facing rename lands within one hook tick; silent
	// no-op when no name is set (sessions started without --name keep the
	// existing agent-deck adjective-noun title).
	applyClaudeTitleSync(instanceID, payload.SessionID)

	// Write cost event if this hook contains usage data
	logCostDebug("hook event=%s instance=%s status=%s", payload.HookEventName, instanceID, status)
	writeCostEvent(instanceID, data)

	// PermissionRequest in DSP-launched, agent-deck-managed sessions: emit an
	// explicit allow decision so headless / /remote-control contexts (which
	// have no UI fallback) do not silently deny. DSP is the user-declared
	// trust signal; the hook just makes that declaration consistent across
	// interactive and non-interactive Claude UIs. Without this, a sync hook
	// that exits with no decision falls through to Claude Code's default,
	// which denies in UI-less contexts. Status-tracking behavior above is
	// unchanged.
	if payload.HookEventName == "PermissionRequest" && parentIsDSP() {
		fmt.Println(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","permissionDecision":"allow"}}`)
	}

	// Issue #1225: on the Stop edge (the turn boundary), a parent drains its
	// durable per-parent outbox and injects any pending child completions via
	// {decision:"block",reason} — so a BUSY conductor still receives every
	// completion at its very next free turn, with zero forced interrupts and
	// zero loss. No-op when the inbox is empty (the common case for non-parent
	// sessions), and bounded by a max-consecutive-block guard so it can't loop.
	//
	// NOTE: Claude Code only reads this decision when the Stop hook runs
	// SYNCHRONOUSLY. The install flips the conductor's Stop hook to sync — see
	// the maintainer note in the PR. Emitting here is harmless under the legacy
	// async install (Claude ignores stdout) and activates once sync lands.
	if payload.HookEventName == "Stop" {
		if dec, blocked, derr := session.DrainForStopHook(instanceID, resolveStopHookActive(payload)); derr == nil && blocked {
			if out, mErr := json.Marshal(dec); mErr == nil {
				fmt.Println(string(out))
			}
		}
	}
}

// parentIsDSP reports whether the parent process (typically the claude binary)
// was launched with --dangerously-skip-permissions. Returns true if the
// AGENTDECK_DSP_MODE env var is explicitly set, or, on Linux/WSL, if the
// parent's /proc/<ppid>/cmdline contains the DSP flag. Returns false on
// non-Linux platforms unless AGENTDECK_DSP_MODE is set, since /proc is
// unavailable; agent-deck launch paths can opt those platforms in via the
// env var when needed.
func parentIsDSP() bool {
	if os.Getenv("AGENTDECK_DSP_MODE") == "1" {
		return true
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", os.Getppid()))
	if err != nil {
		return false
	}
	return strings.Contains(string(cmdline), "--dangerously-skip-permissions")
}

// writeHookStatus writes a hook status file atomically for one instance.
// The optional done argument carries a completion sentinel (issue #1186);
// when supplied its status/summary are persisted alongside the hook status.
func writeHookStatus(instanceID, status, sessionID, event string, done ...session.DoneSignal) {
	if instanceID == "" || status == "" {
		return
	}

	hooksDir := getHooksDir()
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		hookHandlerLog.Warn("hook_status_mkdir_failed",
			slog.String("dir", hooksDir),
			slog.String("instance", instanceID),
			slog.String("error", err.Error()),
		)
		return
	}

	sessionID = strings.TrimSpace(sessionID)
	// Preserve legacy hook JSON semantics: empty stays empty.
	// Persist non-empty session IDs in a sidecar, to be used only when reading.
	if sessionID != "" {
		session.WriteHookSessionAnchor(instanceID, sessionID)
	}

	statusFile := hookStatusFile{
		Status:    status,
		SessionID: sessionID,
		Event:     event,
		Timestamp: time.Now().Unix(),
	}
	if len(done) > 0 {
		statusFile.DoneStatus = done[0].Status
		statusFile.DoneSummary = done[0].Summary
	}

	jsonData, err := json.Marshal(statusFile)
	if err != nil {
		hookHandlerLog.Warn("hook_status_marshal_failed",
			slog.String("instance", instanceID),
			slog.String("error", err.Error()),
		)
		return
	}

	filePath := filepath.Join(hooksDir, filepath.Base(instanceID)+".json")
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, jsonData, 0600); err != nil {
		hookHandlerLog.Warn("hook_status_write_failed",
			slog.String("path", tmpPath),
			slog.String("instance", instanceID),
			slog.String("error", err.Error()),
		)
		return
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		hookHandlerLog.Warn("hook_status_rename_failed",
			slog.String("from", tmpPath),
			slog.String("to", filePath),
			slog.String("instance", instanceID),
			slog.String("error", err.Error()),
		)
		// Best-effort cleanup of the orphaned temp file.
		_ = os.Remove(tmpPath)
		return
	}

	// Clear sticky session mapping when the upstream session is explicitly ended.
	if isTerminalHookEvent(event) {
		session.ClearHookSessionAnchor(instanceID)
	}
}

func isTerminalHookEvent(event string) bool {
	norm := strings.ToLower(strings.TrimSpace(event))
	if norm == "" {
		return false
	}
	norm = strings.NewReplacer(".", "", "-", "", "_", "", "/", "", " ", "").Replace(norm)
	// Explicit terminal event allowlist. Keep this narrow to avoid clearing
	// sidecar on ordinary non-terminal "Stop"/turn-complete style events.
	switch norm {
	case "sessionend", "sessionended", "sessionclose", "sessionclosed", "sessiondone", "sessionexit", "sessionexited",
		"onsessionend", // Hermes: on_session_end normalized
		"threadend", "threadended", "threadterminate", "threadterminated", "threadclose", "threadclosed",
		"threaddone", "threadexit", "threadexited":
		return true
	default:
		return false
	}
}

// projectDirMissing reports whether cwd is a non-empty path that no longer
// exists on disk. An empty cwd (older Claude Code, or hook events that omit
// one) returns false — we can't tell, so behavior stays unchanged. Stat errors
// other than "not exist" (e.g. permission) also return false: only a confirmed
// missing directory triggers the degrade path.
func projectDirMissing(cwd string) bool {
	if cwd == "" {
		return false
	}
	_, err := os.Stat(cwd)
	return errors.Is(err, os.ErrNotExist)
}

// warnProjectDirMissingOnce logs a single WARN for a missing project dir and
// records a marker so subsequent hook invocations for the same instance+path
// stay silent. Because each hook runs as a fresh process, the "once" guard is
// an on-disk marker (next to the hook status files) whose contents are the
// missing path: if the session is later repointed to a different (also-missing)
// path, the mismatch lets it warn again instead of being silenced by a stale
// marker.
func warnProjectDirMissingOnce(instanceID, cwd string) {
	hooksDir := getHooksDir()
	markerPath := filepath.Join(hooksDir, filepath.Base(instanceID)+".projectdir-missing")

	if existing, err := os.ReadFile(markerPath); err == nil && strings.TrimSpace(string(existing)) == cwd {
		return // already warned for this exact missing path
	}

	hookHandlerLog.Warn("hook_projectdir_missing",
		slog.String("instance", instanceID),
		slog.String("project_dir", cwd),
		slog.String("suggestion", "run `agent-deck session move <id|title> <new-path>` to repoint the session at its moved worktree"),
	)

	if err := os.MkdirAll(hooksDir, 0o700); err == nil {
		_ = os.WriteFile(markerPath, []byte(cwd), 0o600)
	}
}

// getHooksDir returns the path to the hooks status directory.
func getHooksDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "hooks")
	}
	return filepath.Join(home, ".agent-deck", "hooks")
}

// cleanStaleHookFiles removes hook status files older than 24 hours.
func cleanStaleHookFiles() {
	hooksDir := getHooksDir()
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if entry.IsDir() || (ext != ".json" && ext != ".sid") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(hooksDir, entry.Name()))
		}
	}
}

// handleHooks handles the "hooks" CLI subcommand for manual hook management.
func handleHooks(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck hooks <install|uninstall|status>")
		os.Exit(1)
	}

	switch args[0] {
	case "install":
		handleHooksInstall()
	case "uninstall":
		handleHooksUninstall()
	case "status":
		handleHooksStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown hooks subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "Usage: agent-deck hooks <install|uninstall|status>")
		os.Exit(1)
	}
}

func handleHooksInstall() {
	configDir := getClaudeConfigDirForHooks()
	installed, err := session.InjectClaudeHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error installing hooks: %v\n", err)
		os.Exit(1)
	}
	if installed {
		fmt.Println("Claude Code hooks installed successfully.")
		fmt.Printf("Config: %s/settings.json\n", configDir)
	} else {
		fmt.Println("Claude Code hooks are already installed.")
	}
}

func handleHooksUninstall() {
	configDir := getClaudeConfigDirForHooks()
	removed, err := session.RemoveClaudeHooks(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error removing hooks: %v\n", err)
		os.Exit(1)
	}
	if removed {
		fmt.Println("Claude Code hooks removed successfully.")
	} else {
		fmt.Println("No agent-deck hooks found to remove.")
	}
}

func handleHooksStatus() {
	// Clean up stale hook files while checking status
	cleanStaleHookFiles()

	configDir := getClaudeConfigDirForHooks()
	installed := session.CheckClaudeHooksInstalled(configDir)

	if installed {
		fmt.Println("Status: INSTALLED")
		fmt.Printf("Config: %s/settings.json\n", configDir)
	} else {
		fmt.Println("Status: NOT INSTALLED")
		fmt.Println("Run 'agent-deck hooks install' to install.")
	}

	// Show hook status files
	hooksDir := getHooksDir()
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return
	}

	activeCount := 0
	cutoff := time.Now().Add(-5 * time.Second)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			activeCount++
		}
	}

	fmt.Printf("Active hook files: %d (in %s)\n", activeCount, hooksDir)
	fmt.Printf("Total hook files: %d\n", len(entries))
}

// costEventFile is the JSON written to ~/.agent-deck/cost-events/{instance}_{ts}.json
type costEventFile struct {
	InstanceID       string `json:"instance_id"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	Timestamp        int64  `json:"ts"`
}

// stopHookPayload extracts transcript_path from the Stop hook payload.
type stopHookPayload struct {
	HookEventName  string `json:"hook_event_name"`
	TranscriptPath string `json:"transcript_path"`
}

// transcriptMessage is the last line of the transcript JSONL file (assistant turn).
type transcriptMessage struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// writeCostEvent reads usage from the Claude transcript file on Stop events.
func writeCostEvent(instanceID string, rawPayload []byte) {
	logCostDebug("writeCostEvent called for instance=%s", instanceID)

	var stop stopHookPayload
	if err := json.Unmarshal(rawPayload, &stop); err != nil {
		logCostDebug("payload parse error: %v", err)
		return
	}
	if stop.HookEventName != "Stop" {
		logCostDebug("not a Stop event, skipping")
		return
	}
	if stop.TranscriptPath == "" {
		logCostDebug("no transcript_path in Stop payload")
		return
	}

	// Validate transcript path to prevent path traversal.
	// Claude stores transcripts under ~/.claude/projects/{hash}/{session}.jsonl
	cleanPath := filepath.Clean(stop.TranscriptPath)
	if strings.Contains(cleanPath, "..") {
		logCostDebug("rejected transcript_path with path traversal: %s", stop.TranscriptPath)
		return
	}
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		claudeDir := filepath.Join(home, ".claude")
		if !strings.HasPrefix(cleanPath, claudeDir) {
			logCostDebug("rejected transcript_path outside ~/.claude: %s", stop.TranscriptPath)
			return
		}
	}
	logCostDebug("transcript_path: %s", cleanPath)

	lastLine, err := readLastLine(cleanPath)
	if err != nil {
		logCostDebug("read transcript failed: %v", err)
		return
	}

	var msg transcriptMessage
	if err := json.Unmarshal([]byte(lastLine), &msg); err != nil {
		logCostDebug("parse transcript line failed: %v", err)
		return
	}

	if msg.Type != "assistant" {
		logCostDebug("last line type=%s, not assistant", msg.Type)
		return
	}

	usage := msg.Message.Usage
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		logCostDebug("no token usage in transcript")
		return
	}

	logCostDebug("found usage: model=%s in=%d out=%d cache_read=%d cache_write=%d",
		msg.Message.Model, usage.InputTokens, usage.OutputTokens,
		usage.CacheReadInputTokens, usage.CacheCreationInputTokens)

	costDir := getCostEventsDir()
	if err := os.MkdirAll(costDir, 0700); err != nil {
		hookHandlerLog.Warn("cost_event_mkdir_failed",
			slog.String("dir", costDir),
			slog.String("instance", instanceID),
			slog.String("error", err.Error()),
		)
		return
	}

	ts := time.Now().UnixNano()
	cf := costEventFile{
		InstanceID:       instanceID,
		Model:            msg.Message.Model,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadInputTokens,
		CacheWriteTokens: usage.CacheCreationInputTokens,
		Timestamp:        ts,
	}

	jsonData, err := json.Marshal(cf)
	if err != nil {
		hookHandlerLog.Warn("cost_event_marshal_failed",
			slog.String("instance", instanceID),
			slog.String("error", err.Error()),
		)
		return
	}

	filename := fmt.Sprintf("%s_%d.json", instanceID, ts)
	tmpPath := filepath.Join(costDir, filename+".tmp")
	finalPath := filepath.Join(costDir, filename)

	if err := os.WriteFile(tmpPath, jsonData, 0600); err != nil {
		hookHandlerLog.Warn("cost_event_write_failed",
			slog.String("path", tmpPath),
			slog.String("instance", instanceID),
			slog.String("error", err.Error()),
		)
		logCostDebug("write failed: %v", err)
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		hookHandlerLog.Warn("cost_event_rename_failed",
			slog.String("from", tmpPath),
			slog.String("to", finalPath),
			slog.String("instance", instanceID),
			slog.String("error", err.Error()),
		)
		logCostDebug("rename failed: %v", err)
		_ = os.Remove(tmpPath)
		return
	}
	logCostDebug("wrote cost event: %s model=%s in=%d out=%d", finalPath, cf.Model, cf.InputTokens, cf.OutputTokens)
}

// transcriptContentMessage extracts the assistant message content blocks from
// the last transcript line, for completion-sentinel detection (issue #1186).
type transcriptContentMessage struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// detectDoneSentinel parses transcript_path out of a Stop hook payload and
// scans the transcript tail for a worker-printed completion sentinel. It
// applies the same path-traversal / ~/.claude containment guards as the cost
// path so a crafted payload can't read arbitrary files.
func detectDoneSentinel(rawPayload []byte) (session.DoneSignal, bool) {
	var stop stopHookPayload
	if err := json.Unmarshal(rawPayload, &stop); err != nil {
		return session.DoneSignal{}, false
	}
	if stop.TranscriptPath == "" {
		return session.DoneSignal{}, false
	}
	cleanPath := filepath.Clean(stop.TranscriptPath)
	if strings.Contains(cleanPath, "..") {
		return session.DoneSignal{}, false
	}
	if home, err := os.UserHomeDir(); err == nil {
		if !strings.HasPrefix(cleanPath, filepath.Join(home, ".claude")) {
			return session.DoneSignal{}, false
		}
	}
	return scanTranscriptForDone(cleanPath)
}

// scanTranscriptForDone reads the last transcript line, and if it is an
// assistant turn, scans its text content for a completion sentinel. The path
// is the injectable source: tests point it at a temp file, no live agent
// required. A missing/unreadable file or a non-assistant tail yields no
// sentinel rather than an error.
func scanTranscriptForDone(path string) (session.DoneSignal, bool) {
	lastLine, err := readLastLine(path)
	if err != nil {
		return session.DoneSignal{}, false
	}
	var msg transcriptContentMessage
	if err := json.Unmarshal([]byte(lastLine), &msg); err != nil {
		return session.DoneSignal{}, false
	}
	if msg.Type != "assistant" {
		return session.DoneSignal{}, false
	}
	return session.ScanDoneSentinel(transcriptText(msg.Message.Content))
}

// transcriptText flattens an assistant message's content into plain text.
// Claude transcripts encode content either as a string or as an array of
// typed blocks ({"type":"text","text":"..."}); only text blocks contribute.
func transcriptText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return asString
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// readLastLine reads the last non-empty line from a file.
func readLastLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", err
	}

	size := stat.Size()
	if size == 0 {
		return "", fmt.Errorf("empty file")
	}

	// Read backwards in chunks to find the last complete line
	buf := make([]byte, 0, 16384)
	offset := size

	for offset > 0 {
		readSize := int64(16384)
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		if _, err := f.ReadAt(chunk, offset); err != nil {
			return "", err
		}
		buf = append(chunk, buf...)

		// Strip trailing whitespace/newlines for consistent handling
		trimmed := strings.TrimRight(string(buf), "\n\r ")
		// Find the last newline in the trimmed content
		lastNL := strings.LastIndexByte(trimmed, '\n')
		if lastNL >= 0 {
			return trimmed[lastNL+1:], nil
		}
	}

	// Entire file is one line
	return strings.TrimSpace(string(buf)), nil
}

// logCostDebug writes debug messages to ~/.agent-deck/cost-debug.log
// Only active when AGENTDECK_DEBUG is set.
func logCostDebug(format string, args ...any) {
	if os.Getenv("AGENTDECK_DEBUG") == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	logPath := filepath.Join(home, ".agent-deck", "cost-debug.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05.000"), msg)
}

// getCostEventsDir returns the path to the cost events directory.
func getCostEventsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "cost-events")
	}
	return filepath.Join(home, ".agent-deck", "cost-events")
}

// getClaudeConfigDirForHooks returns the Claude config directory for hook operations.
// Respects CLAUDE_CONFIG_DIR env var and agent-deck config resolution.
func getClaudeConfigDirForHooks() string {
	return session.GetClaudeConfigDir()
}
