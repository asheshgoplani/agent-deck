package ctxinspect

import (
	"errors"
	"sort"
	"strings"
)

// ErrHostUnavailable is returned by [Host] methods when the probe has no way to
// answer. It is a first-class result, not a failure: the adapter turns it into
// a visible "inventory unavailable" note rather than an empty section that
// reads as "you have none of these".
var ErrHostUnavailable = errors.New("ctxinspect: agent-deck host probe unavailable")

// MCPScope says where an MCP server was configured.
type MCPScope uint8

const (
	// MCPScopeGlobal: the harness's global configuration.
	MCPScopeGlobal MCPScope = iota
	// MCPScopeProject: the harness's per-project configuration.
	MCPScopeProject
	// MCPScopeLocal: a config file inside the project tree (or an ancestor).
	MCPScopeLocal
)

var (
	mcpScopeNames = map[MCPScope]string{
		MCPScopeGlobal:  "global",
		MCPScopeProject: "project",
		MCPScopeLocal:   "local",
	}
	mcpScopeByName = invertEnum(mcpScopeNames)
)

// String returns the wire/display name.
func (s MCPScope) String() string { return enumName(s, mcpScopeNames, "global") }

// MarshalJSON encodes the value as its wire name.
func (s MCPScope) MarshalJSON() ([]byte, error) { return marshalEnum(s, mcpScopeNames) }

// UnmarshalJSON decodes the value from its wire name.
func (s *MCPScope) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, mcpScopeByName, s, "mcp scope")
}

// MCPRef is one MCP server configured for a session, with the file that
// configured it.
type MCPRef struct {
	// Name is the server name as the harness sees it.
	Name string
	// Scope says which configuration layer supplied it.
	Scope MCPScope
	// SourcePath is the configuration file or directory it came from.
	SourcePath string
}

// CatalogMCP is an entry in agent-deck's own [mcps.*] catalog.
//
// This is the inventory nobody else can produce: agent-deck knows what it wired
// in, so these items get an exact command lever instead of "go find the file".
type CatalogMCP struct {
	// Name is the catalog key.
	Name string
	// Description is the catalog's help text.
	Description string
	// Transport is "stdio", "http" or "sse".
	Transport string
	// SourcePath is the config.toml the entry was read from.
	SourcePath string
}

// Host supplies agent-deck's own knowledge of a session to the otherwise pure
// inspection engine.
//
// The engine deliberately does not import internal/session: that package pulls
// in tmux, the state database and the process supervisor, none of which belong
// on a render path or in a fixture-driven test. Everything the engine needs
// from it therefore enters through this interface, implemented for production
// by the internal/ctxinspect/sessionhost subpackage and by [NoHost] elsewhere.
type Host interface {
	// Name identifies the probe in notes and caveats, e.g. "session" or "none".
	Name() string
	// IsClaudeCompatible reports whether the tool is claude or a custom tool
	// wrapping it.
	IsClaudeCompatible(tool string) bool
	// IsCodexCompatible reports whether the tool is codex or a custom tool
	// wrapping it.
	IsCodexCompatible(tool string) bool
	// SupportsMCPManager reports whether agent-deck's MCP surfaces apply to
	// this tool at all. A false here is why an empty MCP section is honest.
	SupportsMCPManager(tool string) bool
	// MCPLocalConfigPath returns the project-local MCP config path, or "".
	MCPLocalConfigPath(tool, projectPath string) string
	// MCPGlobalConfigPath returns the global MCP config path, or "".
	MCPGlobalConfigPath(tool string) string
	// AttachedMCPs lists the MCP servers configured for this tool and project.
	// It returns [ErrHostUnavailable] when the probe cannot answer.
	AttachedMCPs(tool, projectPath string) ([]MCPRef, error)
	// CatalogMCPs lists agent-deck's [mcps.*] catalog. It returns
	// [ErrHostUnavailable] when the probe cannot answer.
	CatalogMCPs() ([]CatalogMCP, error)
}

// noHost is the probe used when the caller wired none. It answers the
// compatibility predicates by exact name — the conservative choice, because a
// custom tool wrapping claude then falls through to the generic adapter and is
// under-reported rather than reported as something it is not — and reports
// every inventory as unavailable with a reason.
type noHost struct{}

// NoHost returns a probe that answers nothing, with a reason. It exists so an
// unwired build degrades honestly instead of panicking on a nil interface.
func NoHost() Host { return noHost{} }

