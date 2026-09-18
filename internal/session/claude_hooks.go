package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// agentDeckHookCommand is the legacy bare form of the hook command. It is
// still recognised (and rewritten) on install, and is what the install falls
// back to when the running executable cannot be resolved.
const agentDeckHookCommand = "agent-deck hook-handler"

// hookHandlerSubcommand is the subcommand every agent-deck hook entry invokes.
const hookHandlerSubcommand = "hook-handler"

// hookExecutablePath returns the absolute, STABLE path of the running
// agent-deck binary, or "" when the binary is unpinnable. Messaging audit
// P1-1: the hook command used to be the bare `agent-deck hook-handler`, so
// each Claude session ran whatever `agent-deck` came first on ITS PATH — on
// the maintainer's machine a stale /usr/local/bin symlink eight releases
// behind the daemon. Installing an absolute path pins the hook to the binary
// that installed it.
//
// Review round 2 (P1-A): the pinned path must survive a package upgrade, so
// a versioned Homebrew Cellar keg is never pinned; the symlink the operator
// invokes (/opt/homebrew/bin/agent-deck, ~/.local/bin/agent-deck) is.
//
// Review round 3 (finding 2): only a path inside a known install directory
// (hookInstallDirs) is pinned. A binary anywhere else (a dev build under
// /tmp or a repo's out/ dir) is unpinnable: pinning it turned a `hooks
// status` or TUI start from such a build into a fleet-wide hook outage the
// moment the build directory was removed. For an unpinnable binary the
// install keeps whatever program the entries already name (the bare command
// on a fresh install) and the heal never writes; see
// stableHookExecutablePath. Test seam.
var hookExecutablePath = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return stableHookExecutablePath(exe, hookInstallDirs())
}

// unpinnableHookExecutableReason is what the heal and `hooks status` report
// when the running binary is not in a known install directory.
const unpinnableHookExecutableReason = "unpinnable dev build: hooks keep the bare command"

// hookInstallDirs lists the directories a hook entry may pin a binary in:
// the package-manager and user-local bin directories an install lands in
// and an upgrade replaces in place.
func hookInstallDirs() []string {
	dirs := []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/home/linuxbrew/.linuxbrew/bin",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return dirs
}

// stableHookExecutablePath picks the path a hook entry should pin for the
// running executable exe: the first of (exe as invoked, then the binary's
// name in each install directory) that lives directly in one of installDirs
// and resolves to the same file as exe. It returns "" when no such path
// exists: the binary is unpinnable and the entries keep their program.
func stableHookExecutablePath(exe string, installDirs []string) (string, error) {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	real = filepath.Clean(real)
	candidates := []string{filepath.Clean(exe)}
	for _, dir := range installDirs {
		candidates = append(candidates, filepath.Join(dir, filepath.Base(real)))
	}
	for _, candidate := range candidates {
		if filepath.IsAbs(candidate) && inInstallDir(candidate, installDirs) && resolvesToFile(candidate, real) {
			return candidate, nil
		}
	}
	return "", nil
}

