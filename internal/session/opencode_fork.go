package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// OpenCode 2's interactive TUI accepts -s/--session but not --fork (that flag
// exists only on `run` and `mini`). Forking a 2.x session therefore calls the
// server (`opencode api session.fork`), optionally moves the child onto the
// fork worktree, then resumes it with `opencode -s <child>`. The v2 installer
// also ships an `opencode2` shim. A service-based fork must run on the target
// host: the local process must never mutate a different host's session service.

// openCodeExecutableBase returns the basename of the first non-env token in a
// launch command, lowercased and without a Windows .exe suffix.
func openCodeExecutableBase(command string) string {
	words, ok := shellwords.Split(command)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(shellwords.ExecutableBase(words)), ".exe")
}

// CanonicalToolName maps launcher aliases to the identity used by session features.
func CanonicalToolName(tool string) string {
	if tool == "opencode2" {
		return "opencode"
	}
	return tool
}

// openCodeUsesV2CLI reports whether this session's OpenCode launch must avoid
// 1.x-only flags and fork through the v2 session API. The opencode2 shim is
// always 2.x. A configured `[opencode] command = "opencode2"` is too. Otherwise
// the installed binary is probed; SSH and sandbox sessions that still say
// `opencode` keep the 1.x command, matching launch-flag policy.
func (i *Instance) openCodeUsesV2CLI() bool {
	return i != nil && i.openCodeRejectsV1LaunchFlags()
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
		// An explicit executable path or env-wrapped root launcher still needs
		// resume/options handling. Subcommands remain intentional passthroughs.
		words, valid := shellwords.Split(baseCommand)
		if valid && (openCodeExecutableBase(baseCommand) == "opencode" || openCodeExecutableBase(baseCommand) == "opencode2") {
			for index, word := range words {
				base := strings.TrimSuffix(strings.ToLower(filepath.Base(word)), ".exe")
				if base == "opencode" || base == "opencode2" {
					if index+1 == len(words) {
						return baseCommand, true
					}
					break
				}
			}
		}
		return "", false
	}
}

// openCodePersistentCommand is the Command stored on a forked OpenCode session
// so a later restart resumes through the same launcher the fork used.
func (i *Instance) openCodePersistentCommand() string {
	if command := strings.TrimSpace(i.Command); command != "" {
		return command
	}
	return "opencode"
}

// OpenCodeCommandName is the command-picker label for this OpenCode session.
// Empty when the session is not OpenCode.
func (i *Instance) OpenCodeCommandName() string {
	if i == nil || i.Tool != "opencode" {
		return ""
	}
	if openCodeExecutableBase(i.openCodeForkBinary()) == "opencode2" {
		return "opencode2"
	}
	return "opencode"
}

// quoteOpenCodeArg quotes a single token. A multi-word [opencode].command is
// already a shell command line (buildOpenCodeCommand interpolates it raw), so
// quoting it as one word would break `env FOO=bar opencode`.
func quoteOpenCodeArg(s string) string {
	if words, ok := shellwords.Split(s); ok && len(words) > 0 && isShellEnvAssignment(words[0]) {
		// `exec VAR=value binary` is not a valid shell command. env makes the
		// same configured assignments work for both API calls and exec.
		return "env " + s
	}
	if strings.ContainsAny(s, " \t") {
		return s
	}
	if s == "" || strings.ContainsAny(s, "\n'\"\\$`;|&<>(){}[]*?~") {
		return shellescape.Quote(s)
	}
	return s
}

// openCodeForkBinary returns the selected launcher command, not its canonical tool name.
func (i *Instance) openCodeForkBinary() string {
	if launcher, ok := i.openCodeLauncher(i.openCodePersistentCommand()); ok {
		return launcher
	}
	return i.openCodePersistentCommand()
}

// setOpenCodeForkTitleViaService gives a 2.x fork child the agent-deck title
// before it is published. The worktree move happens earlier, in
// moveOpenCodeForkViaService. The caller removes the child if this fails.
func setOpenCodeForkTitleViaService(binary, childID, workDir, title string) error {
	if title == "" {
		return nil
	}
	body, err := json.Marshal(map[string]string{"title": title})
	if err != nil {
		return err
	}
	if _, err := runOpenCodeServiceCall(binary, workDir, "session.update", childID, string(body)); err != nil {
		return fmt.Errorf("opencode fork: set child title: %w", err)
	}
	return nil
}
