// Package session — worker-scratch CLAUDE_CONFIG_DIR (issue #59, v1.7.68).
//
// Background. Conductors store the real telegram bot token under
// `~/.claude/channels/telegram/.env`. When a non-conductor claude
// worker is launched via `agent-deck launch` / `agent-deck add` on the
// same host, three layers of prior work stripped TELEGRAM_STATE_DIR
// from the child's env (issue #680 narrow, S8 broadened in v1.7.40).
// But the Telegram plugin is ENABLED GLOBALLY in the profile's
// `settings.json` — and without TSD it falls back to the default path
// `~/.claude/channels/telegram/`, which is the conductor's token dir.
// The worker reads the conductor's `.env`, spawns its own `bun
// telegram` poller, and the Bot API returns 409 Conflict when two
// pollers race the same token. Messages drop for everyone.
//
// Fix. Prepare an ephemeral scratch CLAUDE_CONFIG_DIR for every worker
// spawn. The scratch dir is a shallow mirror of the ambient profile:
// every entry is symlinked to the source EXCEPT `settings.json`,
// which is copied and mutated so
// `enabledPlugins["telegram@claude-plugins-official"] = false`. That
// pins the plugin OFF before it has a chance to load — categorically
// different from TSD stripping, which only moves its state dir.
//
// Scope. Applies to claude workers whose
// `telegramStateDirStripExpr(inst) != ""` (the existing predicate).
// Conductors, explicit telegram channel owners, and non-claude tools
// use the ambient profile as-is.
//
// Cleanup. `CleanupWorkerScratchConfigDir` removes the dir on
// session stop/remove — best-effort, no-op on first-time misses. The
// scratch dir lives under `~/.agent-deck/worker-scratch/<instance-id>/`.

package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const telegramPluginID = "telegram@claude-plugins-official"

// Issue #759: the worker-scratch indirection (#732) is only load-bearing
// when a real Telegram bot poller exists for a worker to race. Elsewhere
// it breaks per-group config_dir account isolation (macOS Claude keys
// OAuth credentials by the literal CLAUDE_CONFIG_DIR path).
// Package var so tests can override.
var hostHasTelegramConductor = func() bool {
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return false
	}
	return strings.TrimSpace(cfg.Conductor.Telegram.Token) != ""
}

// NeedsWorkerScratchConfigDir is true when a scratch CLAUDE_CONFIG_DIR
// must be prepared at spawn. Reasons:
//  1. Telegram poller defense (v1.7.68 / #759) — pin telegramPluginID off.
//  2. Per-session plugin enablement (RFC docs/rfc/PLUGIN_ATTACH.md) —
//     write enabledPlugins[<id>] = true without contaminating the ambient
//     profile or peer sessions.
//
// When both fire, EnsureWorkerScratchConfigDir combines the deny+allow lists.
func (i *Instance) NeedsWorkerScratchConfigDir() bool {
	return needsScratchForTelegram(i) || needsScratchForExplicitPlugins(i)
}

func needsScratchForTelegram(i *Instance) bool {
	if telegramStateDirStripExpr(i) == "" {
		return false
	}
	return hostHasTelegramConductor()
}

func needsScratchForExplicitPlugins(i *Instance) bool {
	if i == nil || i.Tool != "claude" {
		return false
	}
	return len(i.Plugins) > 0
}

func computeDenyList(i *Instance) []string {
	if needsScratchForTelegram(i) {
		return []string{telegramPluginID}
	}
	return nil
}

// allCatalogPluginIDs lets EnsureWorkerScratchConfigDir strip catalog-managed
// entries from inherited source state — so detach actually clears the entry
// in this session's scratch instead of inheriting the global default.
func allCatalogPluginIDs() map[string]struct{} {
	out := map[string]struct{}{}
	for _, def := range GetAvailablePlugins() {
		out[def.ID()] = struct{}{}
	}
	return out
}

