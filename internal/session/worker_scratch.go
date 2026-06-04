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
// scratch dir lives under the effective worker-scratch data directory.

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
	return configDeclaresTelegram(cfg)
}

// configDeclaresTelegram reports whether the host runs a telegram conductor
// under EITHER the legacy single-bot topology OR the modern per-conductor
// env_file topology (issue #1163).
//
// The legacy field `[conductor.telegram].token` is empty under the 7-bot
// setup — each conductor declares its bot via a `[conductors.<name>].claude.env_file`
// whose .envrc exports TELEGRAM_STATE_DIR. Reading only the legacy field left
// the scratch-pin gate (#759/#1137) permanently disarmed, so conductor-spawned
// children inherited the conductor's telegram=true CLAUDE_CONFIG_DIR and fired
// duplicate default-bot pollers. Detecting the modern topology re-arms the gate
// for every spawn path.
func configDeclaresTelegram(cfg *UserConfig) bool {
	if cfg == nil {
		return false
	}
	// Legacy single-bot token.
	if strings.TrimSpace(cfg.Conductor.Telegram.Token) != "" {
		return true
	}
	// Modern topology: any conductor whose env_file defines TELEGRAM_STATE_DIR.
	for _, c := range cfg.Conductors {
		ef := strings.TrimSpace(c.Claude.EnvFile)
		if ef == "" {
			continue
		}
		data, err := os.ReadFile(ExpandPath(ef))
		if err != nil {
			continue // missing/unreadable env_file is not a telegram declaration
		}
		if strings.Contains(string(data), "TELEGRAM_STATE_DIR") {
			return true
		}
	}
	return false
}

