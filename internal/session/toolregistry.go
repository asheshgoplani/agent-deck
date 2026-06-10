package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// Registry is the unified, in-memory view of every tool agent-deck knows about:
// the canonical built-ins (static, from builtins.go) plus any user-defined
// [tools.<name>] entries merged in at Init time.
//
// It replaces the two-source-of-truth split that issue #1258 describes:
//   - detectTool()        -> Registry.Match()
//   - isBuiltinToolName()  -> Registry.IsBuiltin()
//   - GetToolDef()         -> Registry.GetCustom()  (see note below)
//   - GetCustomToolNames() -> Registry.CustomNames()
//
// Precedence rule (issue #1258 — chose option (a): reject + warn):
//
//	A custom [tools.<name>] whose name EXACTLY matches a built-in is rejected
//	with a startup warning, and the built-in is kept. This preserves the prior
//	"built-in wins" behavior while making the previously-silent shadow (#13)
//	explicit. To customize a built-in's command, use a different name plus
//	compatible_with = "<builtin>".
//
// A Registry is immutable after Init; rebuild via Init for a new config.
type Registry struct {
	order    []string               // built-in precedence order, drives Match()
	builtins map[string]builtinTool // name -> built-in record
	custom   map[string]ToolDef     // name -> user-defined tool (shadows dropped)

	// --- show_only_installed_tools filter state (issue #1259) ---
	// These are populated ONLY when the filter is enabled at Init. With the
	// filter off they stay zero-valued and every visibility method short-circuits
	// to "show everything", so the default path is byte-identical to before.
	filterInstalled bool            // the flag was on at Init (probe ran)
	installed       map[string]bool // name -> command resolved on PATH (shell always true)
	fallback        bool            // filter on AND nothing but shell resolved -> show all + hint

	// userHidden is the [ui].hidden_tools denylist (always applied).
	userHidden map[string]bool
}

var registryLog = logging.ForComponent(logging.CompSession)

// lookPathFn and statFn are indirections over exec.LookPath / os.Stat so tests
// can simulate a host where specific commands are (or are not) on PATH without
// mutating the real environment. Production code uses the real implementations.
var (
	lookPathFn = exec.LookPath
	statFn     = os.Stat
)

// probeInstalled reports whether a tool command resolves on the host.
//
//   - Absolute paths are checked with os.Stat (the binary must exist on disk).
//   - Commands that contain whitespace and are NOT absolute paths are inline
//     shell expressions or wrapper invocations (e.g. `bash -c "..."` or
//     `my-wrapper claude`); splitting and probing the wrapper is more work than
//     it's worth and would surprise users with intentional wrapper setups, so we
//     treat them as installed (issue #1259).
//   - Everything else is a bare command name, resolved with exec.LookPath.
func probeInstalled(command string) bool {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false
	}
	if filepath.IsAbs(cmd) {
		_, err := statFn(cmd)
		return err == nil
	}
	if strings.ContainsAny(cmd, " \t") {
		return true
	}
	_, err := lookPathFn(cmd)
	return err == nil
}

// Init builds a Registry from the static built-ins plus the supplied custom
// tools (typically config.Tools from LoadUserConfig). Custom entries whose name
// shadows a built-in are dropped with a warning (precedence rule (a)).
//
// Init is the single explicit constructor — tests build registries directly via
// Init(map[string]ToolDef{...}) rather than poking package globals. It builds an
// UNFILTERED registry (show_only_installed_tools off): no PATH probing happens,
// so behavior is byte-identical to before issue #1259.
func Init(custom map[string]ToolDef) *Registry {
	return InitFiltered(custom, false, nil)
}

