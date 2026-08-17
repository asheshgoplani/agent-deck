package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
)

// DeepSeek Harness (`dsh`) adapter.
//
// Upstream: github.com/deepseek-ai/deepseek-harness, published to npm as
// @deepseek-ai/dsh (MIT, developer preview). Everything below was verified
// against the real 0.1.0-rc.6 binary in a sandboxed HOME; nothing here is
// inferred from a blog post. See docs/tools/deepseek.md for the capture.
//
// # Invocation grammar (`dsh --help`)
//
//	dsh --profile <name> [--patch <path>]... [app args...]
//	dsh web [app args...]                      # hardcoded alias of --profile web
//	dsh --profile headless "<task>"            # one-shot: print final answer, exit
//	dsh plugin --profile <name> <pnpm args...> # plugin management (forwards to pnpm)
//	dsh --dump-config | --dump-default-config  # print the composed tree, boot nothing
//
// The launcher owns ONLY --profile/--patch/the dumps. The first token it does
// not recognize starts the booted app's own argument list, which that app
// parses and documents itself. Launcher flags therefore always come first —
// agent-deck emits them in that order and never interleaves app args.
//
// # Shipped profiles (rc.6)
//
//	web      --host <h> --port <p> --trusted-host <a>...  (long-lived HTTP server)
//	headless <task text>                                  (one-shot, then exits)
//
// There is deliberately no third "TUI" profile in the published package: the
// launcher's own --help advertises `dsh --profile tui --resume <id>` as an
// example "assuming the tui profile is installed", i.e. a profile the user adds
// with `dsh plugin --profile tui add <package>`. agent-deck therefore treats the
// profile name as the mode selector (DeepSeekProfileMode) and passes app
// arguments straight through for any profile it does not ship knowledge of,
// which is exactly the launcher's own contract.
//
// # Config home
//
// $DSH_HOME (default ~/.dsh) is the single user-data root:
//
//	$DSH_HOME/profiles/<name>/{package.json,cordis.yml,cordis.patch.yml}
//	$DSH_HOME/cordis.patch.yml                 machine-local patch layer
//	$DSH_HOME/.credentials.yaml, $DSH_HOME/.env
//	$DSH_HOME/storages/workspace.json          workspace -> sessionIds index
//	$DSH_HOME/sessions/<workspace-slug>/session-<uuid>/session.jsonl.zstd
//
// Exporting DSH_HOME is how agent-deck gives a session its own account slot,
// exactly as CODEX_HOME does for codex ([profiles.<account>.deepseek].config_dir).
//
// # Credentials
//
// DEEPSEEK_API_KEY in the launch environment, or the credentials document the
// web Models page writes. With neither, `dsh --profile headless` exits 1 with
// "MISSING_CREDENTIAL: ... no API key for provider route" on stderr — the string
// the auth-failure detector keys on.
//
// # Process contract
//
// SIGTERM starts a graceful drain and exits 0 (a supervisor's ordinary stop);
// SIGINT reports 130; a second signal forces immediate exit. The tree gets up
// to five seconds to dispose. Headless exits 0 when the final turn completed,
// else 1.
//
// # What dsh does NOT have (and what agent-deck therefore does not claim)
//
//   - No fork/branch command — CanFork stays false for deepseek.
//   - No `--resume` on either shipped profile. Resume is wired for profiles that
//     install an app accepting it, behind [deepseek].resume_flag (default empty
//     = off), with the session ID discovered from the on-disk workspace index.
//   - No hooks are enabled by default. The bundled hook bridges
//     (@deepseek-ai/dsh-hooks-claude-code / -codex) speak the Claude Code and
//     Codex hooks.json protocols, but a user must mount them in a patch layer,
//     so agent-deck does not auto-install hooks and falls back to pane-content
//     status detection.

// deepSeekBinary is the upstream binary name. The npm package is
// @deepseek-ai/dsh; the bin it installs is `dsh`.
const deepSeekBinary = "dsh"

// deepSeekDefaultProfile is the profile agent-deck boots when the user has not
// chosen one. `web` is the documented front door (`npx @deepseek-ai/dsh web`)
// and the only shipped profile that stays alive in a pane.
const deepSeekDefaultProfile = "web"

// deepSeekHomeDirName is the default user-data directory name, owned upstream by
// @deepseek-ai/dsh-home-paths (DSH_HOME_DIR_NAME).
const deepSeekHomeDirName = ".dsh"

// deepSeekWorkspaceIndex is the workspace storage document under $DSH_HOME that
// maps a workspace path to its ordered session IDs.
const deepSeekWorkspaceIndex = "storages/workspace.json"

// DeepSeekMode classifies a profile name into the app-argument family that
// profile's app parses. Unknown profiles are "custom": agent-deck passes
// configured extra args through verbatim, which is the launcher's own contract
// for an app it does not know.
type DeepSeekMode string

const (
	deepSeekModeWeb      DeepSeekMode = "web"
	deepSeekModeHeadless DeepSeekMode = "headless"
	deepSeekModeCustom   DeepSeekMode = "custom"
)

// DeepSeekProfileMode returns the app-argument family for a profile name.
func DeepSeekProfileMode(profile string) DeepSeekMode {
	switch strings.TrimSpace(strings.ToLower(profile)) {
	case "web", "":
		return deepSeekModeWeb
	case "headless":
		return deepSeekModeHeadless
	default:
		return deepSeekModeCustom
	}
}

