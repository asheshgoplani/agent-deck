package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

// OpenCode 2's interactive TUI accepts -s/--session but not --fork (that flag
// exists only on `run` and `mini`). Forking a 2.x session therefore calls the
// server (`opencode api session.fork`), optionally moves the child onto the
// fork worktree, then resumes it with `opencode -s <child>`. The v2 installer
// also ships an `opencode2` shim; selecting that command forces this path even
// when the host cannot probe the binary (SSH, sandbox).

// openCodeExecutableBase returns the basename of the first non-env token in a
// launch command, lowercased and without a Windows .exe suffix.
func openCodeExecutableBase(command string) string {
	for _, field := range strings.Fields(strings.TrimSpace(command)) {
		if isShellEnvAssignment(field) {
			continue
		}
		base := strings.Trim(filepath.Base(field), `"'`)
		base = strings.TrimSuffix(strings.ToLower(base), ".exe")
		return base
	}
	return ""
}

// openCodeUsesV2CLI reports whether this session's OpenCode launch must avoid
// 1.x-only flags and fork through the v2 session API. The opencode2 shim is
// always 2.x. A configured `[opencode] command = "opencode2"` is too. Otherwise
// the installed binary is probed; SSH and sandbox sessions that still say
// `opencode` keep the 1.x command, matching launch-flag policy.
func (i *Instance) openCodeUsesV2CLI() bool {
	if i == nil {
		return false
	}
	if openCodeExecutableBase(i.Command) == "opencode2" {
		return true
	}
	if openCodeExecutableBase(GetToolCommand("opencode")) == "opencode2" {
		return true
	}
	return i.openCodeRejectsV1LaunchFlags()
}

// openCodeLauncher is the executable used for a bare OpenCode start/resume.
// "opencode" follows [opencode].command; "opencode2" is the v2 shim itself.
func (i *Instance) openCodeLauncher(baseCommand string) (string, bool) {
	switch strings.TrimSpace(baseCommand) {
	case "", "opencode":
		cmd := strings.TrimSpace(GetToolCommand("opencode"))
		if cmd == "" {
			cmd = "opencode"
		}
		return cmd, true
	case "opencode2":
		return "opencode2", true
	default:
		return "", false
	}
}

// openCodePersistentCommand is the Command stored on a forked OpenCode session
// so a later restart resumes through the same launcher the fork used.
func (i *Instance) openCodePersistentCommand() string {
	if openCodeExecutableBase(i.Command) == "opencode2" {
		return "opencode2"
	}
	if openCodeExecutableBase(GetToolCommand("opencode")) == "opencode2" {
		return "opencode2"
	}
	return "opencode"
}

// OpenCodeCommandName is the command-picker label for this OpenCode session.
// Empty when the session is not OpenCode.
func (i *Instance) OpenCodeCommandName() string {
	if i == nil || i.Tool != "opencode" {
		return ""
	}
	return i.openCodePersistentCommand()
}

// quoteOpenCodeArg quotes a single token. A multi-word [opencode].command is
// already a shell command line (buildOpenCodeCommand interpolates it raw), so
// quoting it as one word would break `env FOO=bar opencode`.
func quoteOpenCodeArg(s string) string {
	if strings.ContainsAny(s, " \t") {
		return s
	}
	if s == "" || strings.ContainsAny(s, "\n'\"\\$`;|&<>(){}[]*?~") {
		return shellescape.Quote(s)
	}
	return s
}

// buildOpenCodeV2ForkCommand is the one-shot launch for an OpenCode 2 fork.
// It forks via the session API (the TUI has no --fork), moves the child onto
// workDir when that is a different tree, then execs the TUI on the child id.
func (i *Instance) buildOpenCodeV2ForkCommand(workDir, title string) string {
	bin := quoteOpenCodeArg(i.openCodeForkBinary())
	parentParam := shellescape.Quote("sessionID=" + strings.TrimSpace(i.OpenCodeSessionID))

	steps := []string{
		"cd -- " + shellescape.Quote(workDir),
		fmt.Sprintf(`fork_json=$(%s api session.fork --param %s --data '{}') || exit 1`, bin, parentParam),
		`new_id=$(printf '%s' "$fork_json" | tr -d '\n' | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\(ses_[^"]*\)".*/\1/p')`,
		`if [ -z "$new_id" ]; then echo "OpenCode fork did not return a session id" >&2; printf '%s\n' "$fork_json" >&2; exit 1; fi`,
	}
	if filepath.Clean(workDir) != filepath.Clean(i.ProjectPath) {
		body, err := json.Marshal(map[string]string{"directory": workDir})
		if err == nil {
			steps = append(steps, fmt.Sprintf(
				`%s api session.move --param sessionID="$new_id" --data %s || exit 1`,
				bin, shellescape.Quote(string(body)),
			))
		}
	}
	if title = strings.TrimSpace(title); title != "" {
		if body, err := json.Marshal(map[string]string{"title": title}); err == nil {
			steps = append(steps, fmt.Sprintf(
				`%s api session.update --param sessionID="$new_id" --data %s || true`,
				bin, shellescape.Quote(string(body)),
			))
		}
	}
	// Publish the child id before the TUI replaces this shell, so a restart
	// before async detection can recover it from the tmux session environment.
	steps = append(steps,
		`if [ -n "$TMUX" ]; then tmux set-environment OPENCODE_SESSION_ID "$new_id" 2>/dev/null || true; fi`,
		fmt.Sprintf(`exec %s -s "$new_id"`, bin),
	)
	return strings.Join(steps, " && ")
}

func (i *Instance) openCodeForkBinary() string {
	if launcher, ok := i.openCodeLauncher(i.openCodePersistentCommand()); ok {
		return launcher
	}
	return "opencode"
}
