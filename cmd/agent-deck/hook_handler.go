package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
func mapEventToStatus(event string) string {
	switch event {
	case "SessionStart":
		return "waiting" // Claude at initial prompt, waiting for user input
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
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
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

// getClaudeConfigDirForHooks returns the Claude config directory for hook operations.
// Checks CLAUDE_CONFIG_DIR env var first, then falls back to ~/.claude
func getClaudeConfigDirForHooks() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}