// Name implements [Host].
func (noHost) Name() string { return "none" }

// IsClaudeCompatible implements [Host] by exact name.
func (noHost) IsClaudeCompatible(tool string) bool { return tool == "claude" }

// IsCodexCompatible implements [Host] by exact name.
func (noHost) IsCodexCompatible(tool string) bool { return tool == "codex" }

// SupportsMCPManager implements [Host]. Without agent-deck's configuration
// there is no basis to claim MCP management applies.
func (noHost) SupportsMCPManager(string) bool { return false }

// MCPLocalConfigPath implements [Host].
func (noHost) MCPLocalConfigPath(string, string) string { return "" }

// MCPGlobalConfigPath implements [Host].
func (noHost) MCPGlobalConfigPath(string) string { return "" }

// AttachedMCPs implements [Host].
func (noHost) AttachedMCPs(string, string) ([]MCPRef, error) {
	return nil, ErrHostUnavailable
}

// CatalogMCPs implements [Host].
func (noHost) CatalogMCPs() ([]CatalogMCP, error) { return nil, ErrHostUnavailable }

// StaticHost is an in-memory [Host] for tests and for callers that already hold
// the answers. Its zero value behaves like [NoHost] except that it reports
// empty inventories rather than [ErrHostUnavailable]; set the Unavailable flag
// to exercise the degraded path.
type StaticHost struct {
	// HostName is reported by Name; defaults to "static".
	HostName string
	// ClaudeTools and CodexTools are matched exactly by the compatibility
	// predicates, in addition to the built-in names.
	ClaudeTools []string
	CodexTools  []string
	// MCPTools are the tools for which agent-deck MCP management applies.
	MCPTools []string
	// LocalPaths and GlobalPaths are keyed by tool name.
	LocalPaths  map[string]string
	GlobalPaths map[string]string
	// Attached is keyed by tool name.
	Attached map[string][]MCPRef
	// Catalog is the [mcps.*] catalog.
	Catalog []CatalogMCP
	// Unavailable makes the inventory methods return [ErrHostUnavailable].
	Unavailable bool
}

// Name implements [Host].
func (h *StaticHost) Name() string {
	if h.HostName != "" {
		return h.HostName
	}
	return "static"
}

// IsClaudeCompatible implements [Host].
func (h *StaticHost) IsClaudeCompatible(tool string) bool {
	return tool == "claude" || containsString(h.ClaudeTools, tool)
}

// IsCodexCompatible implements [Host].
func (h *StaticHost) IsCodexCompatible(tool string) bool {
	return tool == "codex" || containsString(h.CodexTools, tool)
}

// SupportsMCPManager implements [Host].
func (h *StaticHost) SupportsMCPManager(tool string) bool {
	return containsString(h.MCPTools, tool)
}

// MCPLocalConfigPath implements [Host].
func (h *StaticHost) MCPLocalConfigPath(tool, _ string) string { return h.LocalPaths[tool] }

// MCPGlobalConfigPath implements [Host].
func (h *StaticHost) MCPGlobalConfigPath(tool string) string { return h.GlobalPaths[tool] }

// AttachedMCPs implements [Host].
func (h *StaticHost) AttachedMCPs(tool, _ string) ([]MCPRef, error) {
	if h.Unavailable {
		return nil, ErrHostUnavailable
	}
	return h.Attached[tool], nil
}

// CatalogMCPs implements [Host].
func (h *StaticHost) CatalogMCPs() ([]CatalogMCP, error) {
	if h.Unavailable {
		return nil, ErrHostUnavailable
	}
	return h.Catalog, nil
}

// containsString reports membership, trimming and comparing case-insensitively
// so a tool name typed with different casing in config.toml still matches.
func containsString(list []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), want) {
			return true
		}
	}
	return false
}

// sortMCPRefs orders MCP references deterministically so a report is stable
// across runs and comparable against a golden fixture.
func sortMCPRefs(refs []MCPRef) {
	sort.SliceStable(refs, func(a, b int) bool {
		if refs[a].Scope != refs[b].Scope {
			return refs[a].Scope < refs[b].Scope
		}
		if refs[a].Name != refs[b].Name {
			return refs[a].Name < refs[b].Name
		}
		return refs[a].SourcePath < refs[b].SourcePath
	})
}