// resolveDeepSeekCommand returns the dsh command this SESSION launches:
// the conductor override, then the group override (ancestor-walking), then the
// process-wide value.
//
// The scoped levels exist because [groups.<path>.deepseek].command and
// [conductors.<name>.deepseek].command are configurable; before PR #1942's
// review they were parsed and then ignored in favor of the global command,
// which is worse than not offering them — a per-group wrapper looked configured
// and never ran. Every other scoped DeepSeek setting (profile, config_dir,
// env_file) already walks this chain, so the command must too.
func (i *Instance) resolveDeepSeekCommand() string {
	cfg, _ := LoadUserConfig()
	if cfg != nil && i != nil {
		if name := conductorNameFromInstance(i); name != "" {
			if cmd := strings.TrimSpace(cfg.GetConductorDeepSeekCommand(name)); cmd != "" {
				return cmd
			}
		}
		if cmd := strings.TrimSpace(cfg.GetGroupDeepSeekCommand(i.GroupPath)); cmd != "" {
			return cmd
		}
	}
	return GetDeepSeekCommand()
}

// GetDeepSeekCommand returns the process-wide dsh command/alias, falling back to
// the bare binary name. Session-scoped resolution goes through
// Instance.resolveDeepSeekCommand; this is the "no instance in hand" answer that
// backs the installed-tools probe and `agent-deck deepseek status`.
func GetDeepSeekCommand() string {
	userConfig, _ := LoadUserConfig()
	if userConfig != nil && strings.TrimSpace(userConfig.DeepSeek.Command) != "" {
		return strings.TrimSpace(userConfig.DeepSeek.Command)
	}
	return deepSeekBinary
}

// --- binary identity ---------------------------------------------------------
//
// `dsh` is NOT a name DeepSeek coined. Debian and Ubuntu have shipped a `dsh`
// since long before this harness existed:
//
//	Package: dsh — "dancer's shell, or distributed shell"
//	executes specified command on a group of computers using remote shell
//	(universe/net, maintainer Junichi Uekawa <dancer@debian.org>)
//
// So a bare `dsh` on PATH is not evidence of DeepSeek Harness. Without a check,
// a host with that package and no harness would report DeepSeek as installed and
// then run `dsh --profile web` against a REMOTE-EXECUTION tool. The other
// DeepSeek-named projects this integration declines to support collide on the
// PACKAGE name and are therefore harmless; this one collides on the COMMAND
// name, which is the one that actually gets executed.

// deepSeekIdentityMarker is the string DeepSeek Harness prints in its own
// launcher help. Captured from 0.1.0-rc.6:
//
//	dsh: boot a DeepSeek Harness profile — an ordered stack of plugin-bundle
//	patch layers under your own overrides.
//
// Dancer's dsh prints distributed-shell usage and cannot contain it.
const deepSeekIdentityMarker = "DeepSeek Harness"

// deepSeekIdentityTimeout bounds the identity probe. --help neither boots a
// profile nor touches the network, so this only guards a wedged or unrelated
// binary that never returns.
var deepSeekIdentityTimeout = 3 * time.Second

// deepSeekIdentityCache memoizes the verdict per resolved path for the process
// lifetime. The probe runs at registry construction (only when
// show_only_installed_tools is on), and a binary does not change identity under
// us; re-execing it on every dialog open would be a visible cost for no answer.
var deepSeekIdentityCache sync.Map // resolved path -> bool

// deepSeekBinaryIsHarness reports whether the binary at path is DeepSeek Harness
// rather than some other program named dsh.
//
// Returns false when the binary cannot be run, times out, or prints help that
// does not identify itself — "cannot confirm" is reported as "not it", because
// the whole point is to stop agent-deck from launching an unidentified `dsh`.
func deepSeekBinaryIsHarness(path string) bool {
	if cached, ok := deepSeekIdentityCache.Load(path); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), deepSeekIdentityTimeout)
	defer cancel()
	// #nosec G204 -- path is the PATH-resolved default binary name, not runtime
	// user input, and the only argument is a fixed literal.
	cmd := exec.CommandContext(ctx, path, "--help")
	// WaitDelay force-closes the pipes shortly after the context is cancelled so
	// a wedged binary holding stdout cannot keep Output() blocked past the
	// timeout (same guard as captureHermesSessionID).
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	ok := err == nil && strings.Contains(string(out), deepSeekIdentityMarker)
	deepSeekIdentityCache.Store(path, ok)
	return ok
}

// DeepSeekInstalled reports whether a usable DeepSeek Harness is on this host.
//
// When the configured command is the bare default, the binary's identity is
// verified (see above). When the user has configured their own command, it is
// taken at its word: they named that program deliberately, and a wrapper is not
// obliged to reproduce upstream's help text.
func DeepSeekInstalled(command string) bool {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return false
	}
	if trimmed != deepSeekBinary {
		return probeInstalled(trimmed)
	}
	path, err := lookPathFn(trimmed)
	if err != nil {
		return false
	}
	return deepSeekBinaryIsHarness(path)
}

// defaultDeepSeekHome returns ~/.dsh, the upstream default when $DSH_HOME is
// unset. Returns "" when the home directory cannot be resolved.
func defaultDeepSeekHome() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, deepSeekHomeDirName)
}

