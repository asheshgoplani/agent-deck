package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const codexNotifyMarkerBegin = "# BEGIN AGENTDECK CODEX NOTIFY"
const codexNotifyMarkerEnd = "# END AGENTDECK CODEX NOTIFY"
const codexNotifyLine = `notify = ["agent-deck", "codex-notify"]`

var codexNotifyTableRe = regexp.MustCompile(`(?m)^\s*\[notify\]\s*$`)
var codexNotifyKeyRe = regexp.MustCompile(`(?m)^\s*notify\s*=`)
var codexNotifyExactRe = regexp.MustCompile(`(?m)^\s*notify\s*=\s*\[\s*["']agent-deck["']\s*,\s*["']codex-notify["']\s*\]\s*$`)
var codexNotifyExactLineRe = regexp.MustCompile(`^\s*notify\s*=\s*\[\s*["']agent-deck["']\s*,\s*["']codex-notify["']\s*\]\s*$`)
var codexLegacyNotifyProgramLineRe = regexp.MustCompile(`(?i)^\s*program\s*=\s*\[\s*["']agent-deck["']\s*,\s*["']codex-notify["']\s*\]\s*$`)

type codexNotifyPayload struct {
	Type         string `json:"type"`
	Event        string `json:"event"`
	Method       string `json:"method"`
	SessionID    string `json:"session_id"`
	ThreadID     string `json:"thread_id"`
	ThreadIDDash string `json:"thread-id"`
	TurnID       string `json:"turn_id"`
	TurnIDDash   string `json:"turn-id"`
	Params       map[string]json.RawMessage
	Payload      map[string]json.RawMessage
}

func mapCodexNotifyToStatus(event string) string {
	e := strings.ToLower(strings.TrimSpace(event))
	if e == "" {
		return ""
	}

	switch e {
	case "thread.started", "thread/started", "thread-started",
		"session.configured", "session/configured", "session-configured":
		return "waiting"
	case "agent-turn-complete", "agent-turn-completed", "turn/completed", "turn-completed", "turn.completed",
		"turn/complete", "turn-complete", "turn.complete", "turn/failed", "turn-failed", "turn.failed",
		"turn/aborted", "turn-aborted", "turn.aborted", "turn/cancelled", "turn-cancelled", "turn.cancelled",
		"turn/canceled", "turn-canceled", "turn.canceled":
		return "waiting"
	case "agent-turn-start", "agent-turn-started", "turn/started", "turn-started", "turn.started":
		return "running"
	default:
		canon := strings.NewReplacer(".", "/", "-", "/", "_", "/").Replace(e)
		if strings.Contains(canon, "thread/started") || strings.Contains(canon, "session/configured") {
			return "waiting"
		}
		if strings.Contains(canon, "turn") && (strings.Contains(canon, "complete") ||
			strings.Contains(canon, "fail") ||
			strings.Contains(canon, "abort") ||
			strings.Contains(canon, "cancel")) {
			return "waiting"
		}
		if strings.Contains(e, "turn") && strings.Contains(e, "complete") {
			return "waiting"
		}
		if strings.Contains(canon, "turn") && strings.Contains(canon, "start") {
			return "running"
		}
		return ""
	}
}

func decodeStringField(raw map[string]json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	for _, key := range keys {
		value, ok := raw[key]
		if !ok || len(value) == 0 {
			continue
		}
		var str string
		if err := json.Unmarshal(value, &str); err == nil {
			str = strings.TrimSpace(str)
			if str != "" {
				return str
			}
		}
	}
	return ""
}

func parseCodexNotifyPayload(data []byte) (event, sessionID, turnID string) {
	var payload codexNotifyPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", "", ""
	}

	event = strings.TrimSpace(payload.Type)
	if event == "" {
		event = strings.TrimSpace(payload.Event)
	}
	if event == "" {
		event = strings.TrimSpace(payload.Method)
	}
	if event == "" {
		event = decodeStringField(payload.Params, "type", "event", "method")
	}
	if event == "" {
		event = decodeStringField(payload.Payload, "type", "event", "method")
	}

	sessionID = strings.TrimSpace(payload.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(payload.ThreadID)
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(payload.ThreadIDDash)
	}
	if sessionID == "" {
		sessionID = decodeStringField(payload.Params, "session_id", "thread_id", "thread-id", "id")
	}
	if sessionID == "" {
		sessionID = decodeStringField(payload.Payload, "session_id", "thread_id", "thread-id", "id")
	}
	turnID = strings.TrimSpace(payload.TurnID)
	if turnID == "" {
		turnID = strings.TrimSpace(payload.TurnIDDash)
	}
	if turnID == "" {
		turnID = decodeStringField(payload.Params, "turn_id", "turn-id")
	}
	if turnID == "" {
		turnID = decodeStringField(payload.Payload, "turn_id", "turn-id")
	}

	return event, sessionID, turnID
}