// computeAllowList resolves Instance.Plugins to fully-qualified ids.
// Telegram-official is filtered defense-in-depth (also blocked at the
// catalog-read layer, RFC §6). Allow is applied AFTER deny so an explicit
// opt-in wins (irrelevant in v1; RFC PLUGIN_TELEGRAM_RETROFIT.md tracks v2).
func computeAllowList(i *Instance) []string {
	if i == nil || len(i.Plugins) == 0 {
		return nil
	}
	out := make([]string, 0, len(i.Plugins))
	for _, name := range i.Plugins {
		def := GetPluginDef(name)
		if def == nil {
			continue
		}
		if IsTelegramOfficialRefusal(def.Name, def.Source) {
			continue
		}
		out = append(out, def.ID())
	}
	return out
}

// WorkerScratchDirRoot returns the path that holds every worker's
// scratch config dir. Callers with a valid home should prefer
// workerScratchDirFor below which derives this from the effective
// HOME at call time.
func workerScratchDirRoot(home string) string {
	return filepath.Join(home, ".agent-deck", "worker-scratch")
}

func workerScratchDirFor(home, instanceID string) string {
	return filepath.Join(workerScratchDirRoot(home), instanceID)
}

// EnsureWorkerScratchConfigDir idempotently prepares the scratch
// CLAUDE_CONFIG_DIR. Returns "" (no error) when no scratch is needed —
// callers treat that as "use the ambient profile". The scratch mirrors
// sourceProfileDir via symlinks and rewrites settings.json with the
// deny+allow overlay. Source absence is fine — we emit a minimal
// settings.json.
func (i *Instance) EnsureWorkerScratchConfigDir(sourceProfileDir string) (string, error) {
	if !i.NeedsWorkerScratchConfigDir() {
		return "", nil
	}
	if i.ID == "" {
		return "", fmt.Errorf("EnsureWorkerScratchConfigDir: instance has no ID")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	scratch := workerScratchDirFor(home, i.ID)

	// 0o700: scratch settings.json holds plugin topology that shouldn't
	// be world-readable on a multi-user host.
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return "", fmt.Errorf("mkdir scratch: %w", err)
	}

	// Mutate settings.json with a deny ∪ allow overlay on enabledPlugins
	// (RFC §4.3). Any prior scratch state is clobbered — at spawn time
	// stale state is a liability.
	settings := map[string]interface{}{}
	if sourceProfileDir != "" {
		if data, readErr := os.ReadFile(filepath.Join(sourceProfileDir, "settings.json")); readErr == nil {
			_ = json.Unmarshal(data, &settings)
		}
	}
	// Wrong-shape enabledPlugins (e.g. legacy array form) → warn so users
	// notice the reset.
	plugins, _ := settings["enabledPlugins"].(map[string]interface{})
	if plugins == nil {
		if raw, present := settings["enabledPlugins"]; present && raw != nil {
			sessionLog.Warn("worker_scratch_enabledPlugins_unexpected_shape",
				slog.String("instance_id", i.ID),
				slog.String("source", sourceProfileDir),
				slog.String("got_type", fmt.Sprintf("%T", raw)),
				slog.String("hint", "scratch settings.json will reset enabledPlugins to an object; source profile may have used a non-object format"),
			)
		}
		plugins = map[string]interface{}{}
	}

	// Catalog plugins are managed EXCLUSIVELY per-session via Instance.Plugins.
	// Detached catalog ids must be set to false EXPLICITLY — Claude Code
	// scans plugins/cache/ and defaults installed-but-unspecified plugins
	// to enabled, so omission would bleed-through. Non-catalog plugins
	// pass through unchanged.
	catalogIDs := allCatalogPluginIDs()
	for id := range plugins {
		if _, isCatalog := catalogIDs[id]; isCatalog {
			delete(plugins, id)
		}
	}

	for _, id := range computeDenyList(i) {
		plugins[id] = false
	}
	allowSet := map[string]struct{}{}
	for _, id := range computeAllowList(i) {
		plugins[id] = true
		allowSet[id] = struct{}{}
	}
	for id := range catalogIDs {
		if _, attached := allowSet[id]; attached {
			continue
		}
		plugins[id] = false
	}
	settings["enabledPlugins"] = plugins

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal settings: %w", err)
	}
	// G5: atomic write so concurrent Ensure calls don't tear settings.json.
	if err := atomicWriteFile(filepath.Join(scratch, "settings.json"), out, 0o600); err != nil {
		return "", fmt.Errorf("write scratch settings: %w", err)
	}

	if sourceProfileDir != "" {
		if err := mirrorProfileEntries(scratch, sourceProfileDir); err != nil {
			return "", err
		}
	}

	return scratch, nil
}