// getDeepSeekHomeDir resolves the process-wide DSH_HOME: the env var the user's
// shell exports, then [deepseek].config_dir, then the upstream default ~/.dsh.
// This is the "no instance in hand" chain; per-instance resolution adds the
// account/conductor/group levels on top (deepSeekHomeToExport).
func getDeepSeekHomeDir() string {
	if env := strings.TrimSpace(os.Getenv("DSH_HOME")); env != "" {
		return ExpandPath(env)
	}
	if cfg, _ := LoadUserConfig(); cfg != nil && strings.TrimSpace(cfg.DeepSeek.ConfigDir) != "" {
		return ExpandPath(strings.TrimSpace(cfg.DeepSeek.ConfigDir))
	}
	return defaultDeepSeekHome()
}

// isDeepSeekHomeExplicit reports whether any level of the process-wide chain
// actually names a home, as opposed to falling through to ~/.dsh. Only an
// explicit value is exported into the pane: exporting the default would pin a
// path that dsh would have resolved identically, and would mask a DSH_HOME the
// user's own profile script sets later.
func isDeepSeekHomeExplicit() bool {
	if strings.TrimSpace(os.Getenv("DSH_HOME")) != "" {
		return true
	}
	cfg, _ := LoadUserConfig()
	return cfg != nil && strings.TrimSpace(cfg.DeepSeek.ConfigDir) != ""
}

// accountDeepSeekHomeDir returns the DSH_HOME mapped to this session's account
// slot via [profiles.<account>.deepseek].config_dir, or "" when the session has
// no account or the account names no deepseek block.
//
// Mirrors accountCodexHomeDir (#1922): the per-session account is the most
// specific thing a user can say about which credentials a session runs under, so
// it outranks the process-wide DSH_HOME and the global config.
func (i *Instance) accountDeepSeekHomeDir() string {
	if i == nil || strings.TrimSpace(i.Account) == "" {
		return ""
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.GetProfileDeepSeekConfigDir(i.Account)
}

// conductorGroupDeepSeekHomeDir returns the conductor-then-group config_dir
// override for this instance, or "" when neither names one. Group lookup walks
// ancestors, matching GetGroupClaudeConfigDir semantics.
func (i *Instance) conductorGroupDeepSeekHomeDir() string {
	if i == nil {
		return ""
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return ""
	}
	if name := conductorNameFromInstance(i); name != "" {
		if dir := cfg.GetConductorDeepSeekConfigDir(name); dir != "" {
			return dir
		}
	}
	return cfg.GetGroupDeepSeekConfigDir(i.GroupPath)
}

// deepSeekHomeToExport returns the DSH_HOME this session must launch with, or ""
// when nothing should be exported and dsh may resolve its own default.
//
// Priority, most specific first:
//
//  1. [profiles.<account>.deepseek].config_dir   (the session's account slot)
//  2. [conductors.<name>.deepseek].config_dir
//  3. [groups."<path>".deepseek].config_dir      (ancestor-walking)
//  4. $DSH_HOME from the launching environment
//  5. [deepseek].config_dir
//
// Both the launch and restart builders call this so they cannot drift — a
// session that resolves its home one way and launches another is the #1922
// failure class.
func (i *Instance) deepSeekHomeToExport() string {
	if accountHome := strings.TrimSpace(i.accountDeepSeekHomeDir()); accountHome != "" {
		return accountHome
	}
	if scoped := strings.TrimSpace(i.conductorGroupDeepSeekHomeDir()); scoped != "" {
		return scoped
	}
	if isDeepSeekHomeExplicit() {
		return strings.TrimSpace(getDeepSeekHomeDir())
	}
	return ""
}

// DeepSeekHomeDirForInstance returns the DSH_HOME this session reads and writes,
// falling back to the process-wide resolution (and finally ~/.dsh) when nothing
// is explicit. Unlike deepSeekHomeToExport this NEVER returns "": callers that
// need to look inside the home (session discovery, the CLI status command) want
// the effective path, not the "should we export it" answer.
func (i *Instance) DeepSeekHomeDirForInstance() string {
	if explicit := i.deepSeekHomeToExport(); explicit != "" {
		return explicit
	}
	return getDeepSeekHomeDir()
}

// resolveDeepSeekProfile returns the profile this session boots: the per-session
// options, then the conductor/group override, then [deepseek].profile, then
// "web".
func (i *Instance) resolveDeepSeekProfile() string {
	if i != nil && len(i.ToolOptionsJSON) > 0 {
		if opts, err := UnmarshalDeepSeekOptions(i.ToolOptionsJSON); err == nil && opts != nil {
			if p := strings.TrimSpace(opts.Profile); p != "" {
				return p
			}
		}
	}
	cfg, _ := LoadUserConfig()
	if cfg != nil && i != nil {
		if name := conductorNameFromInstance(i); name != "" {
			if p := strings.TrimSpace(cfg.GetConductorDeepSeekProfile(name)); p != "" {
				return p
			}
		}
		if p := strings.TrimSpace(cfg.GetGroupDeepSeekProfile(i.GroupPath)); p != "" {
			return p
		}
	}
	if cfg != nil && strings.TrimSpace(cfg.DeepSeek.Profile) != "" {
		return strings.TrimSpace(cfg.DeepSeek.Profile)
	}
	return deepSeekDefaultProfile
}

// deepSeekResumeFlag returns the configured resume flag, or "" when resume is
// off. It is empty by default and deliberately so: neither shipped profile
// accepts a resume flag, and emitting one would make dsh exit with a usage
// error. A user who installs a terminal app that does accept one sets
// [deepseek].resume_flag = "--resume".
func deepSeekResumeFlag() string {
	cfg, _ := LoadUserConfig()
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.DeepSeek.ResumeFlag)
}

