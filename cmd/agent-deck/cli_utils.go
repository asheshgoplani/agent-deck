package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// tmuxProbeTimeout bounds the plain-argv tmux probes the CLI fires to identify
// the caller's own session. These deliberately omit -L so tmux auto-routes via
// $TMUX (see the display-message entries in TestNoRawTmuxExec_OutsideAllowlist),
// which is why they cannot use tmux.OutputBounded — that helper always emits
// -L <name>. They still need a deadline: on tmux 3.0a a client leaks an epoll
// fd per event-loop iteration, and once it hits RLIMIT_NOFILE it spins at 100%
// CPU in EMFILE retries and never exits. These probes run on conductor-polled
// CLI paths, so an unbounded one accumulates wedged clients exactly like the
// cadence pollers in internal/tmux did.
const tmuxProbeTimeout = 3 * time.Second

// tmuxProbeBounded runs `tmux <args…>` under tmuxProbeTimeout and returns
// stdout. exec.CommandContext SIGKILLs a wedged client at the deadline; the
// WaitDelay bounds the post-kill stdio drain.
func tmuxProbeBounded(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.WaitDelay = 2 * time.Second
	return cmd.Output()
}

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
//   - Tool with extra *flags* (e.g. "codex --dangerously-bypass-approvals-and-sandbox"):
//     keep tool detection but forward extra args via wrapper so they are not lost.
//   - Tool with a *known* claude/codex *subcommand* (e.g. "claude remote-control --name X",
//     "codex mcp list"): agent-deck's injected flags (--session-id, permission mode, …)
//     are only valid on the plain interactive invocation, never after a subcommand —
//     see #1800, where injecting them before "remote-control" silently turned it into a
//     positional argument of a different program. Run the line as-is instead of
//     guessing where flags belong.
//   - Generic shell command: keep full command as-is.
//   - Explicit wrapper always wins.
//
// The subcommand check is a fixed, explicit allowlist (claudeKnownSubcommands /
// codexKnownSubcommands) rather than "any non-flag-shaped first token" — an early
// version of this fix used that broader heuristic and it misfired on an ordinary
// positional prompt (e.g. `-c 'claude "review this repo"'`): the prompt's first
// token isn't flag-shaped either, so it was wrongly routed through the no-injection
// path and silently lost --session-id / permission-mode, which a plain
// flags-then-prompt claude invocation had always gotten correctly before #1821.
// Only claude and codex are covered because they're the only builtins whose own
// command builders inject flags *inside* the wrapper substitution point (ahead of
// any trailing subcommand text); every other tool's flags (e.g. a custom
// [tools.X].dangerous_flag) are appended at the very end of the fully-built
// command by buildGenericCommand, so wrapper-suffix ordering never misplaces them
// — a custom tool's subcommand-shaped --cmd (e.g. "reviewbot serve") correctly
// keeps using the wrapper-suffix path below and needs no special-casing.
//
// Returns a non-nil err only when the extra-args portion of rawCommand can't be
// tokenized unambiguously (e.g. an unterminated quote) — in that case agent-deck
// refuses to guess flag placement rather than silently building a broken command.
// isSubcommandPassthrough reports whether toolName/command/wrapper came from
// the no-flag-injection passthrough branch below (Tool="shell", the raw
// command run verbatim). Callers must propagate it onto the created
// Instance's SubcommandPassthrough field — see that field's doc for why:
// it's the only thing that lets buildShellPassthroughCommand (instance.go)
// tell "this session's command was explicitly validated as a claude/codex
// subcommand invocation" apart from "an ordinary Tool==shell command that
// merely mentions claude/codex" at spawn time (Claude review, PR #1821 HIGH #1).
func resolveSessionCommand(rawCommand, explicitWrapper string) (toolName, command, wrapper, note string, isSubcommandPassthrough bool, err error) {
	raw := strings.TrimSpace(rawCommand)
	wrapper = strings.TrimSpace(explicitWrapper)
	if raw == "" {
		return "", "", wrapper, "", false, nil
	}

	toolName = detectTool(raw)
	base, extra := splitFirstWord(raw)

	// No explicit wrapper provided and command looks like "tool arg1 arg2".
	if wrapper == "" && extra != "" {
		baseTool := detectTool(base)
		if baseTool != "shell" {
			tokens, tokenizeErr := splitShellTokens(extra)
			if tokenizeErr != nil {
				return "", "", "", "", false, fmt.Errorf(
					"could not parse extra arguments in --cmd %q (%v); agent-deck refuses "+
						"to guess where its flags belong when quoting is ambiguous — use "+
						"--wrapper to control placement explicitly, or wrap the whole "+
						"command yourself (e.g. bash -c '...')",
					raw, tokenizeErr)
			}

			// Only route through the no-flag-injection passthrough when the
			// first extra token is a REAL, known claude/codex subcommand —
			// see the allowlist rationale in the function doc above. The
			// length guard is defensive: today `extra` is non-empty so
			// splitShellTokens always yields at least one token, but a
			// future tokenizer change (e.g. treating a bare `''` as
			// producing no token) must not turn this into a panic.
			if len(tokens) > 0 && isKnownSubcommandToken(baseTool, tokens[0]) {
				toolName = "shell"
				command = raw
				note = fmt.Sprintf(
					"detected subcommand-shaped argument %q after tool '%s' — running "+
						"the command as-is with no session/permission flag injection "+
						"(those flags aren't valid after a subcommand)",
					tokens[0], base)
				return toolName, command, wrapper, note, true, nil
			}

			toolName = baseTool
			if toolDef := session.GetToolDef(toolName); toolDef != nil {
				command = toolDef.Command
			} else {
				command = base
			}
			wrapper = strings.TrimSpace("{command} " + extra)
			note = fmt.Sprintf("parsed --cmd as tool '%s' and forwarded extra args via wrapper", toolName)
			return toolName, command, wrapper, note, false, nil
		}
	}

	if toolDef := session.GetToolDef(toolName); toolDef != nil {
		command = toolDef.Command
	} else {
		command = raw
	}
	return toolName, command, wrapper, note, false, nil
}

