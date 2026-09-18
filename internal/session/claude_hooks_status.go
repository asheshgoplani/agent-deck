package session

import (
	"context"
	"encoding/json"
	"fmt"
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
	// binary, both symlink-resolved (for a bare command: a PATH shadow).
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
	// Present is the weaker check: every event carries our hook in some
	// recognised form (bare, for another binary, marker-less, dangling).
	Present bool `json:"present"`
	// Executable is the stable path the install pins for this process ("" if
	// unknown or unpinnable); see hookExecutablePath.
	Executable string `json:"executable,omitempty"`
	// Unpinnable is set when this binary is not in a known install directory
	// (a dev build): the install keeps the entries' program and the heal
	// never writes from it.
	Unpinnable string `json:"unpinnable,omitempty"`
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
	if report.Executable == "" {
		report.Unpinnable = unpinnableHookExecutableReason
		if exe, err := os.Executable(); err == nil {
			report.Unpinnable += " (" + exe + ")"
		}
	}

	hooks, _ := readClaudeHooksSection(configDir)
	report.Present = hooksPresent(hooks)
	report.Installed = hooksInstalledWithCommand(hooks, true)

	for _, command := range distinctAgentDeckHookCommands(hooks) {
		report.Binaries = append(report.Binaries, resolveHookBinary(command, report.Executable, currentVersion))
	}
	return report
}

// readClaudeHooksSection returns the hooks section of configDir's
// settings.json (nil when the file is absent) or the parse error of a
// malformed file, which callers must not write over.
func readClaudeHooksSection(configDir string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read settings.json: %w", err)
	}
	var raw struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse settings.json: %w", err)
	}
	return raw.Hooks, nil
}

func resolveHookBinary(command, executable, currentVersion string) ClaudeHookBinaryStatus {
	st := ClaudeHookBinaryStatus{Command: command}
	words, _ := shellwords.Split(command)
	words = stripLeadingEnvAssignments(words)
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
	if executable != "" {
		// executable is the stable pinned path (possibly a symlink); the
		// shadow comparison is between the files actually run.
		if realExe, err := filepath.EvalSymlinks(executable); err == nil {
			executable = filepath.Clean(realExe)
		}
		st.Shadowed = st.ResolvedPath != executable
	}
	if st.Shadowed {
		st.Version = hookBinaryVersion(st.ResolvedPath)
	} else {
		st.Version = currentVersion
	}
	st.VersionMismatch = st.Version != "" && currentVersion != "" && st.Version != currentVersion
	return st
}

// StopHookForm is how the agent-deck Stop entry under a config dir is
// installed, as read at hook time.
type StopHookForm int

const (
	// StopHookFormUnknown: no settings.json, no agent-deck Stop entry, or an
	// unreadable file. The handler falls back to the command-line marker.
	StopHookFormUnknown StopHookForm = iota
	// StopHookFormSync: the entry runs synchronously, so Claude Code reads
	// the {decision:"block"} the drain answers with.
	StopHookFormSync
	// StopHookFormAsync: the entry is async; Claude ignores its stdout, so a
	// drain would consume the inbox for nothing (messaging audit P2-1).
	StopHookFormAsync
)

// StopHookInstallForm reports the form of the agent-deck Stop entry in
// configDir's settings.json (review round 3, finding 3: the handler checks
// the installed form at drain time instead of trusting the heal to have
// flipped it).
func StopHookInstallForm(configDir string) StopHookForm {
	hooks, err := readClaudeHooksSection(configDir)
	if err != nil {
		return StopHookFormUnknown
	}
	raw, ok := hooks["Stop"]
	if !ok {
		return StopHookFormUnknown
	}
	var matchers []claudeHookMatcher
	if json.Unmarshal(raw, &matchers) != nil {
		return StopHookFormUnknown
	}
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if !isAgentDeckHookCommand(h.Command) {
				continue
			}
			if h.Async {
				return StopHookFormAsync
			}
			return StopHookFormSync
		}
	}
	return StopHookFormUnknown
}