// handleCodexNotify processes Codex notify payloads.
func handleCodexNotify() {
	instanceID := os.Getenv("AGENTDECK_INSTANCE_ID")
	if instanceID == "" {
		return
	}

	eventArg := ""
	var data []byte
	// Codex notify may pass payload in argv and/or stdin.
	if len(os.Args) > 2 {
		for _, arg := range os.Args[2:] {
			arg = strings.TrimSpace(arg)
			if arg == "" {
				continue
			}
			if strings.HasPrefix(arg, "{") && strings.HasSuffix(arg, "}") {
				data = []byte(arg)
				break
			}
			if eventArg == "" {
				eventArg = arg
			}
		}
	}

	if len(data) == 0 {
		readData, err := io.ReadAll(os.Stdin)
		if err != nil || len(readData) == 0 {
			readData = nil
		}
		if len(readData) > 0 {
			data = readData
		}
	}

	event := ""
	sessionID := ""
	turnID := ""
	if len(data) > 0 {
		event, sessionID, turnID = parseCodexNotifyPayload(data)
		if event == "" {
			trimmed := strings.TrimSpace(string(data))
			if !strings.HasPrefix(trimmed, "{") {
				event = trimmed
			}
		}
	}
	if event == "" {
		event = eventArg
	}
	status := mapCodexNotifyToStatus(event)
	if status == "" {
		return
	}

	if sessionID == "" {
		sessionID = strings.TrimSpace(os.Getenv("CODEX_SESSION_ID"))
	}

	writeCodexHookStatus(instanceID, status, sessionID, event, turnID)
}

func codexTurnEdge(event string) (started, completed bool) {
	canon := strings.NewReplacer(".", "/", "-", "/", "_", "/").Replace(strings.ToLower(strings.TrimSpace(event)))
	if !strings.Contains(canon, "turn") {
		return false, false
	}
	return strings.Contains(canon, "start"), strings.Contains(canon, "complete") ||
		strings.Contains(canon, "fail") || strings.Contains(canon, "abort") || strings.Contains(canon, "cancel")
}

// writeCodexHookStatus retains both edges of the current turn under a file
// lock. Notify invocations are separate processes and can overlap; serializing
// the read/modify/write makes the generation proof deterministic.
func writeCodexHookStatus(instanceID, status, sessionID, event string, turnIDs ...string) {
	if instanceID == "" || status == "" {
		return
	}
	hooksDir := getHooksDir()
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		return
	}
	base := filepath.Base(instanceID)
	// This lock serializes notify writers only. It is intentionally distinct
	// from the host-owned consumption lock: a sandbox may control this scoped
	// directory, so host lifecycle code must never wait on it.
	lock, err := os.OpenFile(filepath.Join(hooksDir, base+".codex-writer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return
	}
	defer closeChecked(lock)
	locked := false
	for attempt := 0; attempt < 20; attempt++ {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			locked = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !locked {
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	path := filepath.Join(hooksDir, base+".json")
	var prior hookStatusFile
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &prior)
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		session.WriteHookSessionAnchor(instanceID, sessionID)
	}
	evidenceSessionID := sessionID
	if evidenceSessionID == "" {
		evidenceSessionID = session.ReadHookSessionAnchor(instanceID)
	}
	started, completed := codexTurnEdge(event)
	turnID := ""
	if len(turnIDs) > 0 {
		turnID = strings.TrimSpace(turnIDs[0])
	}
	if started {
		prior.CodexCompletedGeneration = ""
		prior.CodexCompletedSessionID = ""
		if evidenceSessionID != "" && turnID != "" {
			prior.CodexStartedGeneration = fmt.Sprintf("%s:%s", evidenceSessionID, turnID)
			prior.CodexStartedSessionID = evidenceSessionID
		} else {
			prior.CodexStartedGeneration = ""
			prior.CodexStartedSessionID = ""
		}
	}
	completionGeneration := ""
	if evidenceSessionID != "" && turnID != "" {
		completionGeneration = fmt.Sprintf("%s:%s", evidenceSessionID, turnID)
	}
	// Codex's legacy notify contract emits only agent-turn-complete. A complete
	// thread/turn identity therefore supplies both sides of the generation
	// proof; waiting must not depend on a start notification that never occurs.
	if completed && completionGeneration != "" &&
		(prior.Status != "running" || prior.CodexStartedGeneration == "" || completionGeneration == prior.CodexStartedGeneration) {
		prior.CodexStartedGeneration = completionGeneration
		prior.CodexStartedSessionID = evidenceSessionID
		prior.CodexCompletedGeneration = completionGeneration
		prior.CodexCompletedSessionID = evidenceSessionID
	}
	prior.Status, prior.SessionID, prior.Event = status, sessionID, event
	prior.Timestamp = time.Now().Unix()
	prior.DoneStatus, prior.DoneSummary, prior.TranscriptPath, prior.Cwd = "", "", "", ""
	writeHookStatusFile(instanceID, prior, false)
}

