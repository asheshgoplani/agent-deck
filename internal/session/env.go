package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// buildEnvSourceCommand builds shell commands to source .env files before the main command.
// Returns empty string if no env files or theme vars are configured.
// Order of sourcing (later overrides earlier):
//  1. Theme environment (COLORFGBG) for terminal-aware tools
//  2. Global [shell].env_files (in order)
//  3. [shell].init_script (for direnv, nvm, etc.)
//  4. Tool-specific env_file ([claude].env_file, [gemini].env_file, [tools.X].env_file)
//  5. Per-group / per-conductor inline env ([groups.X.claude].env, [conductors.X.claude].env)
//  6. Inline env vars from [tools.X].env
//  7. Conductor-specific env from meta.json (highest priority, overrides tool env)
//  8. Strip TELEGRAM_STATE_DIR (v1.7.40, S8)
//
// Note: This does NOT handle [shell].launch_shell wrapping — that happens at the
// prepareCommand layer (instance.go) after env sourcing, so the shell startup
// files run first and THEN the env files/init_script are sourced inline.
func (i *Instance) buildEnvSourceCommand() string {
	var sources []string

	// 1. Theme environment (COLORFGBG) so tools like Codex detect light/dark theme.
	// Set early so env files or init scripts can override if needed.
	// For sandboxed sessions, COLORFGBG is injected via docker exec environment
	// forwarding (collectDockerEnvVars) instead of inline shell export. Inline
	// export uses a semicolon-containing value (e.g. "15;0") that becomes fragile
	// under nested bash -c quoting chains used by sandbox command wrappers.
	if !i.IsSandboxed() {
		if themeExport := themeEnvExport(); themeExport != "" {
			sources = append(sources, themeExport)
		}
	}

	config, cfgErr := LoadUserConfig()
	if cfgErr != nil {
		// A broken config.toml degrades EVERY per-group / per-conductor /
		// global tool override to defaults, and the parse error is
		// discarded at most call sites — historically "my env_file stopped
		// injecting" with zero diagnostics. Surface it in the pane (the
		// spawn still proceeds on defaults) and in the debug log.
		sessionLog.Warn("user config load failed at spawn; config.toml overrides inactive",
			slog.String("session", i.Title),
			slog.String("error", cfgErr.Error()))
		sources = append(sources, paneWarning("config.toml error — overrides inactive: "+cfgErr.Error()))
	}
	if config == nil {
		if len(sources) == 0 {
			return ""
		}
		return strings.Join(sources, " && ") + " && "
	}

	ignoreMissing := config.Shell.GetIgnoreMissingEnvFiles()

	// 2. Global env_files from [shell] section
	for _, envFile := range config.Shell.EnvFiles {
		resolved := resolvePath(envFile, i.ProjectPath)
		sources = append(sources, buildSourceCmd(resolved, ignoreMissing))
	}

	// 3. Shell init script (direnv, nvm, pyenv, etc.)
	if config.Shell.InitScript != "" {
		script := config.Shell.InitScript
		if isFilePath(script) {
			resolved := ExpandPath(script)
			sources = append(sources, buildSourceCmd(resolved, ignoreMissing))
		} else {
			// Inline command (e.g., 'eval "$(direnv hook bash)"')
			sources = append(sources, script)
		}
	}

	// 4. Tool-specific env_file
	toolEnvFile := i.getToolEnvFile()
	if toolEnvFile != "" {
		resolved := resolvePath(toolEnvFile, i.ProjectPath)
		// CodeQL alert 54 (uncontrolled data in path expression): env_file is
		// operator config and flows unsanitized into the os.Stat path sink
		// below. Gate the stat behind a boundary-aware, fail-closed check —
		// probedPath is returned only when the resolved file sits under a root
		// agent-deck legitimately probes (the operator's home or the session's
		// project tree). The stat is purely a proactive "configured-but-missing"
		// diagnostic; the file is sourced regardless via buildSourceCmd below
		// (whose `[ -f ]` guard covers a genuinely missing file), so an env_file
		// the operator deliberately placed outside those roots skips the probe
		// but is still sourced — no feature is lost and tainted config never
		// reaches the filesystem sink unvalidated. Mirrors ValidateTranscriptPath
		// (#1435) and the conductor_migrate_dir.go containment precedent.
		if probedPath, ok := validateEnvFileForProbe(resolved, i.ProjectPath); ok {
			if _, statErr := os.Stat(probedPath); statErr != nil {
				// An explicitly configured env_file that is absent at spawn is a
				// misconfiguration, not a soft default — the silent `[ -f ] &&`
				// skip below is exactly how a typo'd env_file stanza goes
				// unnoticed. Warn in the pane and the debug log; the
				// ignore_missing_env_files=false hard-fail path is unchanged.
				sessionLog.Warn("configured env_file missing at spawn",
					slog.String("session", i.Title),
					slog.String("tool", i.Tool),
					slog.String("env_file", probedPath))
				if ignoreMissing {
					sources = append(sources, paneWarning("env_file not found: "+probedPath))
				}
			}
		}
		sources = append(sources, buildSourceCmd(resolved, ignoreMissing))
	}

	// 5. Per-group / per-conductor inline env for claude sessions
	//    ([groups.X.claude].env / [conductors.X.claude].env). Exported AFTER
	//    the env_file source (step 4) so an inline key deterministically
	//    wins over the same key from the file; the conductor map is applied
	//    over the group map (CFG-08 precedence: conductor > group). Same
	//    tool gate as the claude branch of getToolEnvFile.
	if i.Tool == "claude" {
		if claudeEnv := i.getClaudeInlineEnv(config); claudeEnv != "" {
			sources = append(sources, claudeEnv)
		}
	}

	// 6. Inline env vars from [tools.X].env
	if inlineEnv := i.getToolInlineEnv(); inlineEnv != "" {
		sources = append(sources, inlineEnv)
	}

	// 7. Conductor-specific env (highest priority, overrides tool env)
	if conductorEnv := i.getConductorEnv(ignoreMissing); conductorEnv != "" {
		sources = append(sources, conductorEnv)
	}

	// 8. S8 (v1.7.40) — strip TELEGRAM_STATE_DIR on every non-channel-owning
	// claude spawn. Fires AFTER all sources and inline env so it wins
	// over any env_file / inline export that set the variable, and
	// runs even when no env_file is in play (covers `agent-deck
	// launch` children outside the conductor's group triangle).
	// Subsumes the narrower issue #680 predicate.
	if stripExpr := telegramStateDirStripExpr(i); stripExpr != "" {
		sources = append(sources, stripExpr)
	}

	if len(sources) == 0 {
		return ""
	}

	// Join all sources with && and add trailing && for the main command
	return strings.Join(sources, " && ") + " && "
}