// InitFiltered builds a Registry, optionally running the show_only_installed_tools
// probe (issue #1259). When showOnlyInstalled is false the probe is skipped
// ENTIRELY — not "probe then ignore" — so the default path makes zero LookPath
// calls and is byte-identical to Init's prior behavior.
//
// Caching policy: the probe runs only here, at construction time. The process
// registry (currentRegistry) rebuilds whenever the cached *UserConfig pointer
// changes, so a mid-session config edit re-probes on the next dialog open. There
// is deliberately no separate timer/refresh path (issue #1259 caching note).
func InitFiltered(custom map[string]ToolDef, showOnlyInstalled bool, hiddenTools []string) *Registry {
	r := &Registry{
		builtins: make(map[string]builtinTool),
		custom:   make(map[string]ToolDef),
	}
	for _, bt := range builtinTools() {
		r.order = append(r.order, bt.Name)
		r.builtins[bt.Name] = bt
	}
	for name, def := range custom {
		if _, isBuiltin := r.builtins[name]; isBuiltin {
			registryLog.Warn("ignored custom tool: name shadows a built-in",
				"name", name,
				"hint", "rename your custom tool and set compatible_with = \""+name+"\" instead")
			continue
		}
		r.custom[name] = def
	}

	if len(hiddenTools) > 0 {
		r.userHidden = make(map[string]bool, len(hiddenTools))
		for _, name := range hiddenTools {
			r.userHidden[name] = true
		}
	}

	if showOnlyInstalled {
		r.runInstalledProbe()
	}
	return r
}

// runInstalledProbe resolves every registered tool's command against the host
// PATH and records the result. It is the only place the probe runs. shell is
// hardcoded installed (the catch-all; bash/sh is universal and we never want to
// trap users with an empty dialog). When nothing but shell resolves the empty
// fallback engages and visibility reverts to "show all" + a hint.
func (r *Registry) runInstalledProbe() {
	r.filterInstalled = true
	r.installed = make(map[string]bool, len(r.order)+len(r.custom))
	nonShellInstalled := 0

	for _, name := range r.order {
		if name == "shell" {
			r.installed[name] = true // shell is always shown
			continue
		}
		// A built-in's command is its bare name (matches Registry.All / detectTool).
		ok := probeInstalled(name)
		r.installed[name] = ok
		if ok {
			nonShellInstalled++
		}
	}
	for name, def := range r.custom {
		// Probe the custom entry's OWN command. For compatible_with tools this is
		// the user's wrapper binary, NOT the parent built-in — a missing wrapper is
		// hidden even though the built-in it's compatible with resolves.
		ok := probeInstalled(def.Command)
		r.installed[name] = ok
		if ok {
			nonShellInstalled++
		}
	}

	r.fallback = nonShellInstalled == 0
}

// IsBuiltin reports whether name is one of the canonical built-in tools.
// Replaces isBuiltinToolName().
func (r *Registry) IsBuiltin(name string) bool {
	_, ok := r.builtins[name]
	return ok
}

// GetCustom returns the user-defined ToolDef for name, or nil if name is not a
// custom tool. Built-in names return nil here (a shadowing custom entry was
// already rejected at Init), which is exactly the legacy GetToolDef() contract
// that many callers rely on (they branch on a nil result to fall back to
// built-in handling). GetToolDef() delegates here, NOT to Get().
func (r *Registry) GetCustom(name string) *ToolDef {
	if def, ok := r.custom[name]; ok {
		d := def
		return &d
	}
	return nil
}

// Get returns the unified entry for name as a *ToolDef: a custom tool if one is
// defined, otherwise a synthesized ToolDef for the built-in, otherwise nil.
//
// This is the new single lookup surface for NEW code that genuinely wants "tell
// me about this tool, built-in or custom." Existing callers stay on GetCustom
// via GetToolDef() to remain byte-identical (see GetCustom).
func (r *Registry) Get(name string) *ToolDef {
	if def := r.GetCustom(name); def != nil {
		return def
	}
	if bt, ok := r.builtins[name]; ok {
		return &ToolDef{Command: bt.Name, Icon: bt.Icon}
	}
	return nil
}

