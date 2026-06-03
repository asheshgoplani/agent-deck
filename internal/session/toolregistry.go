package session

import (
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
}

var registryLog = logging.ForComponent(logging.CompSession)

// Init builds a Registry from the static built-ins plus the supplied custom
// tools (typically config.Tools from LoadUserConfig). Custom entries whose name
// shadows a built-in are dropped with a warning (precedence rule (a)).
//
// Init is the single explicit constructor — tests build registries directly via
// Init(map[string]ToolDef{...}) rather than poking package globals.
func Init(custom map[string]ToolDef) *Registry {
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
	return r
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
	if cfg != nil {
		custom = cfg.Tools
	}
	registryCache = Init(custom)
	registryCacheCfg = cfg
	return registryCache
}

// MatchTool resolves a command string to a tool name using the process
// registry. It is the exported seam that cmd/agent-deck's detectTool() wraps.
func MatchTool(cmd string) string {
	return currentRegistry().Match(cmd)
}