// themeEnvExport returns a shell export command for COLORFGBG based on the
// resolved agent-deck theme. This allows terminal-aware tools (Codex, vim, etc.)
// running inside tmux to detect the correct light/dark background.
// Returns empty string if the parent terminal already has COLORFGBG set and
// it matches the resolved theme (avoid unnecessary override).
func themeEnvExport() string {
	theme := ResolveTheme()

	// Determine the COLORFGBG value for the resolved theme.
	// Format: "foreground;background" using terminal color indices.
	// Background >= 8 signals a light terminal to most tools.
	var colorfgbg string
	switch theme {
	case "light":
		colorfgbg = "0;15" // black on white
	default:
		colorfgbg = "15;0" // white on black
	}

	// If the parent terminal already has the matching COLORFGBG, propagate
	// its exact value (it may encode more nuance than our synthetic value).
	if existing := os.Getenv("COLORFGBG"); existing != "" {
		if matchesTheme, ok := colorfgbgMatchesTheme(existing, theme); ok && matchesTheme {
			colorfgbg = existing
		}
	}

	return fmt.Sprintf("export COLORFGBG='%s'", colorfgbg)
}

// ThemeColorFGBG returns the COLORFGBG value for the current resolved theme.
// Used by tmux session setup to persist the value via set-environment.
func ThemeColorFGBG() string {
	theme := ResolveTheme()
	if existing := os.Getenv("COLORFGBG"); existing != "" {
		if matchesTheme, ok := colorfgbgMatchesTheme(existing, theme); ok && matchesTheme {
			return existing
		}
	}
	if theme == "light" {
		return "0;15"
	}
	return "15;0"
}

