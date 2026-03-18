package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// hookPayload represents the JSON payload Claude Code sends to hooks via stdin.
// Only the fields we need are decoded; unknown fields are ignored.
type hookPayload struct {
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	Source        string          `json:"source"`
	Matcher       json.RawMessage `json:"matcher,omitempty"`
}

// hookStatusFile is the JSON written to ~/.agent-deck/hooks/{instance_id}.json
type hookStatusFile struct {
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
	Event     string `json:"event"`
	Timestamp int64  `json:"ts"`
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

	// Read stdin (Claude sends hook payload as JSON)
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		return
	}

	var payload hookPayload
	if err := json.Unmarshal(data, &payload); err != nil {
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

	writeHookStatus(instanceID, status, payload.SessionID, payload.HookEventName)

	// Write cost event if this hook contains usage data
	logCostDebug("hook event=%s instance=%s status=%s", payload.HookEventName, instanceID, status)
	writeCostEvent(instanceID, data)
}

// writeHookStatus writes a hook status file atomically for one instance.
func writeHookStatus(instanceID, status, sessionID, event string) {
	if instanceID == "" || status == "" {
		return
	}

	hooksDir := getHooksDir()
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
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

	jsonData, err := json.Marshal(statusFile)
	if err != nil {
		return
	}

	filePath := filepath.Join(hooksDir, instanceID+".json")
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, jsonData, 0644); err != nil {
		return
	}
	_ = os.Rename(tmpPath, filePath)

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
		"threadend", "threadended", "threadterminate", "threadterminated", "threadclose", "threadclosed",
		"threaddone", "threadexit", "threadexited":
		return true
	default:
		return false
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
	logCostDebug("transcript_path: %s", stop.TranscriptPath)

	lastLine, err := readLastLine(stop.TranscriptPath)
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
	if err := os.MkdirAll(costDir, 0755); err != nil {
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
		return
	}

	filename := fmt.Sprintf("%s_%d.json", instanceID, ts)
	tmpPath := filepath.Join(costDir, filename+".tmp")
	finalPath := filepath.Join(costDir, filename)

	if err := os.WriteFile(tmpPath, jsonData, 0644); err != nil {
		logCostDebug("write failed: %v", err)
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		logCostDebug("rename failed: %v", err)
		return
	}
	logCostDebug("wrote cost event: %s model=%s in=%d out=%d", finalPath, cf.Model, cf.InputTokens, cf.OutputTokens)
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