// mirrorProfileEntries symlinks every top-level entry in source (except
// settings.json) into dest. Idempotent: existing dest entries are left
// alone; G6 EEXIST races are benign.
func mirrorProfileEntries(dest, source string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read source profile: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "settings.json" {
			continue
		}
		linkPath := filepath.Join(dest, name)
		if _, statErr := os.Lstat(linkPath); statErr == nil {
			continue
		}
		target := filepath.Join(source, name)
		if err := os.Symlink(target, linkPath); err != nil {
			if os.IsExist(err) {
				continue
			}
			return fmt.Errorf("symlink %s: %w", name, err)
		}
	}
	return nil
}

// CleanupWorkerScratchConfigDir removes the scratch dir. Best-effort.
func (i *Instance) CleanupWorkerScratchConfigDir() {
	if i.WorkerScratchConfigDir == "" {
		return
	}
	_ = os.RemoveAll(i.WorkerScratchConfigDir)
	i.WorkerScratchConfigDir = ""
}

// prepareWorkerScratchConfigDirForSpawn is the spawn-path wrapper
// around EnsureWorkerScratchConfigDir. Called from Start(),
// StartWithMessage(), and the restart fallback path. Best-effort —
// a failure here falls back to the ambient profile rather than
// blocking the spawn, with a warning to the session log.
//
// On darwin, when the scratch dir is created BECAUSE OF Plugins
// (not telegram), emits a one-shot loud warning per (host, source-profile)
// pair about Claude Code's path-keyed OAuth credential store
// (RFC docs/rfc/PLUGIN_ATTACH.md §7, issue #759 successor).
//
// Order contract (RFC §4.6 / fix C1): plugin auto-install runs BEFORE
// the scratch dir is built, because the scratch's plugins/ symlink
// captures source-profile state at scratch creation time. If install
// ran second, the scratch would symlink an empty source profile and
// claude would start with enabledPlugins[<id>]=true but without the
// plugin code reachable, until the next restart rebuilt scratch.
func (i *Instance) prepareWorkerScratchConfigDirForSpawn() {
	if !i.NeedsWorkerScratchConfigDir() {
		return
	}
	sourceDir := GetClaudeConfigDirForInstance(i)

	// Step 1: install plugin code into the SOURCE profile (not scratch).
	// Best-effort — failures log but don't block. Runs first so the
	// subsequent scratch's plugins/ symlink resolves to a populated tree.
	if len(i.Plugins) > 0 {
		_ = i.EnsurePluginsInstalled(sourceDir)
	}

	// Step 2: macOS warning if the scratch is plugin-driven on a host
	// without a TG conductor.
	if needsScratchForExplicitPlugins(i) && !needsScratchForTelegram(i) {
		maybeEmitMacOSScratchWarning(sourceDir)
	}

	// Step 3: build the scratch dir. By this point the source profile
	// has the plugin code so symlinks resolve correctly.
	scratch, err := i.EnsureWorkerScratchConfigDir(sourceDir)
	if err != nil {
		sessionLog.Warn("worker_scratch_prepare_failed",
			slog.String("instance_id", i.ID),
			slog.String("source", sourceDir),
			slog.String("error", err.Error()),
		)
		return
	}
	i.WorkerScratchConfigDir = scratch
}

// macOSScratchWarningEmitter is the package-level seam that lets tests
// observe and override the warning emission. Real callers go through
// maybeEmitMacOSScratchWarning which is darwin-gated and state-cached.
var macOSScratchWarningEmitter func(sourceProfileDir string) = emitMacOSScratchWarningToStderr

