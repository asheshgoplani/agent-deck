package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// normalizeArgs reorders args so flags come before positional arguments.
// Go's flag package stops parsing at the first non-flag argument, which means
// "session show my-title --json" silently ignores --json. This function
// moves all flags to the front so they get parsed correctly.
func normalizeArgs(fs *flag.FlagSet, args []string) []string {
	// Build set of known boolean flags (don't need a value argument)
	boolFlags := make(map[string]bool)
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			boolFlags[f.Name] = true
		}
	})

	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// "--" terminates flag processing
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}

		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)

			// Determine flag name (strip leading dashes)
			name := strings.TrimLeft(arg, "-")

			// Handle --flag=value (value is part of the arg, nothing to move)
			if strings.Contains(name, "=") {
				continue
			}

			// If it's not a bool flag, the next arg is its value
			if !boolFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, arg)
		}
	}
	return append(flags, positional...)
}

// firstNonEmpty returns the first non-empty string after trimming whitespace.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// resolveSessionCommand normalizes the user-provided --cmd/-c input.
//
// Behavior:
//   - Plain tool name (e.g. "claude", "codex"): use built-in/default command.
//   - Tool with extra args (e.g. "codex --dangerously-bypass-approvals-and-sandbox"):
//     keep tool detection but forward extra args via wrapper so they are not lost.
//   - Generic shell command: keep full command as-is.
//   - Explicit wrapper always wins.
func resolveSessionCommand(rawCommand, explicitWrapper string) (toolName, command, wrapper, note string) {
	raw := strings.TrimSpace(rawCommand)
	wrapper = strings.TrimSpace(explicitWrapper)
	if raw == "" {
		return "", "", wrapper, ""
	}

	toolName = detectTool(raw)
	base, extra := splitFirstWord(raw)

	// No explicit wrapper provided and command looks like "tool arg1 arg2".
	// Preserve extra args by turning them into wrapper suffix.
	if wrapper == "" && extra != "" {
		baseTool := detectTool(base)
		if baseTool != "shell" {
			toolName = baseTool
			if toolDef := session.GetToolDef(toolName); toolDef != nil {
				command = toolDef.Command
			} else {
				command = base
			}
			wrapper = strings.TrimSpace("{command} " + extra)
			note = fmt.Sprintf("parsed --cmd as tool '%s' and forwarded extra args via wrapper", toolName)
			return toolName, command, wrapper, note
		}
	}

	if toolDef := session.GetToolDef(toolName); toolDef != nil {
		command = toolDef.Command
	} else {
		command = raw
	}
	return toolName, command, wrapper, note
}

func splitFirstWord(raw string) (string, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ""
	}
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return s[:i], strings.TrimSpace(s[i+1:])
		}
	}
	return s, ""
}

// resolveGroupSelection applies parent-group inheritance rules.
// If the user explicitly provided -g/--group, keep that value.
// Otherwise inherit the parent's group.
func resolveGroupSelection(currentGroup, parentGroup string, explicitGroupProvided bool) string {
	if explicitGroupProvided {
		return currentGroup
	}
	return parentGroup
}

// resolveAddPath resolves the user-provided positional path arg for `agent-deck add`.
// Handles ".", "~", "~/foo", "$VAR/foo", and relative/absolute paths uniformly.
// session.ExpandPath runs first so a literal tilde from a non-expanding shell
// (e.g. SSH-driven invocation) reaches a real home directory before Abs.
func resolveAddPath(rawPathArg string) (string, error) {
	if rawPathArg == "." {
		return os.Getwd()
	}
	return filepath.Abs(session.ExpandPath(rawPathArg))
}

// CLIOutput handles consistent output formatting across all CLI commands
type CLIOutput struct {
	jsonMode  bool
	quietMode bool
}

// NewCLIOutput creates a new CLI output handler
func NewCLIOutput(jsonMode, quietMode bool) *CLIOutput {
	return &CLIOutput{
		jsonMode:  jsonMode,
		quietMode: quietMode,
	}
}

// Success prints a success message or JSON response
func (c *CLIOutput) Success(message string, data interface{}) {
	if c.quietMode {
		return
	}
	if c.jsonMode {
		c.printJSON(data)
		return
	}
	fmt.Printf("%s %s\n", successSymbol, message)
}

// Error prints an error message or JSON error response
func (c *CLIOutput) Error(message string, code string) {
	if c.jsonMode {
		c.printJSON(map[string]interface{}{
			"success": false,
			"error":   message,
			"code":    code,
		})
		return
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", message)
}

// Print prints data (human-readable or JSON)
func (c *CLIOutput) Print(humanOutput string, jsonData interface{}) {
	if c.quietMode {
		return
	}
	if c.jsonMode {
		c.printJSON(jsonData)
		return
	}
	fmt.Print(humanOutput)
}

// printJSON marshals and prints JSON data
func (c *CLIOutput) printJSON(data interface{}) {
	output, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to format JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(output))
}