// NeedsWorkerScratchConfigDir is true when a scratch CLAUDE_CONFIG_DIR
// must be prepared at spawn. Reasons:
//  1. Telegram poller defense (v1.7.68 / #759) — pin telegramPluginID off.
//  2. Per-session plugin enablement (RFC docs/rfc/PLUGIN_ATTACH.md) —
//     write enabledPlugins[<id>] = true without contaminating the ambient
//     profile or peer sessions.
//  3. GLOBAL_ANTIPATTERN guard (issue #941) — channel-owning conductor with
//     `enabledPlugins.telegram=true` already in the ambient settings.json.
//     The TelegramValidator surfaces this as DOUBLE_LOAD but warnings
//     don't prevent the spawn; the scratch pins telegram off so --channels
//     is the only activation source and exactly one bun poller runs.
//
// When multiple reasons fire, EnsureWorkerScratchConfigDir combines the
// deny+allow lists.
func (i *Instance) NeedsWorkerScratchConfigDir() bool {
	return needsScratchForTelegram(i) ||
		needsScratchForExplicitPlugins(i) ||
		needsScratchForGlobalChannelConflict(i) ||
		needsScratchForTelegramChannelOwner(i)
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

// needsScratchForGlobalChannelConflict fires for issue #941: a
// channel-owning conductor session whose ambient profile has
// `enabledPlugins."telegram@claude-plugins-official" = true`.
// Without intervention, claude loads the plugin twice (once from the
// global setting, once from --channels) and two bun pollers race for
// the same bot token → 409 Conflict.
//
// We can't just rely on the user disabling the global flag — the rule
// is documented but not enforced. This predicate detects the topology
// and triggers a scratch CLAUDE_CONFIG_DIR that pins telegram off.
// --channels remains the only activation, yielding exactly one poller.
func needsScratchForGlobalChannelConflict(i *Instance) bool {
	if i == nil || i.Tool != "claude" {
		return false
	}
	if !sessionHasTelegramChannel(i) {
		return false
	}
	sourceDir := GetClaudeConfigDirForInstance(i)
	if sourceDir == "" {
		return false
	}
	return globalTelegramEnablementSet(sourceDir)
}

func sessionHasTelegramChannel(i *Instance) bool {
	for _, ch := range i.Channels {
		if strings.HasPrefix(ch, telegramChannelPrefix) {
			return true
		}
	}
	return false
}

// globalTelegramEnablementSet reports whether the source profile's
// settings.json has enabledPlugins."telegram@claude-plugins-official"=true.
// Missing files, parse errors, or absent keys all return false — we
// only fire the scratch guard when the antipattern is unambiguously present.
func globalTelegramEnablementSet(sourceProfileDir string) bool {
	data, err := os.ReadFile(filepath.Join(sourceProfileDir, "settings.json"))
	if err != nil {
		return false
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
	v, ok := plugins[telegramPluginID].(bool)
	return ok && v
}

func computeDenyList(i *Instance) []string {
	// Issue #1134: channel-owning sessions MUST keep their channel
	// plugin enabled — `--channels` is a routing/wiring directive and
	// claude only opens the MCP stdio transport when the plugin is
	// enabled in settings.json. Denying telegram here causes the bun
	// child to spawn in task-mode (no MCP handshake) and crash-respawn,
	// taking Telegram inbound offline for the conductor.
	// computeChannelPluginAllowList re-enables the plugin in scratch
	// settings.json; we must also stop denying it here so the deny
	// pass doesn't overwrite the allow.
	if sessionHasTelegramChannel(i) {
		return nil
	}
	if needsScratchForTelegram(i) || needsScratchForGlobalChannelConflict(i) {
		return []string{telegramPluginID}
	}
	return nil
}

// computeChannelPluginAllowList returns plugin IDs that this session
// references via .Channels and MUST therefore be ENABLED in the
// scratch settings.json. Channel plugins are wired by claude's
// `--channels` arg AFTER the plugin's MCP server starts; if the plugin
// is disabled in settings.json the server never starts, `--channels`
// has nothing to wire, and bun crashes in a respawn loop. Issue #1134.
//
// Today only telegram is a channel plugin; if other channel plugins
// land in the future, add their (channel-prefix, plugin-id) pairs
// here. The allow list is applied AFTER computeAllowList so it cannot
// be silently dropped by the catalog default-false pass.
func computeChannelPluginAllowList(i *Instance) []string {
	if i == nil {
		return nil
	}
	for _, ch := range i.Channels {
		if strings.HasPrefix(ch, telegramChannelPrefix) {
			return []string{telegramPluginID}
		}
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
	dir, err := dataPath("worker-scratch", "worker-scratch")
	if err != nil {
		return filepath.Join(os.TempDir(), "agent-deck", "worker-scratch")
	}
	return dir
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
	// Issue #1134: channel-owning sessions need their channel plugin
	// enabled so claude's `--channels` flag has a live MCP server to
	// wire its routing to. Applied AFTER computeAllowList so the
	// catalog default-false pass below treats channel plugins as
	// attached and leaves them true.
	for _, id := range computeChannelPluginAllowList(i) {
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

// credentialsFileName is the profile's OAuth credentials file. It is the one
// scratch entry that must be RE-ASSERTED (not merely left alone) on every
// seeding — see reassertCredentialSymlink and issue #1222.
const credentialsFileName = ".credentials.json"

// mirrorProfileEntries symlinks every top-level entry in source (except
// settings.json) into dest. For most entries it is idempotent the simple way:
// an existing dest entry is left alone (G6 EEXIST races are benign).
//
// `.credentials.json` is the exception. It is handled by
// reassertCredentialSymlink, which heals the issue #1222 clobber: running
// `/login` inside a managed session replaces the scratch symlink with a
// real-file COPY of the OAuth token. Anthropic rotates the refresh token on
// each refresh, so that stale copy 401s on the next rotation while the fresh
// token is stranded in scratch. Re-asserting the symlink (and promoting a
// fresh in-session login to canonical first) restores the single-source-of-
// truth invariant on the next start/restart/resume.
func mirrorProfileEntries(dest, source string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		if os.IsNotExist(err) {
			// Source absent: still re-assert credentials in case scratch
			// holds a stranded in-session login to promote (no canonical).
			return reassertCredentialSymlink(dest, source)
		}
		return fmt.Errorf("read source profile: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		// settings.json is OWNED (copied + mutated) by the scratch seeding;
		// .credentials.json is re-asserted explicitly below. Both are skipped
		// from the generic leave-alone mirror.
		if name == "settings.json" || name == credentialsFileName {
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
	return reassertCredentialSymlink(dest, source)
}

// reassertCredentialSymlink guarantees dest/.credentials.json is a clean symlink
// to source/.credentials.json (the single canonical profile credentials),
// healing the in-session `/login` clobber described in issue #1222.
//
//   - dest entry is the correct symlink → left untouched (idempotent).
//   - dest entry is absent → symlinked to canonical (when canonical exists).
//   - dest entry is a symlink to the WRONG target → repointed to canonical.
//   - dest entry is a REAL FILE (the `/login` clobber, or any diverged copy) →
//     force-replaced with a symlink to canonical, regardless of mtime.
//     Canonical is NEVER overwritten.
//   - dest entry is a real file but canonical is ABSENT → left intact, so the
//     only token copy is not stranded behind a dangling symlink. The next
//     start, once canonical exists, replaces it with the symlink.
//
// Why no mtime-promote (removed; see the OAuth root-cause memo and the
// disassembly of Claude v2.1.159 in /tmp/oauth-fix/SUBSCRIPTION-FIX.md):
// Anthropic OAuth uses single-use ROTATING refresh tokens, so a scratch copy's
// mtime is NOT a "this token is newer/valid" signal. The previous code promoted
// a strictly-newer scratch real-file to canonical, which could overwrite a good
// canonical with a stale (or already-rotated-out) scratch token and fork a
// second rotation chain — re-introducing the `invalid_grant` /login race for
// every session sharing that profile. Claude re-reads .credentials.json on
// expiry and serializes refreshes on a cross-process lock keyed by
// realpath(); collapsing every scratch to ONE canonical symlink is what makes
// that lock actually serialize the workers. The load-bearing invariant is
// therefore "the scratch credentials must always be a symlink to the one
// canonical file, never a real file", and we enforce it on every
// spawn/start/restart here.
//
// WARNING for operators: do NOT run `/login` inside a managed agent-deck
// session — it writes a real file that diverges from canonical until the next
// restart relinks it (the in-session token is dropped, NOT promoted). Log in
// once in the canonical profile (e.g. `CLAUDE_CONFIG_DIR=~/.claude claude` →
// /login) and every session inherits it through this symlink.
func reassertCredentialSymlink(dest, source string) error {
	target := filepath.Join(source, credentialsFileName)
	linkPath := filepath.Join(dest, credentialsFileName)

	li, lerr := os.Lstat(linkPath)
	switch {
	case lerr != nil && os.IsNotExist(lerr):
		// Nothing in scratch yet — link to canonical when it exists.
		if _, terr := os.Stat(target); terr == nil {
			return symlinkReplace(target, linkPath)
		}
		return nil
	case lerr != nil:
		return fmt.Errorf("lstat scratch credentials: %w", lerr)
	}

	if li.Mode()&os.ModeSymlink != 0 {
		// Already a symlink — leave it iff it points at canonical.
		if cur, rerr := os.Readlink(linkPath); rerr == nil && cur == target {
			return nil
		}
		// Wrong/stale symlink → repoint (only meaningful if canonical exists).
		if _, terr := os.Stat(target); terr != nil {
			return nil
		}
		return symlinkReplace(target, linkPath)
	}

	// dest is a REAL FILE — the `/login` clobber, or a diverged copy. Always
	// force-replace it with a clean symlink to canonical; NEVER promote its
	// contents (mtime is not a valid-token signal for rotating tokens).
	if _, terr := os.Stat(target); terr != nil {
		if os.IsNotExist(terr) {
			// No canonical to point at — leave the only token copy intact
			// rather than stranding it behind a dangling symlink. Once the
			// user logs in to canonical, the next start relinks this.
			return nil
		}
		return fmt.Errorf("stat canonical credentials: %w", terr)
	}
	return symlinkReplace(target, linkPath)
}

// symlinkReplace atomically points linkPath at target, removing any existing
// entry first. An EEXIST from a concurrent creator is benign.
func symlinkReplace(target, linkPath string) error {
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale credentials entry: %w", err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("symlink credentials: %w", err)
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

// applyWorkerScratchOverride is the single seam where the worker-scratch
// CLAUDE_CONFIG_DIR replaces the resolved one. Returns the effective
// config dir to use. Centralising the override here means every
// spawn-env builder (buildClaudeCommandWithMessage, buildBashExportPrefix,
// buildClaudeResumeCommand) logs the swap with identical wording. Issue
// #922 (reporter @bautrey) closed the silent-override hole by making
// this the only place the swap can happen.
func (i *Instance) applyWorkerScratchOverride(resolvedConfigDir string) string {
	if i.WorkerScratchConfigDir == "" {
		return resolvedConfigDir
	}
	sessionLog.Info("worker_scratch_override",
		slog.String("instance_id", i.ID),
		slog.String("resolved_config_dir", resolvedConfigDir),
		slog.String("worker_scratch_config_dir", i.WorkerScratchConfigDir),
	)
	return i.WorkerScratchConfigDir
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

	// Issue #941: surface the GLOBAL_ANTIPATTERN at spawn so operators can
	// flip enabledPlugins.telegram=false in their profile and stop relying
	// on this guard. Log-level WARN keeps it visible without blocking.
	if needsScratchForGlobalChannelConflict(i) {
		sessionLog.Warn("telegram_global_antipattern_suppressed",
			slog.String("instance_id", i.ID),
			slog.String("title", i.Title),
			slog.String("source_profile_dir", sourceDir),
			slog.String("plugin_id", telegramPluginID),
			slog.String("guidance", "enabledPlugins.\"telegram@claude-plugins-official\"=true in the ambient settings.json would have caused a duplicate bun telegram poller (issue #941). Pinning the plugin off in a per-session scratch config dir so --channels is the sole activation. Recommended fix: remove the global enablement and rely on --channels."),
		)
	}

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

	// Issue #1138: post-write verification. After the scratch
	// settings.json is rewritten, confirm the channel plugin is
	// actually enabled in the EFFECTIVE config dir (scratch when
	// present, ambient otherwise). Any failure here means `--channels`
	// would land on a disabled plugin and bun-telegram would never
	// spawn — surface it loudly so operators can heal manually if the
	// force-correct itself somehow drifted.
	effectiveDir := scratch
	if effectiveDir == "" {
		effectiveDir = sourceDir
	}
	if result := VerifyTelegramChannelEnabled(effectiveDir, i.Channels); !result.OK {
		EmitTelegramChannelDriftWarning(i.Title, i.ID, effectiveDir, i.Channels, result)
	}
}

// macOSScratchWarningEmitter is the package-level seam that lets tests
// observe and override the warning emission. Real callers go through
// maybeEmitMacOSScratchWarning which is darwin-gated and state-cached.
var macOSScratchWarningEmitter func(sourceProfileDir string) = emitMacOSScratchWarningToStderr

// maybeEmitMacOSScratchWarning is a no-op on non-darwin and a one-shot
// per-(host, sourceProfileDir) pair on darwin. Cache lives in
// the effective data directory so a second session re-using the same
// source profile silently skips the warning.
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
// warning. Lives in the effective data directory.
//
// Schema: { "shown": { "<source-profile-dir>": true, ... } }
//
// Best-effort everywhere — read errors degrade to "not yet shown",
// write errors degrade to "may show twice". No mandate-level guard.
func macOSWarningStateFile() (string, error) {
	return dataPath("macos-plugin-warning-state.json", "macos-plugin-warning-state.json")
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