// maybeEmitMacOSScratchWarning is a no-op on non-darwin and a one-shot
// per-(host, sourceProfileDir) pair on darwin. Cache lives in
// `~/.agent-deck/state.json` under the key
// `macos_plugin_scratch_warning_shown[<sourceProfileDir>]` so a second
// session re-using the same source profile silently skips the warning.
//
// Best-effort: state-file errors (read or write) do NOT block the
// session. Worst case: warning is shown twice.
func maybeEmitMacOSScratchWarning(sourceProfileDir string) {
	if runtimeGOOS() != "darwin" {
		return
	}
	already, _ := readMacOSScratchWarningFlag(sourceProfileDir)
	if already {
		return
	}
	macOSScratchWarningEmitter(sourceProfileDir)
	_ = writeMacOSScratchWarningFlag(sourceProfileDir)
}

// runtimeGOOS is exposed as a var so tests can pretend to be darwin
// without rebuilding under GOOS=darwin.
var runtimeGOOS = func() string { return goosNative() }

// goosNative returns runtime.GOOS — exposed as a function rather than a
// const so the runtimeGOOS package var can shadow it in tests without
// touching real OS detection.
func goosNative() string { return runtime.GOOS }

// macOSWarningStateFile is the single-flag JSON state file recording
// which source profile dirs already showed the macOS plugin-scratch
// warning. Lives at `~/.agent-deck/macos-plugin-warning-state.json`.
//
// Schema: { "shown": { "<source-profile-dir>": true, ... } }
//
// Best-effort everywhere — read errors degrade to "not yet shown",
// write errors degrade to "may show twice". No mandate-level guard.
func macOSWarningStateFile() (string, error) {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "macos-plugin-warning-state.json"), nil
}

type macosWarningState struct {
	Shown map[string]bool `json:"shown"`
}

func readMacOSScratchWarningFlag(sourceProfileDir string) (bool, error) {
	path, err := macOSWarningStateFile()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var state macosWarningState
	if err := json.Unmarshal(data, &state); err != nil {
		return false, err
	}
	return state.Shown[sourceProfileDir], nil
}

func writeMacOSScratchWarningFlag(sourceProfileDir string) error {
	path, err := macOSWarningStateFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	state := macosWarningState{Shown: map[string]bool{}}
	if data, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(data, &state)
		if state.Shown == nil {
			state.Shown = map[string]bool{}
		}
	}
	state.Shown[sourceProfileDir] = true
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	// Atomic temp+rename — defends against symlink overwrite (G1) and
	// concurrent-writer races (G5). os.WriteFile would follow symlinks
	// and clobber whatever they point at; rename(2) replaces the path
	// atomically without dereferencing the original.
	return atomicWriteFile(path, out, 0o600)
}

// atomicWriteFile writes data to path via a temp file in the same
// directory, then renames atomically. The rename is symlink-safe:
// rename(2) does not follow the destination, so a malicious symlink
// at `path` is replaced rather than dereferenced.
//
// Used for shared state files that may be written concurrently by two
// agent-deck processes (TUI + CLI / two TUI windows).
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		// On any error path, ensure the temp file doesn't linger.
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func emitMacOSScratchWarningToStderr(sourceProfileDir string) {
	const banner = "" +
		"┌─ NOTICE: per-session plugin scratch on macOS ──────────────────┐\n" +
		"│ This session enables plugins via a per-session CLAUDE_CONFIG_DIR. │\n" +
		"│ On macOS, Claude Code keys OAuth credentials to the literal     │\n" +
		"│ config-dir path, so this session may show \"login required.\"     │\n" +
		"│                                                                  │\n" +
		"│ If that happens:                                                 │\n" +
		"│   1. Open a regular shell                                        │\n" +
		"│   2. Run: CLAUDE_CONFIG_DIR=<scratch-path> claude                │\n" +
		"│   3. Authenticate                                                │\n" +
		"│   4. Restart this agent-deck session                             │\n" +
		"│                                                                  │\n" +
		"│ See: docs/rfc/PLUGIN_ATTACH.md §7                                │\n" +
		"└──────────────────────────────────────────────────────────────────┘\n"
	fmt.Fprint(os.Stderr, banner)
	sessionLog.Warn("macos_plugin_scratch_warning_shown",
		slog.String("source_profile_dir", sourceProfileDir),
	)
}