// inInstallDir reports whether path sits directly inside one of dirs
// (symlinks in the directory part are not followed: the pinned string must
// be the stable one).
func inInstallDir(path string, dirs []string) bool {
	parent := filepath.Dir(path)
	for _, dir := range dirs {
		if dir != "" && parent == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

// resolvesToFile reports whether path, symlinks followed, is the file real.
func resolvesToFile(path, real string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && filepath.Clean(resolved) == real
}

// hookHandlerCommand returns the command a fresh install writes: the stable
// executable path plus the hook-handler subcommand, or the legacy bare form
// when the executable is unpinnable (so a hook is always installed).
func hookHandlerCommand() string {
	return hookHandlerCommandFor("")
}

// hookHandlerCommandFor returns the command the install writes in place of
// the agent-deck entry existing (empty when there is none). A pinnable
// binary always pins itself. An unpinnable one (review round 3, finding 2)
// keeps the program the entry already names while it still exists, and
// falls back to the bare form otherwise, so a dev build never writes its
// own path and never pins a stale one either.
func hookHandlerCommandFor(existing string) string {
	if exe, err := hookExecutablePath(); err == nil && exe != "" {
		return shellescape.Quote(exe) + " " + hookHandlerSubcommand
	}
	if program := hookCommandProgram(existing); program != "" && program != "agent-deck" && !hookCommandProgramMissing(existing) {
		return shellescape.Quote(program) + " " + hookHandlerSubcommand
	}
	return agentDeckHookCommand
}

// hookCommandProgram returns the program word of a hook command, leading
// VAR=value assignments stripped, "" when the command cannot be split.
func hookCommandProgram(command string) string {
	words, ok := shellwords.Split(command)
	if !ok {
		return ""
	}
	words = stripLeadingEnvAssignments(words)
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

// isAgentDeckHookCommand reports whether a settings.json hook command is one
// of ours, in either the legacy bare form or the absolute-path form. Only the
// command-position word and the subcommand are inspected, so a user hook that
// merely mentions agent-deck in an argument is not claimed.
func isAgentDeckHookCommand(command string) bool {
	words, ok := shellwords.Split(command)
	if !ok {
		return false
	}
	words = stripLeadingEnvAssignments(words)
	if len(words) < 2 || words[1] != hookHandlerSubcommand {
		return false
	}
	if words[0] == "agent-deck" {
		return true
	}
	// An absolute form: ours if it is the binary the install would write now,
	// or any agent-deck binary a previous install wrote.
	if exe, err := hookExecutablePath(); err == nil && exe != "" && words[0] == exe {
		return true
	}
	return strings.TrimSuffix(filepath.Base(words[0]), ".exe") == "agent-deck"
}

// stripLeadingEnvAssignments drops the leading VAR=value words a hook command
// may carry in front of the program (the Stop sync marker), leaving the
// program in command position. A word containing a path separator is never
// treated as an assignment.
func stripLeadingEnvAssignments(words []string) []string {
	for len(words) > 0 && isEnvAssignmentWord(words[0]) {
		words = words[1:]
	}
	return words
}

// isEnvAssignmentWord reports whether word is a VAR=value prefix rather than
// a program or argument (a word containing a path separator never is).
func isEnvAssignmentWord(word string) bool {
	return strings.Contains(word, "=") && !strings.ContainsRune(word, os.PathSeparator)
}

// claudeHookEntry is the read-only view of a hook entry used by the install
// checks. The rewrite itself goes through jsonObject (claude_hooks_json.go)
// so fields this struct does not know are never dropped.
type claudeHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Async   bool   `json:"async,omitempty"`
}

// claudeHookMatcher represents a matcher block (with optional matcher pattern) in settings.
type claudeHookMatcher struct {
	Matcher string            `json:"matcher,omitempty"`
	Hooks   []claudeHookEntry `json:"hooks"`
}

// StopHookSyncMarkerEnv is exported into the Stop hook's command by the sync
// install (messaging audit P2-1). DrainForStopHook consumes the parent's inbox
// and answers with {decision:"block"}; Claude Code only reads that answer from
// a SYNCHRONOUS hook. The handler drains unless the marker is present with a
// value other than "1": an absent marker means an install from before the
// marker existed, which is sync too (review round 2, P1-B), and the heal adds
// the marker to such an entry on the daemon's next start.
const StopHookSyncMarkerEnv = "AGENTDECK_STOP_SYNC"

// claudeHookEventConfig is one row of hookEventConfigs.
type claudeHookEventConfig struct {
	Event   string
	Matcher string // empty = no matcher
	Async   bool   // false = synchronous (blocks via exit code)
	// Env is an optional VAR=value prefix written in front of the command,
	// which the shell that runs the hook exports to the handler.
	Env string
}

// commandFor is the exact command string the install writes for this event
// in place of the agent-deck entry existing ("" for a fresh install; see
// hookHandlerCommandFor).
func (cfg claudeHookEventConfig) commandFor(existing string) string {
	if cfg.Env != "" {
		return cfg.Env + " " + hookHandlerCommandFor(existing)
	}
	return hookHandlerCommandFor(existing)
}

// agentDeckHookObject returns the agent-deck hook entry for an event, as
// the object the install writes, replacing the entry existing ("" when new).
func agentDeckHookObject(cfg claudeHookEventConfig, existing string) jsonObject {
	obj := jsonObject{
		{Key: "type", Value: mustMarshal("command")},
		{Key: "command", Value: mustMarshal(cfg.commandFor(existing))},
	}
	if cfg.Async {
		obj.set("async", mustMarshal(true))
	}
	return obj
}

// hookEventConfigs defines which Claude Code events we subscribe to and their matcher patterns.
var hookEventConfigs = []claudeHookEventConfig{
	{Event: "SessionStart", Async: true},
	{Event: "UserPromptSubmit", Async: true},
	// Issue #1225/#1226 ACTIVATION: Stop is SYNCHRONOUS so Claude Code reads the
	// {decision:"block",reason} the hook emits to inject busy-parent completions.
	// Audit B12 (global-flip risk) is mitigated by RUNTIME scope, not a per-session
	// install (hooks are per config-dir, shared by conductor + workers):
	//   - DrainForStopHook fast-returns with no block and ZERO ledger writes for any
	//     session with an empty inbox (every leaf/non-conductor session) — inert flip.
	//   - The MaxStopHookBlocks loop guard is crash-safe (B4) and fails safe on an
	//     absent stop_hook_active flag (B8), so it cannot be defeated into a loop.
	// Canary one conductor before flipping fleet-wide (GAP §5).
	// The Env marker is what lets the handler tell this sync install from a
	// stale async one (messaging audit P2-1).
	{Event: "Stop", Async: false, Env: StopHookSyncMarkerEnv + "=1"},
	// PermissionRequest is synchronous so the hook handler's stdout decision is
	// consulted by Claude Code. In headless / /remote-control contexts an async
	// hook with no UI fallback caused silent deny; the sync hook plus an
	// emitted allow decision (when DSP is detected) closes that gap. Status
	// tracking semantics are unchanged.
	{Event: "PermissionRequest", Async: false},
	{Event: "Notification", Matcher: "permission_prompt|elicitation_dialog", Async: true},
	{Event: "SessionEnd", Async: true},
	{Event: "PreCompact", Async: false},
}

// InjectClaudeHooks injects agent-deck hook entries into Claude Code's settings.json.
// Read-preserve-modify-write through an order-preserving object: every key,
// user hook and unknown field round-trips untouched, only the agent-deck
// entries are written, and the file is left alone when nothing would change.
// Returns true if hooks were newly installed, false if already present.
func InjectClaudeHooks(configDir string) (bool, error) {
	settingsPath := filepath.Join(configDir, "settings.json")
	root, data, err := readSettingsObject(settingsPath)
	if err != nil {
		return false, err
	}
	hooksObj, err := hooksSectionObject(root)
	if err != nil {
		return false, err
	}

	// Check if already installed (all events present with our hook command
	// AND pointing at this binary). A legacy bare entry, or an absolute entry
	// for a different binary, counts as drift and is rewritten in place.
	if hooksInstalledWithCommand(hooksObj.asMap(), true) {
		return false, nil
	}

	// Inject our hook entries for each event
	for _, cfg := range hookEventConfigs {
		existing, _ := hooksObj.get(cfg.Event)
		merged, err := mergeHookEvent(existing, cfg)
		if err != nil {
			return false, fmt.Errorf("settings.json hooks.%s: %w", cfg.Event, err)
		}
		hooksObj.set(cfg.Event, merged)
	}
	root.set("hooks", mustMarshal(hooksObj))

	finalData, err := indentSettings(root)
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	if bytes.Equal(finalData, data) {
		return false, nil
	}

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}

	if err := atomicfile.WriteFile(settingsPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write settings.json: %w", err)
	}

	sessionLog.Info("claude_hooks_installed", slog.String("config_dir", configDir))
	return true, nil
}

// readSettingsObject reads settingsPath as an order-preserving object and
// returns it with the raw bytes read. A missing file is an empty object with
// nil data; malformed JSON is an error (nothing is ever written over it).
func readSettingsObject(settingsPath string) (jsonObject, []byte, error) {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return jsonObject{}, nil, nil
		}
		return nil, nil, fmt.Errorf("read settings.json: %w", err)
	}
	var root jsonObject
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("parse settings.json: %w", err)
	}
	return root, data, nil
}