// colorfgbgMatchesTheme checks if a COLORFGBG value matches the given theme.
// Returns (matches, parsedOK). Background index >= 8 is considered light.
func colorfgbgMatchesTheme(colorfgbg, theme string) (bool, bool) {
	idx := strings.LastIndex(colorfgbg, ";")
	if idx < 0 {
		return false, false
	}
	bgStr := colorfgbg[idx+1:]
	var bg int
	if _, err := fmt.Sscanf(bgStr, "%d", &bg); err != nil {
		return false, false
	}
	isLight := bg >= 8
	return (theme == "light") == isLight, true
}

// paneWarning returns a shell command that prints an agent-deck warning to
// the pane's stderr. It always exits 0 so it can sit in the `&&` source
// chain without blocking the spawn — loud, not fatal. Newlines are
// flattened so a multi-line TOML parse error stays one pane line.
func paneWarning(msg string) string {
	msg = strings.ReplaceAll(msg, "\n", " ")
	escaped := strings.ReplaceAll("agent-deck: warning: "+msg, "'", "'\\''")
	return fmt.Sprintf("echo '%s' >&2", escaped)
}

// buildSourceCmd creates a shell command to source a file.
// If ignoreMissing is true, wraps in a file existence check.
func buildSourceCmd(path string, ignoreMissing bool) string {
	if ignoreMissing {
		// Use [ -f file ] && source file pattern for safe sourcing
		return fmt.Sprintf(`[ -f "%s" ] && source "%s"`, path, path)
	}
	return fmt.Sprintf(`source "%s"`, path)
}

// resolvePath resolves a user-specified config file path:
//   - Expands environment variables ($HOME, ${VAR}, etc.)
//   - Expands ~ prefix to home directory
//   - Absolute paths are returned as-is
//   - Relative paths are resolved relative to workDir
func resolvePath(path, workDir string) string {
	expanded := ExpandPath(path)
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded)
	}
	return filepath.Clean(filepath.Join(workDir, expanded))
}

// validateEnvFileForProbe is the boundary guard for the os.Stat sink in
// buildEnvSourceCommand (CodeQL alert 54). It returns the cleaned path together
// with ok=true ONLY when the resolved env_file is an absolute path containing no
// traversal segment AND sitting under a root agent-deck legitimately probes: the
// operator's home directory or the session's own project tree. Any other path
// (relative, traversal-bearing, or outside every known root) returns ok=false so
// the caller fails closed and never stats it.
//
// This intentionally guards only the proactive existence probe, NOT the sourcing
// of the file — the spawned shell sources whatever path the operator configured
// (buildSourceCmd), so containing the probe costs no functionality while keeping
// uncontrolled config data from reaching the filesystem sink unvalidated. The
// shape (Clean → reject ".." → HasPrefix(root) containment, fail-closed) mirrors
// ValidateTranscriptPath and resolveCanonical/pathContains in
// conductor_migrate_dir.go.
func validateEnvFileForProbe(resolved, projectPath string) (string, bool) {
	clean := filepath.Clean(resolved)
	// Only fully-resolved absolute paths are eligible; a relative or
	// traversal-bearing path cannot be proven contained, so fail closed.
	if !filepath.IsAbs(clean) {
		return "", false
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) ||
		strings.Contains(clean, string(os.PathSeparator)+".."+string(os.PathSeparator)) ||
		strings.HasSuffix(clean, string(os.PathSeparator)+"..") {
		return "", false
	}

	var roots []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if h := filepath.Clean(home); filepath.IsAbs(h) {
			roots = append(roots, h)
		}
	}
	if projectPath != "" {
		if p := filepath.Clean(projectPath); filepath.IsAbs(p) {
			roots = append(roots, p)
		}
	}

	for _, root := range roots {
		if clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return clean, true
		}
	}
	return "", false
}