// DeepSeekOptions holds per-session launch options for DeepSeek Harness.
type DeepSeekOptions struct {
	// Profile is the $DSH_HOME/profiles/<name> to boot. Empty inherits the
	// conductor/group/global chain, which bottoms out at "web".
	Profile string `json:"profile,omitempty"`

	// Patches are extra --patch overlay paths applied after the profile layer,
	// in order. Repeatable upstream; repeatable here.
	Patches []string `json:"patches,omitempty"`

	// Host and Port are the web app's --host/--port. Ignored for other
	// profiles, whose apps parse their own flags. Port is a pointer because 0
	// is meaningful upstream ("let the OS pick a free one") and so cannot also
	// mean "unset".
	Host string `json:"host,omitempty"`
	Port *int   `json:"port,omitempty"`

	// TrustedHosts are repeatable --trusted-host authorities for the web app's
	// /api browser-trust fence.
	TrustedHosts []string `json:"trusted_hosts,omitempty"`

	// ExtraArgs are appended verbatim after every flag agent-deck derives, for
	// app flag families agent-deck does not model.
	ExtraArgs []string `json:"extra_args,omitempty"`
}

// ToolName returns "deepseek".
func (o *DeepSeekOptions) ToolName() string {
	return "deepseek"
}

// ToArgs returns the launcher + app arguments in the order dsh requires:
// launcher flags (--profile, --patch) first, app flags after. The profile is
// emitted by the caller (buildDeepSeekCommand) because it needs the resolved
// value from the conductor/group/global chain, not just this struct's field.
func (o *DeepSeekOptions) ToArgs() []string {
	var args []string
	if o == nil {
		return args
	}
	if p := strings.TrimSpace(o.Profile); p != "" {
		args = append(args, "--profile", p)
	}
	args = append(args, o.launcherPatchArgs()...)
	args = append(args, o.appArgs(DeepSeekProfileMode(o.Profile))...)
	return args
}

// launcherPatchArgs returns the repeatable --patch overlays.
func (o *DeepSeekOptions) launcherPatchArgs() []string {
	var args []string
	for _, p := range o.Patches {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			args = append(args, "--patch", trimmed)
		}
	}
	return args
}

// appArgs returns the booted app's own arguments for the given mode. The web
// app owns --host/--port/--trusted-host; every other app owns its own family,
// so only ExtraArgs applies there.
func (o *DeepSeekOptions) appArgs(mode DeepSeekMode) []string {
	var args []string
	if mode == deepSeekModeWeb {
		if h := strings.TrimSpace(o.Host); h != "" {
			args = append(args, "--host", h)
		}
		// Emitted whenever set, INCLUDING 0: upstream documents `--port 0` as
		// "let the OS pick a free one", so dropping it would silently rebind the
		// composed default instead of honoring the request.
		if o.Port != nil {
			args = append(args, "--port", strconv.Itoa(*o.Port))
		}
		for _, host := range o.TrustedHosts {
			if trimmed := strings.TrimSpace(host); trimmed != "" {
				args = append(args, "--trusted-host", trimmed)
			}
		}
	}
	for _, extra := range o.ExtraArgs {
		if trimmed := strings.TrimSpace(extra); trimmed != "" {
			args = append(args, trimmed)
		}
	}
	return args
}

// NewDeepSeekOptions creates DeepSeekOptions seeded from the global config.
func NewDeepSeekOptions(config *UserConfig) *DeepSeekOptions {
	opts := &DeepSeekOptions{}
	if config == nil {
		return opts
	}
	opts.Profile = strings.TrimSpace(config.DeepSeek.Profile)
	opts.Patches = append(opts.Patches, config.DeepSeek.Patches...)
	opts.Host = strings.TrimSpace(config.DeepSeek.Host)
	opts.Port = config.DeepSeek.Port
	opts.TrustedHosts = append(opts.TrustedHosts, config.DeepSeek.TrustedHosts...)
	opts.ExtraArgs = append(opts.ExtraArgs, config.DeepSeek.ExtraArgs...)
	return opts
}

// UnmarshalDeepSeekOptions deserializes DeepSeekOptions from the JSON wrapper.
func UnmarshalDeepSeekOptions(data json.RawMessage) (*DeepSeekOptions, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}

	if wrapper.Tool != "deepseek" {
		return nil, nil
	}

	var opts DeepSeekOptions
	if err := json.Unmarshal(wrapper.Options, &opts); err != nil {
		return nil, err
	}

	return &opts, nil
}

// --- on-disk session discovery ----------------------------------------------