// hooksSectionObject returns root's "hooks" object (empty when absent); a
// "hooks" value that is not an object is an error rather than overwritten.
func hooksSectionObject(root jsonObject) (jsonObject, error) {
	hooksObj := jsonObject{}
	raw, ok := root.get("hooks")
	if !ok {
		return hooksObj, nil
	}
	if err := json.Unmarshal(raw, &hooksObj); err != nil {
		return nil, fmt.Errorf("parse settings.json hooks: %w", err)
	}
	return hooksObj, nil
}

// RemoveClaudeHooks removes agent-deck hook entries from Claude Code's settings.json.
// Returns true if hooks were removed, false if none found.
func RemoveClaudeHooks(configDir string) (bool, error) {
	settingsPath := filepath.Join(configDir, "settings.json")
	root, data, err := readSettingsObject(settingsPath)
	if err != nil {
		return false, err
	}
	if data == nil {
		return false, nil
	}
	hooksObj, err := hooksSectionObject(root)
	if err != nil {
		return false, err
	}

	removed := false
	for _, cfg := range hookEventConfigs {
		raw, ok := hooksObj.get(cfg.Event)
		if !ok {
			continue
		}
		cleaned, didRemove := removeAgentDeckFromEvent(raw)
		if !didRemove {
			continue
		}
		removed = true
		if cleaned == nil {
			hooksObj.del(cfg.Event)
		} else {
			hooksObj.set(cfg.Event, cleaned)
		}
	}
	if !removed {
		return false, nil
	}

	// If hooks map is empty, remove the key entirely
	if len(hooksObj) == 0 {
		root.del("hooks")
	} else {
		root.set("hooks", mustMarshal(hooksObj))
	}

	finalData, err := indentSettings(root)
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	if err := atomicfile.WriteFile(settingsPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write settings.json: %w", err)
	}

	sessionLog.Info("claude_hooks_removed", slog.String("config_dir", configDir))
	return true, nil
}

