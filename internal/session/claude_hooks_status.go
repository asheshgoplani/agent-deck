package session

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// Messaging audit P1-1: `hooks status` used to answer only "are the entries
// present". On a machine where the bare `agent-deck` resolves, on the PATH
// Claude sessions inherit, to a different (older) binary than the one the
// daemon runs, the hook half of the delivery spine silently runs stale code.
// ClaudeHooksStatus resolves every installed hook command the way a shell
// would and compares it with this binary, by path and by version.

// ClaudeHookBinaryStatus describes one distinct hook command found in
// settings.json and what it resolves to.
type ClaudeHookBinaryStatus struct {
	// Command is the command string as installed.
	Command string `json:"command"`
	// Bare is true for the legacy `agent-deck hook-handler` form that is
	// resolved through PATH at hook time.
	Bare bool `json:"bare"`
	// ResolvedPath is the symlink-resolved file the command runs, or "" when
	// it could not be resolved (ResolveError says why).
	ResolvedPath string `json:"resolved_path,omitempty"`
	ResolveError string `json:"resolve_error,omitempty"`
	// Shadowed is true when the command runs a different file than this
	// binary (for a bare command: a PATH shadow).
	Shadowed bool `json:"shadowed"`
	// Version is the version the hook binary reports, "" when unknown.
	Version string `json:"version,omitempty"`
	// VersionMismatch is true when Version is known and differs from this
	// binary's version.
	VersionMismatch bool `json:"version_mismatch"`
}

// ClaudeHooksStatusReport is the result of ClaudeHooksStatus.
type ClaudeHooksStatusReport struct {
	ConfigDir string `json:"config_dir"`
	// Installed is true only when every event carries our hook with the
	// current config AND the command is this binary's absolute path.
	Installed bool `json:"installed"`
	// Present is the weaker check: our hook is installed in some recognised
	// form (possibly bare or for another binary).
	Present bool `json:"present"`
	// Executable is this process's symlink-resolved path ("" if unknown).
	Executable string `json:"executable,omitempty"`
	// Version is this process's version, as passed by the caller.
	Version string `json:"version"`
	// Binaries lists each distinct hook command found, in first-seen order.
	Binaries []ClaudeHookBinaryStatus `json:"binaries"`
}

// Problems returns a human line per detected shadow / mismatch, empty when the
// install points at this binary.
func (r ClaudeHooksStatusReport) Problems() []string {
	var out []string
	for _, b := range r.Binaries {
		switch {
		case b.ResolveError != "":
			out = append(out, "hook command "+b.Command+" cannot be resolved: "+b.ResolveError)
		case b.Shadowed && b.Bare:
			out = append(out, "PATH shadow: bare `"+b.Command+"` resolves to "+b.ResolvedPath+", not this binary ("+r.Executable+")")
		case b.Shadowed:
			out = append(out, "hook command runs "+b.ResolvedPath+", not this binary ("+r.Executable+")")
		}
		if b.VersionMismatch {
			out = append(out, "version mismatch: hook binary is v"+b.Version+", this binary is v"+r.Version)
		}
	}
	return out
}

// hookBinaryVersion runs `<path> version` with a short timeout and returns
// the parsed version, "" on any failure. Test seam.
var hookBinaryVersion = func(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return ""
	}
	v := parseRemoteVersion(string(out))
	if v == strings.TrimSpace(string(out)) && !strings.Contains(v, ".") {
		return "" // no semver token at all
	}
	return v
}

// ClaudeHooksStatus inspects settings.json under configDir and resolves every
// installed agent-deck hook command against this binary. currentVersion is
// the running binary's version (main.Version); it is compared with what each
// hook binary reports.
func ClaudeHooksStatus(configDir, currentVersion string) ClaudeHooksStatusReport {
	report := ClaudeHooksStatusReport{ConfigDir: configDir, Version: currentVersion}
	if exe, err := hookExecutablePath(); err == nil {
		report.Executable = exe
	}

	hooks := readClaudeHooksSection(configDir)
	report.Present = hooksAlreadyInstalled(hooks)
	report.Installed = hooksInstalledWithCommand(hooks, true)

	seen := map[string]bool{}
	for _, cfg := range hookEventConfigs {
		var matchers []claudeHookMatcher
		if raw, ok := hooks[cfg.Event]; ok {
			_ = json.Unmarshal(raw, &matchers)
		}
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if !isAgentDeckHookCommand(h.Command) || seen[h.Command] {
					continue
				}
				seen[h.Command] = true
				report.Binaries = append(report.Binaries, resolveHookBinary(h.Command, report.Executable, currentVersion))
			}
		}
	}
	return report
}

func readClaudeHooksSection(configDir string) map[string]json.RawMessage {
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		return nil
	}
	var raw struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return raw.Hooks
}

func resolveHookBinary(command, executable, currentVersion string) ClaudeHookBinaryStatus {
	st := ClaudeHookBinaryStatus{Command: command}
	words, _ := shellwords.Split(command)
	for len(words) > 0 && strings.Contains(words[0], "=") && !strings.ContainsRune(words[0], os.PathSeparator) {
		words = words[1:] // leading VAR=value exports
	}
	if len(words) == 0 {
		st.ResolveError = "empty command"
		return st
	}
	program := words[0]
	st.Bare = !strings.ContainsRune(program, os.PathSeparator)
	path := program
	if st.Bare {
		found, err := exec.LookPath(program)
		if err != nil {
			st.ResolveError = err.Error()
			return st
		}
		path = found
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		st.ResolveError = err.Error()
		return st
	}
	st.ResolvedPath = filepath.Clean(resolved)
	st.Shadowed = executable != "" && st.ResolvedPath != executable
	if st.Shadowed {
		st.Version = hookBinaryVersion(st.ResolvedPath)
	} else {
		st.Version = currentVersion
	}
	st.VersionMismatch = st.Version != "" && currentVersion != "" && st.Version != currentVersion
	return st
}