// Match resolves a command string to a tool name. Replaces detectTool().
//
// Resolution order, preserving the legacy semantics exactly:
//  1. Exact custom-tool name match on the RAW command (pre-lowercase), mirroring
//     detectTool's leading `GetToolDef(cmd) != nil` check.
//  2. Built-in detect patterns in canonical order (substring or token match,
//     case-insensitive).
//  3. Fallback to "shell".
func (r *Registry) Match(cmd string) string {
	if _, ok := r.custom[cmd]; ok {
		return cmd
	}

	lower := strings.ToLower(cmd)
	fields := strings.Fields(lower)
	for _, name := range r.order {
		bt := r.builtins[name]
		for _, sub := range bt.detectSubstrings {
			if strings.Contains(lower, sub) {
				return name
			}
		}
		for _, tok := range bt.detectTokens {
			if slices.Contains(fields, tok) {
				return name
			}
		}
	}
	return "shell"
}

// CustomNames returns the sorted names of user-defined tools (built-in shadows
// already excluded at Init). Returns nil when there are no custom tools, exactly
// like the legacy GetCustomToolNames(). Replaces GetCustomToolNames()'s body.
func (r *Registry) CustomNames() []string {
	if len(r.custom) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.custom))
	for name := range r.custom {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// All returns every canonical built-in as a ToolDef, in precedence order. This
// is the registry-as-data answer to "what are the built-ins?" (the old
// isBuiltinToolName set). Custom tools are reached via CustomNames()/Get().
func (r *Registry) All() []ToolDef {
	out := make([]ToolDef, 0, len(r.order))
	for _, name := range r.order {
		bt := r.builtins[name]
		out = append(out, ToolDef{Command: bt.Name, Icon: bt.Icon})
	}
	return out
}

// --- show_only_installed_tools visibility surface (issue #1259) --------------
//
// This is a PRESENTATION-layer concern applied AFTER Match()/All() resolve. The
// match/resolution logic from #1261 is untouched; the CLI dispatch path never
// consults these methods, so `agent-deck launch -c <hidden-tool>` still works.

// IsVisible reports whether a tool name should appear in the new-session dialogs.
// The [ui].hidden_tools denylist is always applied. show_only_installed_tools
// is applied when enabled unless the empty-fallback is active. "shell" (and its
// empty-command alias "") is always visible.
func (r *Registry) IsVisible(name string) bool {
	if name == "" || name == "shell" {
		return true
	}
	if r.userHidden[name] {
		return false
	}
	if !r.filterInstalled || r.fallback {
		return true
	}
	return r.installed[name]
}

// FilterActive reports whether the show_only_installed_tools filter is on,
// regardless of whether the empty-fallback engaged.
func (r *Registry) FilterActive() bool {
	return r.filterInstalled
}

// FilterFallback reports the empty-fallback state: the filter is on but nothing
// other than shell resolved on PATH, so the dialogs show the full list plus a
// one-line hint instead of trapping the user with an empty selection.
func (r *Registry) FilterFallback() bool {
	return r.fallback
}

// Visible returns the built-in ToolDefs that should be shown in dialogs, in
// precedence order. With the filter off (or in fallback) it equals All().
func (r *Registry) Visible() []ToolDef {
	out := make([]ToolDef, 0, len(r.order))
	for _, name := range r.order {
		if !r.IsVisible(name) {
			continue
		}
		bt := r.builtins[name]
		out = append(out, ToolDef{Command: bt.Name, Icon: bt.Icon})
	}
	return out
}

// FilterVisibleNames returns names with hidden tools removed. The empty command
// "" (shell) is always kept.
func (r *Registry) FilterVisibleNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if r.IsVisible(n) {
			out = append(out, n)
		}
	}
	return out
}