// CheckClaudeHooksInstalled checks if agent-deck hooks are present in
// settings.json with the current config (no drift).
func CheckClaudeHooksInstalled(configDir string) bool {
	hooks, err := readClaudeHooksSection(configDir)
	return err == nil && hooksAlreadyInstalled(hooks)
}

// CheckClaudeHooksPresent reports whether every event carries an agent-deck
// entry in ANY recognised form (bare, pinned, marker-less, dangling, async
// drift). Review round 3 (finding 4): an install made by `hooks install` and
// never through the TUI is consent all the same, so the TUI's startup check
// uses this to decide whether to PROMPT (drift is repaired silently, never
// re-asked); CheckClaudeHooksInstalled says whether a repair is due.
func CheckClaudeHooksPresent(configDir string) bool {
	hooks, err := readClaudeHooksSection(configDir)
	return err == nil && hooksPresent(hooks)
}

// hooksPresent is CheckClaudeHooksPresent on a decoded hooks section.
func hooksPresent(hooks map[string]json.RawMessage) bool {
	for _, cfg := range hookEventConfigs {
		raw, ok := hooks[cfg.Event]
		if !ok || !eventHasAgentDeckHook(raw, cfg.Matcher) {
			return false
		}
	}
	return true
}

// eventHasAgentDeckHook reports whether the event's matcher block matcher
// holds an agent-deck entry in any form.
func eventHasAgentDeckHook(raw json.RawMessage, matcher string) bool {
	var matchers []claudeHookMatcher
	if err := json.Unmarshal(raw, &matchers); err != nil {
		return false
	}
	for _, m := range matchers {
		if m.Matcher != matcher {
			continue
		}
		for _, h := range m.Hooks {
			if isAgentDeckHookCommand(h.Command) {
				return true
			}
		}
	}
	return false
}