// claudeKnownSubcommands / codexKnownSubcommands are the real CLI subcommands
// of the two builtins whose own command builders inject agent-deck flags
// (--session-id, permission-mode, --yolo, --model, …) ahead of any trailing
// text. A --cmd whose first extra-args token exactly matches one of these
// gets routed through unmodified instead of via wrapper-suffix flag
// injection (#1800). This is deliberately a fixed, maintained list rather
// than "anything that isn't flag-shaped" — see resolveSessionCommand's doc.
// A future claude/codex subcommand not yet listed here falls back to the
// wrapper-suffix path and can still reproduce #1800's ordering bug; that is
// an accepted, bounded gap (same trade-off already accepted for a root flag
// preceding a subcommand, e.g. "claude --debug remote-control") in exchange
// for never misrouting an ordinary positional prompt.
//
// Canonical subcommand source: `claude --help` (Claude Code CLI top-level
// command list) as of the claude version this repo currently targets —
// cross-check there when adding a new one.
var claudeKnownSubcommands = map[string]bool{
	"mcp":            true,
	"plugin":         true,
	"install":        true,
	"remote-control": true,
	"update":         true,
	"doctor":         true,
	"config":         true,
}

// Canonical subcommand source: `codex --help` (Codex CLI top-level command
// list) as of the codex version this repo currently targets — cross-check
// there when adding a new one. This is a fixed, maintained list (see the
// doc comment above); it is only ever as complete as the day it was last
// checked against `codex --help`, so a native subcommand added upstream
// after that will fall back to the wrapper-suffix path until it's added
// here (accepted, bounded gap — see the doc comment above). "fork" added
// per Codex review, PR #1821 P2: it was missing, so `-c "codex fork ..."`
// still had agent-deck's flags injected ahead of it.
var codexKnownSubcommands = map[string]bool{
	"mcp":    true,
	"exec":   true,
	"login":  true,
	"logout": true,
	"apply":  true,
	"resume": true,
	"fork":   true,
}

// isKnownSubcommandToken reports whether tok is a real subcommand of the
// given builtin tool. Returns false for any tool other than claude/codex —
// see resolveSessionCommand's doc for why only those two need this check.
func isKnownSubcommandToken(tool, tok string) bool {
	switch tool {
	case "claude":
		return claudeKnownSubcommands[tok]
	case "codex":
		return codexKnownSubcommands[tok]
	default:
		return false
	}
}