// deepSeekWorkspaceDoc is the subset of $DSH_HOME/storages/workspace.json this
// package reads. Upstream writes `unit.version` 2; unknown future versions are
// still parsed field-by-field rather than rejected, because a version bump that
// keeps `tables.workspaces[].path/sessionIds` shaped the same must not silently
// disable resume.
type deepSeekWorkspaceDoc struct {
	// Global.ArchivedSessionIDs lists sessions the user archived. They stay in
	// their workspace's sessionIds, so resuming the "newest" entry without
	// consulting this list can reopen a conversation the user deliberately put
	// away (PR #1942 adversarial review).
	Global struct {
		ArchivedSessionIDs []string `json:"archivedSessionIds"`
	} `json:"global"`
	Tables struct {
		Workspaces map[string]struct {
			Path       string   `json:"path"`
			SessionIDs []string `json:"sessionIds"`
			UpdatedAt  string   `json:"updatedAt"`
		} `json:"workspaces"`
	} `json:"tables"`
}

// archivedSet indexes the archived ids for lookup.
func (d *deepSeekWorkspaceDoc) archivedSet() map[string]bool {
	if len(d.Global.ArchivedSessionIDs) == 0 {
		return nil
	}
	out := make(map[string]bool, len(d.Global.ArchivedSessionIDs))
	for _, id := range d.Global.ArchivedSessionIDs {
		out[strings.TrimSpace(id)] = true
	}
	return out
}

// DiscoverDeepSeekSessionID returns the most recent dsh session ID recorded for
// workspace under home, or "" when the index is missing, unreadable, names no
// matching workspace, or the session's directory is gone.
//
// This is how agent-deck learns the ID of the conversation a pane is attached
// to: dsh exports no session ID (no env var, no hook, no `sessions list`
// subcommand), but it does maintain a durable workspace index, and the session
// bodies sit beside it. Reading it is strictly better than parsing pane text.
//
// Every failure returns "" so a restart falls back to a fresh boot and never
// blocks — the same fail-closed posture as captureHermesSessionID.
func DiscoverDeepSeekSessionID(home, workspace string) string {
	home = strings.TrimSpace(home)
	workspace = strings.TrimSpace(workspace)
	if home == "" || workspace == "" {
		return ""
	}

	data, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(deepSeekWorkspaceIndex)))
	if err != nil {
		return ""
	}
	var doc deepSeekWorkspaceDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}

	// Compare against the resolved path: dsh records the absolute workspace root
	// it booted in, and agent-deck's EffectiveWorkingDir may reach the same
	// directory through a symlink (multi-repo sessions do exactly that).
	wantPath := resolvedPathKey(workspace)
	archived := doc.archivedSet()
	for _, ws := range doc.Tables.Workspaces {
		if resolvedPathKey(ws.Path) != wantPath {
			continue
		}
		// sessionIds is append-ordered, so the last entry is the newest — but
		// archiving does not remove it, so walk past any archived entry rather
		// than reopening a conversation the user put away.
		for idx := len(ws.SessionIDs) - 1; idx >= 0; idx-- {
			id := strings.TrimSpace(ws.SessionIDs[idx])
			if id == "" || archived[id] {
				continue
			}
			if deepSeekSessionDirExists(home, ws.Path, id) {
				return id
			}
		}
	}
	return ""
}

// resolvedPathKey returns a comparable key for a filesystem path: symlinks
// resolved when possible, otherwise the cleaned path. Never returns an error —
// a path that cannot be resolved (deleted, permission-denied) still compares
// equal to another spelling of itself.
func resolvedPathKey(path string) string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	return cleaned
}

// deepSeekSessionDirExists reports whether the session body for id is still on
// disk under home. dsh stores it at
// $DSH_HOME/sessions/<workspace-slug>/<session-id>/, and prunes are ordinary —
// resuming an ID whose body is gone is the #756 "stale sid" failure, so the
// check gates resume the same way codexRolloutExistsInHome does.
//
// The slug is upstream's own encoding of the workspace path and is not
// re-derived here: agent-deck scans the sessions root for the ID instead, which
// stays correct if upstream changes the slug rule.
func deepSeekSessionDirExists(home, workspacePath, id string) bool {
	root := filepath.Join(home, "sessions")
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(root, entry.Name(), id))
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// DeepSeekSessionIDs returns every session ID recorded for workspace under home,
// newest last, whether or not its body still exists. The CLI status command
// reports these; resume uses DiscoverDeepSeekSessionID, which additionally
// requires the body.
func DeepSeekSessionIDs(home, workspace string) []string {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(deepSeekWorkspaceIndex)))
	if err != nil {
		return nil
	}
	var doc deepSeekWorkspaceDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	wantPath := resolvedPathKey(workspace)
	var out []string
	for _, ws := range doc.Tables.Workspaces {
		if wantPath != "" && resolvedPathKey(ws.Path) != wantPath {
			continue
		}
		for _, id := range ws.SessionIDs {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	if wantPath == "" {
		// No workspace filter: the map iteration order is random, so sort for a
		// deterministic --json payload.
		sort.Strings(out)
	}
	return out
}

// --- command building --------------------------------------------------------

// buildDeepSeekCommand builds the launch command for DeepSeek Harness.
//
// Shape:
//
//	<env sourcing> AGENTDECK_*=... [DSH_HOME=...] dsh --profile <p> [--patch ...] [app args]
//
// Launcher flags precede app arguments because dsh's parser stops owning tokens
// at the first one it does not recognize.
//
// If baseCommand differs from the bare tool name it is a user-supplied
// passthrough command and is returned without flag injection, matching
// buildCrushCommand/buildHermesCommand. The AGENTDECK_* prefix is applied even
// then, so a wrapper script still finds its session (the #951 review finding).
func (i *Instance) buildDeepSeekCommand(baseCommand string) string {
	if i.Tool != "deepseek" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()
	envPrefix += fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%s AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ",
		i.ID, shellescape.Quote(i.Title), i.Tool, shellescape.Quote(sessionProfileEnvValue()))

	// Passthrough: a custom command from the CLI or config that is not the bare
	// binary name owns its own flags entirely.
	if trimmed := strings.TrimSpace(baseCommand); isDeepSeekPassthroughCommand(trimmed) {
		return envPrefix + i.deepSeekHomeExport() + trimmed
	}

	return envPrefix + i.deepSeekHomeExport() + i.deepSeekInvocation("")
}