// hooksAlreadyInstalled checks if all required agent-deck hooks are present
// AND their config (Matcher and Async flag) matches the current
// hookEventConfigs table. Returns false on any drift so a subsequent
// InjectClaudeHooks call will merge the current config in.
//
// Pre-2026-04-29 this only checked presence, which let stale Async flags
// from older binary versions linger across upgrades. The PermissionRequest
// async-true to async-false flip in the /remote-control parity fix
// surfaced the bug: settings.json kept the old flag because hooks install
// no-opped on "already installed."
//
// The command is accepted in any recognised form (bare or absolute). This is
// the check the TUI runs at startup before silently re-installing, and a bare
// install must keep counting as installed there: otherwise every developer
// build started as a TUI would rewrite the operator's hooks to point at
// itself. Only the explicit install (hooksInstalledWithCommand) treats a
// command that differs from this binary as drift.
//
// Two things ARE drift even unpinned (review round 2, P1-A/P1-B): an absolute
// command whose program no longer exists (a Homebrew keg removed by the
// upgrade), and a Stop entry without the sync marker (installed before the
// marker existed). Both are repaired by HealClaudeHooks and by the TUI's
// accepted-reinstall path.
func hooksAlreadyInstalled(hooks map[string]json.RawMessage) bool {
	return hooksInstalledWithCommand(hooks, false)
}

// distinctAgentDeckHookCommands returns each agent-deck hook command found in
// hooks once, in first-seen event order, in any form (bare or absolute, with
// or without an Env marker). Empty when no agent-deck entry is installed.
func distinctAgentDeckHookCommands(hooks map[string]json.RawMessage) []string {
	seen := map[string]bool{}
	var out []string
	for _, cfg := range hookEventConfigs {
		var matchers []claudeHookMatcher
		if raw, ok := hooks[cfg.Event]; !ok || json.Unmarshal(raw, &matchers) != nil {
			continue
		}
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if isAgentDeckHookCommand(h.Command) && !seen[h.Command] {
					seen[h.Command] = true
					out = append(out, h.Command)
				}
			}
		}
	}
	return out
}

// hooksInstalledWithCommand is hooksAlreadyInstalled with the command pinned:
// when pinned, every agent-deck entry must equal the command the install
// would write in its place now (this binary's path, or for an unpinnable
// binary the entry's own program, plus any Env marker).
func hooksInstalledWithCommand(hooks map[string]json.RawMessage, pinned bool) bool {
	for _, cfg := range hookEventConfigs {
		raw, ok := hooks[cfg.Event]
		if !ok {
			return false
		}
		if !eventHasAgentDeckHookMatchingConfig(raw, cfg, pinned) {
			return false
		}
	}
	return true
}

// eventHasAgentDeckHookMatchingConfig checks both presence AND config match:
// the agent-deck hook entry must live under a matcher block whose Matcher
// field equals cfg.Matcher, its Async flag must match, its program must still
// exist when absolute, it must carry cfg.Env when the row has one, and — when
// pinned — its command must equal what the install would write in its place.
func eventHasAgentDeckHookMatchingConfig(raw json.RawMessage, cfg claudeHookEventConfig, pinned bool) bool {
	var matchers []claudeHookMatcher
	if err := json.Unmarshal(raw, &matchers); err != nil {
		return false
	}
	for _, m := range matchers {
		if m.Matcher != cfg.Matcher {
			continue
		}
		for _, h := range m.Hooks {
			if !isAgentDeckHookCommand(h.Command) {
				continue
			}
			if pinned && h.Command != cfg.commandFor(h.Command) {
				return false
			}
			if hookCommandProgramMissing(h.Command) || !hookCommandHasEnv(h.Command, cfg.Env) {
				return false
			}
			return h.Async == cfg.Async
		}
	}
	return false
}

