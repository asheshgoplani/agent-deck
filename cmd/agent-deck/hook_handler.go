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

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
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
	HookEventName  string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	ConversationID string          `json:"conversation_id"`
	Source         string          `json:"source"`
	Matcher        json.RawMessage `json:"matcher,omitempty"`
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
	// TranscriptPath is persisted ONLY when the Stop-edge sentinel scan was
	// inconclusive because the turn's assistant record had not flushed yet
	// (issue #1186 flush race). The daemon re-scans this path on its poll
	// loop; the synchronous Stop hook (#1225) must not wait out the flush.
	TranscriptPath string `json:"transcript_path,omitempty"`
}

// normalizeHookEventKey folds hook event names from Claude (PascalCase), Cursor
// (camelCase), Hermes (snake_case), and Codex into a single lookup key.
func normalizeHookEventKey(event string) string {
	s := strings.ToLower(strings.TrimSpace(event))
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(s)
}

func isStopHookEvent(event string) bool {
	return normalizeHookEventKey(event) == "stop"
}

// mapEventToStatus maps a hook event to an agent-deck status string.
// Status semantics in agent-deck:
//   - "running" = agent is actively processing (green)
//   - "waiting" = agent is at the prompt, waiting for user input (orange)
//   - "dead"    = Session ended
func mapEventToStatus(event string) string {
	switch normalizeHookEventKey(event) {
	case "sessionstart":
		return "waiting" // at initial prompt, waiting for user input
	case "beforeagent":
		return "running" // Gemini received user input and is processing
	case "afteragent":
		return "waiting" // Gemini completed response, back to waiting
	case "pretoolcall", "pretooluse":
		return "running" // executing a tool call
	case "posttoolcall", "posttooluse", "posttoolusefailure":
		return "waiting" // finished a tool call, back at prompt
	case "onsessionstart":
		return "waiting" // Hermes session started, waiting for first prompt
	case "onsessionend":
		return "dead" // Hermes session ended
	case "userpromptsubmit", "beforesubmitprompt":
		return "running" // user sent prompt, agent is processing
	case "stop":
		return "waiting" // agent finished, back at prompt waiting for user
	case "permissionrequest":
		return "waiting" // agent needs permission approval
	case "notification":
		// Notification events with permission_prompt|elicitation_dialog matcher
		// are mapped to "waiting" by the caller after checking the matcher.
		// Default notification is informational, treat as no status change.
		return ""
	case "sessionend":
		return "dead"
	case "precompact":
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
	if normalizeHookEventKey(payload.HookEventName) == "notification" && payload.Matcher != nil {
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
	// to poll artifacts. When the turn's assistant record has not flushed yet
	// (Claude Code can fire Stop before appending it), persist the transcript
	// path instead and let the daemon finish the scan — the Stop hook runs
	// SYNCHRONOUSLY (#1225), so waiting out the flush here would add turn-end
	// latency to every managed session. Absent on ordinary mid-task Stops, so
	// the existing "waiting" behavior is unchanged.
	sessionID := strings.TrimSpace(payload.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(payload.ConversationID)
	}

	if isStopHookEvent(payload.HookEventName) {
		writeHookStatusWithScan(instanceID, status, sessionID, payload.HookEventName, detectDoneSentinel(data))
	} else {
		writeHookStatus(instanceID, status, sessionID, payload.HookEventName)
	}

	// #572: Sync agent-deck title from Claude Code's --name / /rename value.
	// Event-driven so user-facing rename lands within one hook tick; silent
	// no-op when no name is set (sessions started without --name keep the
	// existing agent-deck adjective-noun title).
	applyClaudeTitleSync(instanceID, sessionID)

	// Propagate Claude Code's /cd working-directory change (v2.1.169+) so the
	// TUI/web display and transcript lookups track the current cwd.
	applyClaudeCwdSync(instanceID, payload.Cwd)

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
	if normalizeHookEventKey(payload.HookEventName) == "permissionrequest" && parentIsDSP() {
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
	if isStopHookEvent(payload.HookEventName) {
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
	scan := doneScanResult{}
	if len(done) > 0 {
		scan.signal = &done[0]
	}
	writeHookStatusWithScan(instanceID, status, sessionID, event, scan)
}

// writeHookStatusWithScan is writeHookStatus plus the full Stop-edge scan
// outcome: a parsed sentinel persists as done_status/done_summary; an
// unflushed tail persists as transcript_path so the daemon can finish the
// scan (issue #1186 flush race).
func writeHookStatusWithScan(instanceID, status, sessionID, event string, scan doneScanResult) {
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
	if scan.signal != nil {
		statusFile.DoneStatus = scan.signal.Status
		statusFile.DoneSummary = scan.signal.Summary
	}
	statusFile.TranscriptPath = scan.pendingTranscript

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
	return session.GetHooksDir()
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
	if !isStopHookEvent(stop.HookEventName) {
		logCostDebug("not a Stop event, skipping")
		return
	}
	if stop.TranscriptPath == "" {
		logCostDebug("no transcript_path in Stop payload")
		return
	}

	// Validate transcript path through the shared fail-closed, boundary-aware
	// containment guard (same check the done-sentinel reader uses) so a crafted
	// payload can't coax this reader into opening an arbitrary file. Claude
	// stores transcripts under ~/.claude/projects/{hash}/{session}.jsonl.
	cleanPath, ok := session.ValidateTranscriptPath(stop.TranscriptPath)
	if !ok {
		logCostDebug("rejected transcript_path outside ~/.claude or traversal: %s", stop.TranscriptPath)
		return
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

// doneScanResult carries the Stop-edge sentinel-scan outcome into the hook
// status file. At most one field is set: signal when a sentinel was parsed
// from the flushed assistant turn; pendingTranscript (the validated
// transcript path) when the tail was unflushed at hook time — issue #1186
// flush race — so the daemon can finish the scan on its poll loop. The zero
// value is an ordinary Stop with nothing extra to persist.
type doneScanResult struct {
	signal            *session.DoneSignal
	pendingTranscript string
}

// detectDoneSentinel parses transcript_path out of a Stop hook payload and
// scans the transcript tail for a worker-printed completion sentinel
// (issue #1186). Path-traversal / ~/.claude containment guards mirror the
// cost path so a crafted payload can't read arbitrary files. The scan itself
// lives in internal/session, shared with the transition daemon's flush-race
// rescan.
func detectDoneSentinel(rawPayload []byte) doneScanResult {
	var stop stopHookPayload
	if err := json.Unmarshal(rawPayload, &stop); err != nil {
		return doneScanResult{}
	}
	cleanPath, ok := session.ValidateTranscriptPath(stop.TranscriptPath)
	if !ok {
		return doneScanResult{}
	}
	sig, found, pending := session.ScanTranscriptTailForDone(cleanPath)
	switch {
	case pending:
		return doneScanResult{pendingTranscript: cleanPath}
	case found:
		return doneScanResult{signal: &sig}
	default:
		return doneScanResult{}
	}
}

// readLastLine reads the last non-empty line from a file.
func readLastLine(path string) (string, error) {
	lines, err := session.TranscriptTailLines(path, 1)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("no non-empty line")
	}
	return lines[0], nil
}

// logCostDebug writes debug messages to the XDG cache cost-debug.log.
// Only active when AGENTDECK_DEBUG is set.
func logCostDebug(format string, args ...any) {
	if os.Getenv("AGENTDECK_DEBUG") == "" {
		return
	}
	logPath, err := effectiveCachePath("cost-debug.log")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return
	}
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
	path, err := agentpaths.EffectiveDataPath("cost-events", "cost-events")
	if err != nil {
		return filepath.Join(os.TempDir(), "agent-deck", "cost-events")
	}
	return path
}

// getClaudeConfigDirForHooks returns the Claude config directory for hook operations.
// Respects CLAUDE_CONFIG_DIR env var and agent-deck config resolution.
func getClaudeConfigDirForHooks() string {
	return session.GetClaudeConfigDir()
}