// HiddenToolNames returns the configured [ui].hidden_tools denylist.
func (r *Registry) HiddenToolNames() []string {
	out := make([]string, 0, len(r.userHidden))
	for name := range r.userHidden {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// --- process-wide accessor ---------------------------------------------------
//
// The package-level helpers below back the retargeted call sites. The registry
// is rebuilt only when the user config changes, keyed on LoadUserConfig's cached
// *UserConfig identity (stable until config.toml's mtime changes). This keeps a
// single source of truth for built-in identity while preserving the prior
// runtime-reload behavior of the custom-tool path (GetToolDef used to read live
// config on every call).

var (
	registryMu       sync.Mutex
	registryCache    *Registry
	registryCacheCfg *UserConfig
)

// currentRegistry returns the process registry, rebuilding it lazily whenever
// the user config pointer changes.
func currentRegistry() *Registry {
	cfg, _ := LoadUserConfig()

	registryMu.Lock()
	defer registryMu.Unlock()

	if registryCache != nil && registryCacheCfg == cfg {
		return registryCache
	}

	var custom map[string]ToolDef
	showOnlyInstalled := false
	var hiddenTools []string
	if cfg != nil {
		custom = cfg.Tools
		showOnlyInstalled = cfg.UI.ShowOnlyInstalledTools
		hiddenTools = cfg.UI.HiddenTools
	}
	registryCache = InitFiltered(custom, showOnlyInstalled, hiddenTools)
	registryCacheCfg = cfg
	return registryCache
}

// MatchTool resolves a command string to a tool name using the process
// registry. It is the exported seam that cmd/agent-deck's detectTool() wraps.
//
// NOTE: Match never consults the show_only_installed_tools filter — resolution
// is display-independent, so the CLI dispatch path keeps spawning tools that the
// dialogs would hide (issue #1259 non-goal: display filter only, not a gate).
func MatchTool(cmd string) string {
	return currentRegistry().Match(cmd)
}

// --- process-wide show_only_installed_tools accessors (issue #1259) ----------
//
// These back the new-session dialog call sites. They read the cached registry,
// so they reflect the current config and re-probe only when config changes.

// FilterVisibleToolNames removes tools hidden by show_only_installed_tools from
// names. The empty command "" (shell) is always kept. With the flag off it
// returns names unchanged (byte-identical default behavior).
func FilterVisibleToolNames(names []string) []string {
	return currentRegistry().FilterVisibleNames(names)
}

// VisibleToolNames returns the names of every tool (built-in + custom) that
// passes the show_only_installed_tools filter, in built-in-precedence order then
// sorted custom names. With the flag off it returns the full set. Used by the
// web new-session dialog to intersect its static tool list.
func VisibleToolNames() []string {
	r := currentRegistry()
	names := make([]string, 0, len(r.order)+len(r.custom))
	for _, d := range r.Visible() {
		names = append(names, d.Command)
	}
	for _, n := range r.CustomNames() {
		if r.IsVisible(n) {
			names = append(names, n)
		}
	}
	return names
}

// ToolFilterActive reports whether show_only_installed_tools is enabled.
func ToolFilterActive() bool {
	return currentRegistry().FilterActive()
}

// ToolFilterFallbackActive reports whether the empty-fallback engaged (filter on,
// nothing but shell resolved) so dialogs can surface the "showing all" hint.
func ToolFilterFallbackActive() bool {
	return currentRegistry().FilterFallback()
}

// ConfiguredHiddenToolNames returns the [ui].hidden_tools denylist from config.
func ConfiguredHiddenToolNames() []string {
	return currentRegistry().HiddenToolNames()
}

// pickerPresetOrder matches buildPresetCommands in internal/ui/newdialog.go.
var pickerPresetOrder = []string{"", "claude", "gemini", "opencode", "codex", "pi", "copilot", "crush", "cursor", "hermes"}

// PickerToolNames returns tool names for the new-session picker after applying
// hidden_tools and show_only_installed_tools. The empty command "" is mapped
// to "shell" for web consumers.
func PickerToolNames() []string {
	r := currentRegistry()
	presets := append([]string{}, pickerPresetOrder...)
	if custom := r.CustomNames(); len(custom) > 0 {
		presets = append(presets, custom...)
	}
	filtered := r.FilterVisibleNames(presets)
	out := make([]string, 0, len(filtered))
	for _, name := range filtered {
		if name == "" {
			out = append(out, "shell")
			continue
		}
		out = append(out, name)
	}
	return out
}