// hookCommandHasEnv reports whether command carries the VAR=value prefix env
// in front of its program (always true for an empty env).
func hookCommandHasEnv(command, env string) bool {
	if env == "" {
		return true
	}
	words, ok := shellwords.Split(command)
	if !ok {
		return false
	}
	for _, w := range words {
		if w == env {
			return true
		}
		if !isEnvAssignmentWord(w) {
			return false
		}
	}
	return false
}

// hookCommandProgramMissing reports whether command names an absolute program
// that no longer exists on disk (a dangling pin). A bare command is resolved
// through PATH at hook time and is never reported missing here.
func hookCommandProgramMissing(command string) bool {
	program := hookCommandProgram(command)
	if !filepath.IsAbs(program) {
		return false
	}
	_, err := os.Stat(program)
	return err != nil
}

// mergeHookEvent adds agent-deck's hook to an existing event's matcher array,
// or updates the one already there in place. Every other matcher and hook
// object passes through verbatim (review round 3, finding 1); an event value
// that is not an array of matcher objects is an error.
func mergeHookEvent(existing json.RawMessage, cfg claudeHookEventConfig) (json.RawMessage, error) {
	var matchers []jsonObject
	if existing != nil {
		if err := json.Unmarshal(existing, &matchers); err != nil {
			return nil, err
		}
	}

	for i, m := range matchers {
		if m.getString("matcher") != cfg.Matcher {
			continue
		}
		var hooks []jsonObject
		if raw, ok := m.get("hooks"); ok {
			if err := json.Unmarshal(raw, &hooks); err != nil {
				return nil, err
			}
		}
		// Update our existing entry to the current config (command/async),
		// or append if it is missing. In-place update covers the
		// binary-upgrade case where the hookEventConfigs table changes
		// (e.g., flipping Async from true to false) and the persisted
		// settings.json must drift to follow.
		replaced := false
		for j, h := range hooks {
			if command := h.getString("command"); isAgentDeckHookCommand(command) {
				hooks[j] = agentDeckHookObject(cfg, command)
				replaced = true
				break
			}
		}
		if !replaced {
			hooks = append(hooks, agentDeckHookObject(cfg, ""))
		}
		m.set("hooks", mustMarshal(hooks))
		matchers[i] = m
		return mustMarshal(matchers), nil
	}

	// No matching matcher found; add a new one
	newMatcher := jsonObject{}
	if cfg.Matcher != "" {
		newMatcher.set("matcher", mustMarshal(cfg.Matcher))
	}
	newMatcher.set("hooks", mustMarshal([]jsonObject{agentDeckHookObject(cfg, "")}))
	matchers = append(matchers, newMatcher)
	return mustMarshal(matchers), nil
}

// removeAgentDeckFromEvent removes agent-deck hook entries from an event's
// matcher array. Returns cleaned JSON and whether any removal happened; a
// matcher left with no hooks by the removal is dropped, every other matcher
// and hook passes through verbatim. Returns nil JSON if the array is empty.
func removeAgentDeckFromEvent(raw json.RawMessage) (json.RawMessage, bool) {
	var matchers []jsonObject
	if err := json.Unmarshal(raw, &matchers); err != nil {
		return raw, false
	}

	removed := false
	var cleaned []jsonObject
	for _, m := range matchers {
		var hooks []jsonObject
		if hooksRaw, ok := m.get("hooks"); ok {
			if err := json.Unmarshal(hooksRaw, &hooks); err != nil {
				cleaned = append(cleaned, m)
				continue
			}
		}
		var kept []jsonObject
		for _, h := range hooks {
			if isAgentDeckHookCommand(h.getString("command")) {
				removed = true
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) == len(hooks) {
			cleaned = append(cleaned, m) // untouched
			continue
		}
		if len(kept) == 0 {
			continue // matcher had only our hooks; drop it entirely
		}
		m.set("hooks", mustMarshal(kept))
		cleaned = append(cleaned, m)
	}

	if !removed {
		return raw, false
	}
	if len(cleaned) == 0 {
		return nil, true
	}
	return mustMarshal(cleaned), true
}