// splitShellTokens performs minimal POSIX-ish tokenization of s: splits on
// whitespace, honors single/double quoting, and backslash-escapes the
// following character outside single quotes. It exists so resolveSessionCommand
// can inspect the *first* extra-args token without a full shell parser. It
// returns an error on an unterminated quote so callers can distinguish
// "genuinely ambiguous input" from "just didn't need quoting" — used to
// REFUSE rather than guess (#1800).
func splitShellTokens(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	haveToken := false
	inSingle, inDouble := false, false

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			} else if c == '\\' && i+1 < len(s) && strings.ContainsRune(`"\$`+"`", rune(s[i+1])) {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inSingle = true
			haveToken = true
		case c == '"':
			inDouble = true
			haveToken = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			haveToken = true
		case c == '\\':
			// Trailing unescaped backslash with nothing after it — ambiguous
			// (is it a literal backslash or an incomplete escape?). REFUSE
			// rather than silently emit it as a literal token (#1800: the
			// contract is to refuse when quoting/escaping is ambiguous, not
			// guess).
			return nil, fmt.Errorf("trailing unescaped backslash")
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if haveToken {
				tokens = append(tokens, cur.String())
				cur.Reset()
				haveToken = false
			}
		default:
			cur.WriteByte(c)
			haveToken = true
		}
	}

	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote")
	}
	if haveToken {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
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

// resolveGroupSelection picks the group for a new session using a fixed
// priority order. Priority (issue #972):
//  1. Explicit -g/--group always wins.
//  2. inheritGroup (launch --inherit-group): the parent-session group wins
//     over the cwd-derived group. This keeps a fanned-out fleet co-located
//     with its parent even when each child runs in its own worktree
//     (e.g. .worktrees/<branch>, whose leaf folder would otherwise derive a
//     junk per-branch group). Opt-in so it never regresses #972's conductor
//     case, which relies on the cwd-derived group winning by default.
//  3. Otherwise the cwd-derived project group wins.
//  4. Parent-session group is the fallback only when no cwd-derived group is
//     available (e.g. an empty project path mapping).
//
// Prior to #972 step 3 did not exist, so every conductor-spawned child
// silently inherited the conductor's `conductor` group.
func resolveGroupSelection(currentGroup, cwdDerivedGroup, parentGroup string, explicitGroupProvided, inheritGroup bool) string {
	if explicitGroupProvided {
		return currentGroup
	}
	if inheritGroup && parentGroup != "" {
		return parentGroup
	}
	if cwdDerivedGroup != "" {
		return cwdDerivedGroup
	}
	return parentGroup
}

// shouldInheritParentGroup decides whether a parented `launch` with no explicit
// -g should adopt the parent's group (i.e. behave as if --inherit-group was
// passed). It is the auto-default that makes a fanned-out fleet land with its
// parent without anyone remembering the flag.
//
// Priority:
//  1. An explicit -g always wins — never auto-inherit over a deliberate group.
//  2. --inherit-group set → inherit.
//  3. Otherwise inherit when the child path is a git LINKED worktree. A
//     worktree's path-derived group is junk (the branch leaf, or `worktrees`),
//     so a worktree child almost always belongs with its parent. This is the
//     load-bearing fix: fleets fan out into worktrees, and they should stay
//     co-located by default.
//
// pathIsLinkedWorktree is a thunk so the (process-spawning) git probe runs only
// when steps 1–2 didn't already decide — and stays trivially unit-testable.
// #972 is preserved: conductor children launch into separate REAL repos (main
// working trees, not linked worktrees), so this returns false for them and the
// cwd-derived project group still wins.
func shouldInheritParentGroup(explicitGroupProvided, inheritGroupFlag bool, pathIsLinkedWorktree func() bool) bool {
	if explicitGroupProvided {
		return false
	}
	if inheritGroupFlag {
		return true
	}
	return pathIsLinkedWorktree()
}

// resolveAddPath resolves the user-provided positional path arg for `agent-deck add`.
// Also used by `agent-deck session move` (#1706): both take a user-supplied
// positional project path and must resolve it the same way.
// Handles ".", "~", "~/foo", "$VAR/foo", and relative/absolute paths uniformly.
// session.ExpandPath runs first so a literal tilde from a non-expanding shell
// (e.g. SSH-driven invocation) reaches a real home directory before Abs.
func resolveAddPath(rawPathArg string) (string, error) {
	if rawPathArg == "." {
		return os.Getwd()
	}
	return filepath.Abs(session.ExpandPath(rawPathArg))
}

// resolveSSHAddPaths applies `agent-deck add`'s --ssh path-routing rule: the
// project lives on the remote host, so the resolved positional path is never
// a local path to validate or launch tmux in.
//
// An explicitly given positional path (explicitPathProvided) names the
// REMOTE working directory, unless an explicit --remote-path was already
// given, which always wins (matching the documented
// `add --ssh <host> --remote-path <path>` pattern). Without this routing, a
// positional path given alongside --ssh (e.g. `add <remote-worktree-path>
// --ssh <host>`) was silently misused as the session's local ProjectPath
// placeholder while remotePath stayed empty, so the actual SSH-wrapped
// launch command never `cd`'d into the intended remote directory: the
// session launched in the SSH login shell's default directory instead of the
// registered worktree. Fixes asheshgoplani/agent-deck#1711 / #1710.
//
// rawPositionalPath must be the RAW positional argument, taken before
// resolveAddPath runs session.ExpandPath + filepath.Abs on it: those
// resolutions describe the controller machine, not the remote host, so
// running them here would rewrite `~/x` or `./x` into a local filesystem
// path (e.g. the controller's home directory or CWD) and ship that local
// path to the remote shell as the session's working directory. wrapForSSH
// also single-quotes SSHRemotePath verbatim before handing it to the remote
// shell, so a stored `~/x` or `$VAR/x` reaches the remote host inert (the
// remote shell does not expand a quoted `~` or `$VAR`); a non-absolute
// remote path can never resolve correctly today, so it is refused outright
// rather than stored and silently misinterpreted.
//
// Returns the local placeholder path (always CWD for --ssh sessions, used
// only for local bookkeeping such as tmux pane naming, never launched into)
// and the resolved remote path to store as Instance.SSHRemotePath.
func resolveSSHAddPaths(explicitPathProvided bool, rawPositionalPath, explicitRemotePath string) (localPlaceholder, remotePath string, err error) {
	localPlaceholder, err = os.Getwd()
	if err != nil {
		return "", "", err
	}
	remotePath = explicitRemotePath
	if explicitPathProvided && remotePath == "" {
		if !strings.HasPrefix(rawPositionalPath, "/") {
			return "", "", fmt.Errorf("--ssh remote path must be absolute (got %q); "+
				"agent-deck cannot resolve %q against the remote host's filesystem "+
				"(no local ~ or $VAR expansion applies there)", rawPositionalPath, rawPositionalPath)
		}
		remotePath = rawPositionalPath
	}
	return localPlaceholder, remotePath, nil
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
	c.ErrorWithData(message, code, nil)
}

// ErrorWithData prints an error message or JSON error response with extra
// machine-checkable fields merged into the JSON payload (e.g. the `delivery`
// status of `session send`, issue #1413). The reserved success/error/code
// keys always win over extra entries.
func (c *CLIOutput) ErrorWithData(message string, code string, extra map[string]interface{}) {
	if c.jsonMode {
		payload := make(map[string]interface{}, len(extra)+3)
		for k, v := range extra {
			payload[k] = v
		}
		payload["success"] = false
		payload["error"] = message
		payload["code"] = code
		c.printJSON(payload)
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
	// ErrCodeDeliveryFailed: `session send` typed the message but could not
	// confirm submission (delivery=typed_not_submitted, issue #1413).
	ErrCodeDeliveryFailed = "DELIVERY_FAILED"
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

	// Get current tmux session name (bounded — see tmuxProbeTimeout)
	output, err := tmuxProbeBounded("display-message", "-p", "#S")
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

// SubstateLabel returns a short human label for an additive Honest-Status-v2
// substate, or "" when there is no distinct refinement to show. Used by the
// verbose CLI status output (and mirrored in the TUI) so a supervisor can tell
// a dead-model no-op loop apart from a genuinely-running session.
func SubstateLabel(sub session.Substate) string {
	switch sub {
	case session.SubstateModelUnavailable:
		return "model unavailable"
	case session.SubstateAuth401:
		return "auth (login)"
	case session.SubstateUsageLimit:
		return "usage limit"
	case session.SubstateIdleAtEmptyPrompt:
		return "idle at prompt"
	case session.SubstateRunning:
		return "working"
	default:
		return ""
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