// --- prompt delivery ---------------------------------------------------------
//
// The three profile shapes do not share a delivery channel, and treating them as
// one is what produced all three P1s on PR #1942's review. This type is the
// single answer to "how does a prompt reach this session", and every send path
// consults it.

// DeepSeekPromptDelivery describes how an initial prompt reaches a session.
type DeepSeekPromptDelivery string

const (
	// DeepSeekPromptCommandLine — the task IS the invocation. `dsh --profile
	// headless "<task>"` answers it and exits; there is no prompt to type into,
	// and launching without one is a usage error.
	DeepSeekPromptCommandLine DeepSeekPromptDelivery = "command-line"

	// DeepSeekPromptPane — the booted app owns a terminal prompt, so the
	// ordinary "wait for ready, then type" path is correct. This is what an
	// installed interactive profile gets.
	DeepSeekPromptPane DeepSeekPromptDelivery = "pane"

	// DeepSeekPromptUnsupported — there is no prompt at all. The web profile is
	// an HTTP server: text typed into its pane goes to the server process's
	// stdin and is gone. Accepting a message for it and "delivering" it that way
	// is silent data loss, so callers must refuse instead.
	DeepSeekPromptUnsupported DeepSeekPromptDelivery = "unsupported"
)

// deepSeekCommandIsPassthrough reports whether this session's own command is a
// user-supplied invocation rather than the bare binary. agent-deck injects no
// flags into one and, crucially, cannot reason about its SHAPE either: a wrapper
// named in [deepseek].command or typed at the CLI may ignore the configured
// profile entirely.
func (i *Instance) deepSeekCommandIsPassthrough() bool {
	return isDeepSeekPassthroughCommand(strings.TrimSpace(i.Command))
}

// isDeepSeekPassthroughCommand reports whether a command string is a
// user-supplied invocation rather than the bare binary (or the tool name, which
// GetToolCommand maps onto it).
func isDeepSeekPassthroughCommand(trimmed string) bool {
	return trimmed != "" && trimmed != deepSeekBinary && trimmed != "deepseek"
}

// deepSeekPromptDelivery classifies this session's profile.
//
// A custom-command passthrough is classified as pane delivery regardless of the
// configured profile: agent-deck did not build that invocation and has no
// grounds to claim the wrapper serves a browser UI or needs a positional task.
// Treating it like any other custom command is the honest default — refusing a
// prompt or demanding a task on the strength of a profile the wrapper may never
// pass through would be a guess dressed up as a contract.
func (i *Instance) deepSeekPromptDelivery() DeepSeekPromptDelivery {
	if i.deepSeekCommandIsPassthrough() {
		return DeepSeekPromptPane
	}
	switch DeepSeekProfileMode(i.resolveDeepSeekProfile()) {
	case deepSeekModeHeadless:
		return DeepSeekPromptCommandLine
	case deepSeekModeWeb:
		return DeepSeekPromptUnsupported
	default:
		return DeepSeekPromptPane
	}
}

// PromptDeliveryError returns a non-nil error when this session cannot receive a
// prompt at all, and nil when some channel exists (pane or command line).
//
// It is tool-agnostic by design: send paths call it without knowing about
// DeepSeek, and every other tool returns nil, so nothing else changes behavior.
// The message names the profile, the reason, and the two ways forward, because
// "refused" without a route out is only marginally better than losing the
// message.
func (i *Instance) PromptDeliveryError() error {
	if i == nil || i.Tool != "deepseek" {
		return nil
	}
	if i.deepSeekPromptDelivery() != DeepSeekPromptUnsupported {
		return nil
	}
	profile := i.resolveDeepSeekProfile()
	return fmt.Errorf(
		"deepseek profile %q serves a browser UI and has no terminal prompt, so a message sent to its pane would be silently discarded; "+
			"use the headless profile for one-shot tasks ([deepseek].profile = \"headless\", or a per-group override), "+
			"or open the URL the pane prints and ask there",
		profile)
}

// PromptRidesCommandLine reports whether an initial prompt must be embedded in
// the launch command rather than typed after start. Callers that would otherwise
// Start() first and send asynchronously (`launch --no-wait`) have to know this,
// or the task never reaches the process at all.
func (i *Instance) PromptRidesCommandLine() bool {
	if i == nil || i.Tool != "deepseek" {
		return false
	}
	return i.deepSeekPromptDelivery() == DeepSeekPromptCommandLine
}