// ExpandPath expands environment variables and ~ prefix in a path.
// Use resolvePath when relative paths also need to be resolved against a working directory.
func ExpandPath(path string) string {
	// Step 1: Expand environment variables first.
	// This ensures $HOME/.env becomes /home/user/.env before the tilde
	// check, and handles ${VAR} in any position (including after ~/).
	path = os.ExpandEnv(path)

	// Step 2: Expand tilde prefix to home directory.
	// After env var expansion, any remaining ~ is a genuine tilde.
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}

	return path
}

// isFilePath checks if a string looks like a file path (vs inline command).
func isFilePath(s string) bool {
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "~/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "~")
}

// getToolInlineEnv returns shell export commands for inline env vars from [tools.X].env.
// Returns empty string if the tool has no inline env vars defined.
// Keys are sorted for deterministic output. Single quotes in values are escaped.
func (i *Instance) getToolInlineEnv() string {
	def := GetToolDef(i.Tool)
	if def == nil || len(def.Env) == 0 {
		return ""
	}

	// Sort keys for deterministic ordering
	keys := make([]string, 0, len(def.Env))
	for k := range def.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build export statements with single-quote escaping
	exports := make([]string, 0, len(keys))
	for _, k := range keys {
		v := def.Env[k]
		// Escape single quotes: replace ' with '\'' (end quote, escaped quote, start quote)
		escaped := strings.ReplaceAll(v, "'", "'\\''")
		exports = append(exports, fmt.Sprintf("export %s='%s'", k, escaped))
	}

	return strings.Join(exports, " && ")
}

// getClaudeInlineEnv returns shell export statements for the merged
// per-group / per-conductor inline env map ([groups.X.claude].env,
// [conductors.X.claude].env). Merge order (later wins per key): ancestor
// groups root-first → exact group → conductor block. Keys are sorted for
// deterministic output; invalid env names are skipped and single quotes in
// values are escaped — same rules as the conductor meta.json env
// (getConductorEnv) and [tools.X].env (getToolInlineEnv).
func (i *Instance) getClaudeInlineEnv(config *UserConfig) string {
	if config == nil {
		return ""
	}
	merged := config.GetGroupClaudeEnv(i.GroupPath) // freshly allocated; safe to overlay
	if name := conductorNameFromInstance(i); name != "" {
		if conductorEnv := config.GetConductorClaudeEnv(name); len(conductorEnv) > 0 {
			if merged == nil {
				merged = make(map[string]string, len(conductorEnv))
			}
			for k, v := range conductorEnv {
				merged[k] = v
			}
		}
	}
	if len(merged) == 0 {
		return ""
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	exports := make([]string, 0, len(keys))
	for _, k := range keys {
		if !isValidEnvKey(k) {
			continue // skip invalid env var names
		}
		escaped := strings.ReplaceAll(merged[k], "'", "'\\''")
		exports = append(exports, fmt.Sprintf("export %s='%s'", k, escaped))
	}
	return strings.Join(exports, " && ")
}

// getToolEnvFile returns the env_file setting for the current tool.
// For Claude sessions, group-specific env_file takes priority over global [claude].env_file.
func (i *Instance) getToolEnvFile() string {
	config, _ := LoadUserConfig()
	if config == nil {
		return ""
	}

	switch i.Tool {
	case "claude":
		// Conductor block wins over group (CFG-08 precedence chain).
		// NOTE: This is separate from getConductorEnv below which sources
		// conductor meta.json env_file. Both can be set; the TOML path
		// sources here (via buildEnvSourceCommand step 4) and meta.json
		// sources later (step 6 — overrides). CFG-08 layer, not a
		// replacement for the meta.json layer.
		if name := conductorNameFromInstance(i); name != "" {
			if conductorEnv := config.GetConductorClaudeEnvFile(name); conductorEnv != "" {
				return conductorEnv
			}
		}
		if groupEnv := config.GetGroupClaudeEnvFile(i.GroupPath); groupEnv != "" {
			return groupEnv
		}
		return config.Claude.EnvFile
	case "gemini":
		return config.Gemini.EnvFile
	case "opencode":
		return config.OpenCode.EnvFile
	case "codex":
		return config.Codex.EnvFile
	case "copilot":
		return config.Copilot.EnvFile
	case "crush":
		return config.Crush.EnvFile
	case "hermes":
		if name := conductorNameFromInstance(i); name != "" {
			if conductorEnv := config.GetConductorHermesEnvFile(name); conductorEnv != "" {
				return conductorEnv
			}
		}
		if groupEnv := config.GetGroupHermesEnvFile(i.GroupPath); groupEnv != "" {
			return groupEnv
		}
		return config.Hermes.EnvFile
	default:
		// Check custom tools
		if def := GetToolDef(i.Tool); def != nil {
			return def.EnvFile
		}
	}
	return ""
}

// getConductorEnv returns shell export commands for conductor-specific env vars.
// Checks if this session is a conductor (title starts with "conductor-") and loads
// env and env_file from the conductor's meta.json.
func (i *Instance) getConductorEnv(ignoreMissing bool) string {
	name := strings.TrimPrefix(i.Title, "conductor-")
	if name == "" || name == i.Title {
		return "" // not a conductor session
	}
	meta, err := LoadConductorMeta(name)
	if err != nil {
		sessionLog.Warn("conductor_env_load_failed",
			slog.String("conductor", name),
			slog.String("error", err.Error()))
		return ""
	}

	var parts []string

	// Conductor env_file
	if meta.EnvFile != "" {
		resolved := resolvePath(meta.EnvFile, i.ProjectPath)
		parts = append(parts, buildSourceCmd(resolved, ignoreMissing))
	}

	// Conductor inline env vars
	if len(meta.Env) > 0 {
		keys := make([]string, 0, len(meta.Env))
		for k := range meta.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !isValidEnvKey(k) {
				continue // skip invalid env var names
			}
			parts = append(parts, fmt.Sprintf("export %s='%s'", k, strings.ReplaceAll(meta.Env[k], "'", "'\\''")))
		}
	}

	return strings.Join(parts, " && ")
}