func handleCodexHooks(args []string) {
	if len(args) == 0 {
		printCodexHooksUsage(os.Stderr)
		os.Exit(1)
	}

	// A help request anywhere in the argument list must print usage and exit
	// without side effects (#1993).
	if hooksHelpRequested(args) {
		printCodexHooksUsage(os.Stdout)
		return
	}

	switch args[0] {
	case "help", "--help", "-h":
		printCodexHooksUsage(os.Stdout)
	case "install":
		handleCodexHooksInstall()
	case "uninstall":
		handleCodexHooksUninstall()
	case "status":
		handleCodexHooksStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown codex-hooks subcommand: %s\n", args[0])
		printCodexHooksUsage(os.Stderr)
		os.Exit(1)
	}
}

func printCodexHooksUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck codex-hooks <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage Codex notify hook integration.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  install      Install or upgrade agent-deck Codex notify hook")
	fmt.Fprintln(w, "  uninstall    Remove agent-deck Codex notify hook")
	fmt.Fprintln(w, "  status       Show current hook install status")
}

func handleCodexHooksInstall() {
	configPath := getCodexConfigPath()
	content, _ := readFileOrEmpty(configPath)

	block := codexNotifyMarkerBegin + "\n" +
		codexNotifyLine + "\n" +
		codexNotifyMarkerEnd + "\n"

	if strings.Contains(content, codexNotifyMarkerBegin) {
		begin := strings.Index(content, codexNotifyMarkerBegin)
		endRel := strings.Index(content[begin:], codexNotifyMarkerEnd)
		if endRel != -1 {
			end := begin + endRel + len(codexNotifyMarkerEnd)
			updated := strings.TrimSpace(content[:begin] + content[end:])
			updated = prependCodexNotifyBlock(block, updated)
			if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Error creating codex config dir: %v\n", err)
				os.Exit(1)
			}
			if err := os.WriteFile(configPath, []byte(updated), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing codex config: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Codex notify hook upgraded successfully.")
			fmt.Printf("Config: %s\n", configPath)
			return
		}
	}

	if updated, removed := removeLegacyCodexNotifyTable(content); removed {
		updated = prependCodexNotifyBlock(block, strings.TrimSpace(updated))
		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating codex config dir: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(configPath, []byte(updated), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing codex config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Codex notify hook upgraded successfully.")
		fmt.Printf("Config: %s\n", configPath)
		return
	}

	if codexNotifyExactRe.MatchString(content) {
		fmt.Println("Codex notify hook is already installed.")
		fmt.Printf("Config: %s\n", configPath)
		return
	}

	if codexNotifyKeyRe.MatchString(content) || codexNotifyTableRe.MatchString(content) {
		fmt.Fprintf(os.Stderr, "Error: existing notify setting found in %s\n", configPath)
		fmt.Fprintln(os.Stderr, "Please merge manually by setting:")
		fmt.Fprintln(os.Stderr, `  notify = ["agent-deck", "codex-notify"]`)
		os.Exit(1)
	}

	newContent := prependCodexNotifyBlock(block, content)

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating codex config dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing codex config: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Codex notify hook installed successfully.")
	fmt.Printf("Config: %s\n", configPath)
}

func handleCodexHooksUninstall() {
	configPath := getCodexConfigPath()
	content, err := readFileOrEmpty(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading codex config: %v\n", err)
		os.Exit(1)
	}

	begin := strings.Index(content, codexNotifyMarkerBegin)
	if begin != -1 {
		endRel := strings.Index(content[begin:], codexNotifyMarkerEnd)
		if endRel == -1 {
			fmt.Fprintln(os.Stderr, "Error: malformed agent-deck Codex hook block in config.")
			os.Exit(1)
		}
		end := begin + endRel + len(codexNotifyMarkerEnd)
		updated := content[:begin] + content[end:]
		updated = strings.TrimSpace(updated)
		if updated != "" {
			updated += "\n"
		}

		if err := os.WriteFile(configPath, []byte(updated), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing codex config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Codex notify hook removed successfully.")
		return
	}

	if updated, removed := removeLegacyCodexNotifyTable(content); removed {
		if err := os.WriteFile(configPath, []byte(updated), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing codex config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Codex notify hook removed successfully.")
		return
	}

	if updated, removed := removeExactCodexNotifyLine(content); removed {
		if err := os.WriteFile(configPath, []byte(updated), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing codex config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Codex notify hook removed successfully.")
		return
	}

	fmt.Println("No agent-deck Codex hook found to remove.")
}

func handleCodexHooksStatus() {
	configPath := getCodexConfigPath()
	content, _ := readFileOrEmpty(configPath)

	switch {
	case strings.Contains(content, codexNotifyMarkerBegin), codexNotifyExactRe.MatchString(content):
		fmt.Println("Status: INSTALLED")
	case hasLegacyCodexNotifyTable(content):
		fmt.Println("Status: LEGACY_NOTIFY_TABLE")
		fmt.Println("Run 'agent-deck codex-hooks install' to migrate to current Codex format.")
	case codexNotifyTableRe.MatchString(content):
		fmt.Println("Status: LEGACY_NOTIFY_TABLE")
		fmt.Println("Run 'agent-deck codex-hooks install' to migrate to current Codex format.")
	case codexNotifyKeyRe.MatchString(content):
		fmt.Println("Status: CUSTOM_NOTIFY")
	default:
		fmt.Println("Status: NOT INSTALLED")
		fmt.Println("Run 'agent-deck codex-hooks install' to install.")
	}
	fmt.Printf("Config: %s\n", configPath)
}

func getCodexConfigPath() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(codexHome, "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".codex", "config.toml")
	}
	return filepath.Join(home, ".codex", "config.toml")
}

func readFileOrEmpty(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func prependCodexNotifyBlock(block, content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return block
	}
	return strings.TrimRight(block, "\n") + "\n\n" + trimmed + "\n"
}

func removeLegacyCodexNotifyTable(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	removed := false

	for idx := 0; idx < len(lines); idx++ {
		line := lines[idx]
		if strings.TrimSpace(line) != "[notify]" {
			out = append(out, line)
			continue
		}

		progIdx := idx + 1
		for progIdx < len(lines) && strings.TrimSpace(lines[progIdx]) == "" {
			progIdx++
		}
		if progIdx < len(lines) && codexLegacyNotifyProgramLineRe.MatchString(lines[progIdx]) {
			removed = true
			idx = progIdx
			continue
		}

		out = append(out, line)
	}

	if !removed {
		return content, false
	}

	updated := strings.TrimSpace(strings.Join(out, "\n"))
	if updated != "" {
		updated += "\n"
	}
	return updated, true
}

func hasLegacyCodexNotifyTable(content string) bool {
	_, removed := removeLegacyCodexNotifyTable(content)
	return removed
}

func removeExactCodexNotifyLine(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	removed := false

	for _, line := range lines {
		if codexNotifyExactLineRe.MatchString(line) {
			removed = true
			continue
		}
		out = append(out, line)
	}

	if !removed {
		return content, false
	}

	updated := strings.TrimSpace(strings.Join(out, "\n"))
	if updated != "" {
		updated += "\n"
	}
	return updated, true
}