// Symbols for human-readable output
const (
	successSymbol = "✓"
	errorSymbol   = "✕"
	bulletSymbol  = "•"
)

// Error codes
const (
	ErrCodeNotFound         = "NOT_FOUND"
	ErrCodeAlreadyExists    = "ALREADY_EXISTS"
	ErrCodeAmbiguous        = "AMBIGUOUS"
	ErrCodeInvalidOperation = "INVALID_OPERATION"
	ErrCodeGroupNotEmpty    = "GROUP_NOT_EMPTY"
	ErrCodeMCPNotAvailable  = "MCP_NOT_AVAILABLE"
)

// ResolveSession finds a session by flexible matching (title, ID prefix, or path)
// Returns the matched session or nil with an error message
func ResolveSession(identifier string, instances []*session.Instance) (*session.Instance, string, string) {
	if identifier == "" {
		return nil, "session identifier is required", ErrCodeNotFound
	}

	var matches []*session.Instance

	// Try exact title match first
	for _, inst := range instances {
		if inst.Title == identifier {
			return inst, "", ""
		}
	}

	// Try ID prefix match (minimum 6 chars for prefix to avoid too many matches)
	if len(identifier) >= 6 {
		for _, inst := range instances {
			if strings.HasPrefix(inst.ID, identifier) {
				matches = append(matches, inst)
			}
		}
	}

	if len(matches) == 1 {
		return matches[0], "", ""
	}

	if len(matches) > 1 {
		var names []string
		for _, m := range matches {
			names = append(names, fmt.Sprintf("%s (%s)", m.Title, m.ID[:12]))
		}
		return nil, fmt.Sprintf("'%s' matches multiple sessions:\n  - %s\nUse full ID or more specific title.",
			identifier, strings.Join(names, "\n  - ")), ErrCodeAmbiguous
	}

	// Try path match - collect all sessions at this path
	var pathMatches []*session.Instance
	for _, inst := range instances {
		if inst.ProjectPath == identifier {
			pathMatches = append(pathMatches, inst)
		}
	}

	if len(pathMatches) == 1 {
		return pathMatches[0], "", ""
	}

	if len(pathMatches) > 1 {
		var names []string
		for _, m := range pathMatches {
			names = append(names, fmt.Sprintf("%s (%s)", m.Title, m.ID[:12]))
		}
		return nil, fmt.Sprintf("path '%s' has multiple sessions:\n  - %s\nUse title or ID to specify.",
			identifier, strings.Join(names, "\n  - ")), ErrCodeAmbiguous
	}

	return nil, fmt.Sprintf("session '%s' not found", identifier), ErrCodeNotFound
}

// GetCurrentSessionID detects the current agent-deck session from tmux environment
// Returns session ID or empty string if not in an agent-deck session
func GetCurrentSessionID() string {
	// Check if we're in tmux
	if os.Getenv("TMUX") == "" {
		return ""
	}

	// Get current tmux session name
	cmd := exec.Command("tmux", "display-message", "-p", "#S")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	sessionName := strings.TrimSpace(string(output))

	// Parse agent-deck session name: agentdeck_<title>_<id>
	if !strings.HasPrefix(sessionName, "agentdeck_") {
		return ""
	}

	// Extract ID (last part after final underscore)
	parts := strings.Split(sessionName, "_")
	if len(parts) < 3 {
		return ""
	}

	// ID is the last part
	return parts[len(parts)-1]
}

// ResolveSessionOrCurrent resolves a session by identifier, or uses current session if empty
func ResolveSessionOrCurrent(identifier string, instances []*session.Instance) (*session.Instance, string, string) {
	if identifier == "" {
		// Try to detect current session
		currentID := GetCurrentSessionID()
		if currentID == "" {
			return nil, "no session specified and not inside an agent-deck session", ErrCodeNotFound
		}
		identifier = currentID
	}

	return ResolveSession(identifier, instances)
}

// StatusSymbol returns the symbol for a status
func StatusSymbol(status session.Status) string {
	switch status {
	case session.StatusRunning:
		return "●"
	case session.StatusWaiting:
		return "◐"
	case session.StatusIdle:
		return "○"
	case session.StatusError:
		return "✕"
	case session.StatusStopped:
		return "■"
	default:
		return "?"
	}
}

// StatusString returns the string representation of a status
func StatusString(status session.Status) string {
	switch status {
	case session.StatusRunning:
		return "running"
	case session.StatusWaiting:
		return "waiting"
	case session.StatusIdle:
		return "idle"
	case session.StatusError:
		return "error"
	case session.StatusStopped:
		return "stopped"
	case session.StatusQueued:
		return "queued"
	default:
		return "unknown"
	}
}

// TruncateID returns a shortened ID for display
func TruncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// FormatPath shortens a path by replacing home directory with ~
func FormatPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