// isValidEnvKey checks that a string is a valid environment variable name.
func isValidEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// telegramEnvVarsToStrip lists every TELEGRAM_* env var that the
// Claude Code telegram plugin reads. #680 / #955 / S8 only stripped
// TELEGRAM_STATE_DIR; #1133 broadens this to include TELEGRAM_BOT_TOKEN
// (and any future plugin var) so a child whose conductor exports the
// token can't re-derive plugin state and spawn a duplicate `bun
// telegram` poller. Order is deterministic for stable shell output.
var telegramEnvVarsToStrip = []string{
	"TELEGRAM_STATE_DIR",
	"TELEGRAM_BOT_TOKEN",
}

// telegramStateDirStripExpr returns an `unset TELEGRAM_STATE_DIR ...`
// clause for any claude spawn that is NOT a channel-owning telegram
// session. S8 (v1.7.40) broadens issue #680's narrow conductor-pairing
// predicate: every `agent-deck launch` child that doesn't own the
// telegram bot must lose TELEGRAM_*, otherwise it inherits the
// conductor's env, the telegram plugin (enabled globally per the v3
// topology) reads the conductor's .env, and a duplicate bun poller
// races the conductor on the same bot token → Telegram returns 409
// Conflict and messages drop for everyone.
//
// #1133 broadens further: TELEGRAM_BOT_TOKEN is now stripped too (the
// plugin re-derives state from the token alone). An explicit opt-in
// (Instance.InheritTelegramEnv, CLI `--inherit-telegram-env`) preserves
// the full env for the rare case of debugging the poller from a fork.
//
// Fires when ALL hold:
//  1. Tool is "claude" — TELEGRAM_* are Claude Code plugin env vars;
//     don't mutate codex / gemini spawns.
//  2. Title does NOT start with "conductor-". Conductors are the
//     legitimate bot owners even before `Channels` is set.
//  3. No entry in `Channels` carries the `plugin:telegram@` prefix.
//     Explicit per-session telegram opt-in keeps the variables.
//  4. InheritTelegramEnv is false. The #1133 escape hatch.
//
// Returned string is empty when no strip is needed, so callers can
// append it unconditionally to the sources slice. The function name
// retains "StateDir" for backward compatibility with callers in
// instance.go and the existing test surface; the body covers all
// telegram vars.
func telegramStateDirStripExpr(inst *Instance) string {
	if inst == nil {
		return ""
	}
	if inst.Tool != "claude" {
		return ""
	}
	if inst.InheritTelegramEnv {
		return "" // #1133 opt-in — keep the conductor's telegram env
	}
	if conductorNameFromInstance(inst) != "" {
		return "" // conductor session — owns the bot token
	}
	for _, ch := range inst.Channels {
		if strings.HasPrefix(ch, telegramChannelPrefix) {
			return "" // explicit telegram channel owner
		}
	}
	return "unset " + strings.Join(telegramEnvVarsToStrip, " ")
}