// deepSeekHeadlessTaskRequiredError is the refusal for a headless launch with no
// task. Spawning `dsh --profile headless` instead would exit with dsh's own
// usage error and leave a failed pane to explain.
func (i *Instance) deepSeekHeadlessTaskRequiredError() error {
	return fmt.Errorf(
		"deepseek profile %q answers one task and exits, so it cannot be launched without one; "+
			"pass a task (agent-deck launch -c deepseek -m \"<task>\"), "+
			"or choose a profile that stays up ([deepseek].profile = \"web\", or an installed interactive profile)",
		i.resolveDeepSeekProfile())
}

// deepSeekStartCommand builds the launch command for a start or restart, with no
// caller-supplied prompt in hand.
//
// For the headless profile it replays the recorded task: that is what makes a
// restart reproduce the one-shot instead of rebuilding a taskless invocation dsh
// rejects. With no recorded task it refuses rather than spawning that invocation.
func (i *Instance) deepSeekStartCommand() (string, error) {
	if i.deepSeekPromptDelivery() != DeepSeekPromptCommandLine {
		return i.buildDeepSeekCommand(i.Command), nil
	}
	task := strings.TrimSpace(i.DeepSeekTask)
	if task == "" {
		return "", i.deepSeekHeadlessTaskRequiredError()
	}
	command, _ := i.buildDeepSeekCommandWithPrompt(i.Command, task)
	return command, nil
}

// buildDeepSeekCommandWithPrompt builds the launch command carrying an initial
// prompt. Only the headless profile takes a task positionally; for every other
// profile the prompt cannot ride the command line (the web app rejects unknown
// positionals, a custom app owns its own grammar), so the caller is told the
// prompt was NOT embedded and delivers it through the ordinary pane-send path.
//
// When the prompt IS embedded the task is recorded on the instance, so a later
// restart can replay the same one-shot (PR #1942 review, P1c).
//
// Returns (command, promptEmbedded), mirroring buildCodexCommandWithPrompt.
func (i *Instance) buildDeepSeekCommandWithPrompt(baseCommand, prompt string) (string, bool) {
	command := i.buildDeepSeekCommand(baseCommand)
	trimmedPrompt := strings.TrimSpace(prompt)
	if trimmedPrompt == "" {
		return command, false
	}
	// A custom-command passthrough owns its own grammar — appending a bare
	// positional could land anywhere.
	if isDeepSeekPassthroughCommand(strings.TrimSpace(baseCommand)) {
		return command, false
	}
	if DeepSeekProfileMode(i.resolveDeepSeekProfile()) != deepSeekModeHeadless {
		return command, false
	}
	i.DeepSeekTask = trimmedPrompt
	return command + " " + shellescape.Quote(trimmedPrompt), true
}

// deepSeekHomeExport returns the `DSH_HOME=<path> ` inline assignment for this
// session, or "". The directory is created first: dsh creates it itself on
// first boot, but a pane that dies before that leaves the operator with an
// account slot that exists in config and nowhere on disk (the codex precedent).
func (i *Instance) deepSeekHomeExport() string {
	home := i.deepSeekHomeToExport()
	if home == "" {
		return ""
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		sessionLog.Warn("deepseek_home_mkdir_failed",
			"path", home,
			"error", err.Error())
	}
	return "DSH_HOME=" + shellescape.Quote(home) + " "
}

// deepSeekInvocation renders `dsh <launcher flags> <app args>` for this session.
// resumeID, when non-empty AND [deepseek].resume_flag is set, is appended as an
// app argument.
func (i *Instance) deepSeekInvocation(resumeID string) string {
	cmd := i.resolveDeepSeekCommand()
	profile := i.resolveDeepSeekProfile()
	mode := DeepSeekProfileMode(profile)

	opts := i.deepSeekOptions()

	// Launcher flags first — always. `--profile` is emitted explicitly rather
	// than using the `dsh web` alias: the alias is a subcommand that rejects the
	// parent's launcher options, so one uniform spelling avoids that whole class
	// of usage error.
	parts := []string{"--profile", shellescape.Quote(profile)}
	for _, patch := range opts.launcherPatchArgs() {
		parts = append(parts, shellescape.Quote(patch))
	}

	// App arguments after.
	appArgs := opts.appArgs(mode)
	if flag := deepSeekResumeFlag(); flag != "" && strings.TrimSpace(resumeID) != "" {
		appArgs = append([]string{flag, strings.TrimSpace(resumeID)}, appArgs...)
	}
	for _, arg := range appArgs {
		parts = append(parts, shellescape.Quote(arg))
	}

	return cmd + " " + strings.Join(parts, " ")
}

// deepSeekOptions returns the effective per-session options: the session's own
// ToolOptionsJSON when present, else the global config defaults.
func (i *Instance) deepSeekOptions() *DeepSeekOptions {
	if i != nil && len(i.ToolOptionsJSON) > 0 {
		if opts, err := UnmarshalDeepSeekOptions(i.ToolOptionsJSON); err == nil && opts != nil {
			return opts
		}
	}
	cfg, _ := LoadUserConfig()
	return NewDeepSeekOptions(cfg)
}