// telegramExecEnvStripFlags returns the `-u VAR -u VAR ...` argument
// list for the `env` exec wrapper at instance.go:758. Mirrors
// telegramStateDirStripExpr at the exec layer: same predicate, same
// var list, defense-in-depth against the shell `unset` somehow being
// bypassed.
func telegramExecEnvStripFlags(inst *Instance) string {
	if telegramStateDirStripExpr(inst) == "" {
		return ""
	}
	parts := make([]string, 0, len(telegramEnvVarsToStrip)*2)
	for _, v := range telegramEnvVarsToStrip {
		parts = append(parts, "-u", v)
	}
	return strings.Join(parts, " ")
}

// ScrubProcessEnvForChildLaunch removes TELEGRAM_* vars from the
// CURRENT process environment when the given instance represents a
// non-channel-owning claude child. Issue #955: `agent-deck launch`
// invoked from a conductor session inherits the conductor's
// TELEGRAM_STATE_DIR; without this strip the var propagates into the
// tmux server (which inherits the launching process env on first
// `new-session`) and from there into every subprocess in the new
// pane — Bash-tool spawns, fork claudes, restart respawn — even when
// the S8 exec-layer protects the immediate claude binary. Any of
// those descendants can load the Claude Code telegram plugin and
// start a second `bun telegram` poller against the conductor's bot,
// racing the conductor for the Bot API lock (HTTP 409) and silently
// dropping inbound messages.
//
// #1133 broadens the scrub: TELEGRAM_STATE_DIR alone left a hole —
// conductors that also exported TELEGRAM_BOT_TOKEN (the plugin
// re-derives state from the token) still leaked the duplicate poller.
// We now unset every var named in telegramEnvVarsToStrip *and* any
// other TELEGRAM_-prefixed var present in os.Environ. The prefix
// sweep means a future plugin env addition (TELEGRAM_API_HASH etc.)
// is covered without code change here.
//
// Reuses telegramStateDirStripExpr as the single source of truth for
// the strip predicate so this layer can never disagree with the
// shell-level and exec-level layers about which sessions own the
// telegram bot. No-op for conductors, explicit telegram channel
// owners, --inherit-telegram-env opt-ins, and non-claude tools.
func ScrubProcessEnvForChildLaunch(inst *Instance) {
	if telegramStateDirStripExpr(inst) == "" {
		return
	}
	for _, v := range telegramEnvVarsToStrip {
		_ = os.Unsetenv(v)
	}
	// Prefix sweep: catch any other TELEGRAM_* var the conductor
	// might have exported but the plugin doesn't strictly require.
	// Keeps the child env minimal so a plugin update can't surprise
	// us with a new poller-spawning var.
	for _, kv := range os.Environ() { //nolint:forbidigo // enumerates current env to Unsetenv TELEGRAM_*; not building a child env (childenv is the launch chokepoint)
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key := kv[:eq]
		if strings.HasPrefix(key, "TELEGRAM_") {
			_ = os.Unsetenv(key)
		}
	}
}