// buildDeepSeekResumeCommand builds the restart command, resuming the discovered
// session when [deepseek].resume_flag names a flag the booted app accepts.
// Otherwise it is byte-identical to a fresh launch: dsh persists its own
// sessions under $DSH_HOME, so a re-boot in the same workspace keeps every
// conversation reachable even when the process cannot be told to reopen one.
func (i *Instance) buildDeepSeekResumeCommand() string {
	if i.Tool != "deepseek" {
		return i.Command
	}

	envPrefix := i.buildEnvSourceCommand()
	envPrefix += fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%s AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ",
		i.ID, shellescape.Quote(i.Title), i.Tool, shellescape.Quote(sessionProfileEnvValue()))

	if trimmed := strings.TrimSpace(i.Command); isDeepSeekPassthroughCommand(trimmed) {
		return envPrefix + i.deepSeekHomeExport() + trimmed
	}

	return envPrefix + i.deepSeekHomeExport() + i.deepSeekInvocation(i.DeepSeekSessionID)
}

// deepSeekRestartCommand builds the restart command. The headless profile
// replays its recorded task — for a one-shot that IS the restart — while every
// other profile re-boots through the ordinary resume builder. With no recorded
// task it refuses instead of rebuilding an invocation dsh rejects; CanRestart
// reports the same fact up front, so this is the backstop, not the only guard.
func (i *Instance) deepSeekRestartCommand() (string, error) {
	if i.deepSeekPromptDelivery() != DeepSeekPromptCommandLine {
		return i.buildDeepSeekResumeCommand(), nil
	}
	task := strings.TrimSpace(i.DeepSeekTask)
	if task == "" {
		return "", i.deepSeekHeadlessTaskRequiredError()
	}
	command, _ := i.buildDeepSeekCommandWithPrompt(i.Command, task)
	return command, nil
}

// refreshDeepSeekSessionID re-discovers the workspace's newest dsh session on
// every restart rather than caching one. Overwriting self-heals a stale ID: if
// the resumed session was pruned, the index yields the current newest, or "" —
// in which case the launch starts fresh instead of resuming a dead ID forever.
func (i *Instance) refreshDeepSeekSessionID() {
	if i == nil || i.Tool != "deepseek" {
		return
	}
	if deepSeekResumeFlag() == "" {
		// Resume is off; discovering an ID we would never emit is pure I/O.
		i.DeepSeekSessionID = ""
		return
	}
	i.DeepSeekSessionID = DiscoverDeepSeekSessionID(i.DeepSeekHomeDirForInstance(), i.EffectiveWorkingDir())
}

// DeepSeekSupportsResume reports whether restart will re-open the previous
// conversation rather than boot a fresh one. False on a default install: no
// shipped dsh profile accepts a resume flag (see the package comment).
func DeepSeekSupportsResume() bool {
	return deepSeekResumeFlag() != ""
}

// --- process-wide accessors (the CLI/TUI read these) -------------------------
//
// These answer "what would agent-deck do with no instance in hand" and back
// `agent-deck deepseek status`. Instance-scoped resolution (account, conductor,
// group) lives on the Instance methods above.

// DeepSeekHomeDir returns the effective DSH_HOME for this process: $DSH_HOME,
// then [deepseek].config_dir, then ~/.dsh.
func DeepSeekHomeDir() string {
	return getDeepSeekHomeDir()
}

// DeepSeekProfile returns the configured dsh profile name, defaulting to "web".
func DeepSeekProfile() string {
	cfg, _ := LoadUserConfig()
	if cfg != nil && strings.TrimSpace(cfg.DeepSeek.Profile) != "" {
		return strings.TrimSpace(cfg.DeepSeek.Profile)
	}
	return deepSeekDefaultProfile
}

// DeepSeekResumeFlag returns the configured resume flag, or "" when resume is
// off (the default — see DeepSeekSettings.ResumeFlag).
func DeepSeekResumeFlag() string {
	return deepSeekResumeFlag()
}

// deepSeekProfileManifest is the subset of a profile's package.json that names
// its bundle layers.
type deepSeekProfileManifest struct {
	DSH struct {
		Profile struct {
			Bundles []string `json:"bundles"`
		} `json:"profile"`
	} `json:"dsh"`
}

// DeepSeekProfileBundles returns the ordered bundle layers a profile composes,
// read from its package.json `dsh.profile.bundles`. Returns nil when the
// manifest is missing or unreadable — a profile directory with no manifest is a
// user's half-created profile, not an error worth failing a listing over.
func DeepSeekProfileBundles(profileDir string) []string {
	data, err := os.ReadFile(filepath.Join(profileDir, "package.json"))
	if err != nil {
		return nil
	}
	var manifest deepSeekProfileManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil
	}
	return manifest.DSH.Profile.Bundles
}

// expectsFastExit reports whether this session is launching a documented
// one-shot: a process that answers once and exits by design, rather than a
// long-lived surface whose early exit means something went wrong.
//
// Two spawn-path decisions read this:
//
//   - the fast-death watcher is skipped, so a completed run is not recorded as
//     a spawn failure and the preview does not paint "⚠ session failed to
//     start" over the answer;
//   - tmux `remain-on-exit` is set, so the pane (and the answer in it) survives
//     the process that printed it.
//
// Today the only such surface is DeepSeek's headless profile, whose whole
// contract is "answer one task, print the final assistant message, and exit"
// (0 when the turn completed, else 1). It is a method rather than a package
// function so a second one-shot surface has an obvious home.
func (i *Instance) expectsFastExit() bool {
	if i == nil || i.Tool != "deepseek" {
		return false
	}
	return DeepSeekProfileMode(i.resolveDeepSeekProfile()) == deepSeekModeHeadless
}
