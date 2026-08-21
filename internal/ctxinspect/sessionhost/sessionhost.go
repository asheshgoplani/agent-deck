// Package sessionhost wires agent-deck's live session knowledge into the pure
// [ctxinspect] engine.
//
// The engine must not import internal/session: that package pulls in tmux, the
// state database and the process supervisor, none of which belong on a render
// path or in a fixture-driven test. This package is the one place that
// dependency lives, so ctxinspect stays testable with no HOME, no harness and
// no live session, while production still reuses the real per-tool MCP config
// readers instead of a second, drifting copy of them.
package sessionhost

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// host implements [ctxinspect.Host] on top of internal/session.
type host struct{}

// New returns the production host probe.
func New() ctxinspect.Host { return host{} }

// Name implements [ctxinspect.Host].
func (host) Name() string { return "session" }

// IsClaudeCompatible implements [ctxinspect.Host], so a custom [tools.*] entry
// whose command is claude inherits the Claude adapter.
func (host) IsClaudeCompatible(tool string) bool { return session.IsClaudeCompatible(tool) }

// IsCodexCompatible implements [ctxinspect.Host].
func (host) IsCodexCompatible(tool string) bool { return session.IsCodexCompatible(tool) }

// SupportsMCPManager implements [ctxinspect.Host].
func (host) SupportsMCPManager(tool string) bool { return session.ToolSupportsMCPManager(tool) }

// MCPLocalConfigPath implements [ctxinspect.Host].
func (host) MCPLocalConfigPath(tool, projectPath string) string {
	return session.MCPLocalConfigPathForTool(tool, projectPath)
}

// MCPGlobalConfigPath implements [ctxinspect.Host].
func (host) MCPGlobalConfigPath(tool string) string {
	return session.MCPGlobalConfigPathForTool(tool)
}

// AttachedMCPs implements [ctxinspect.Host] by reusing the same per-tool config
// readers the MCP manager uses, so the inspector can never disagree with the
// attach/detach surfaces about what is configured.
func (h host) AttachedMCPs(tool, projectPath string) ([]ctxinspect.MCPRef, error) {
	if !session.ToolSupportsMCPManager(tool) {
		return nil, nil
	}
	info := session.MCPInfoForLocalAttach(tool, projectPath)
	if info == nil {
		return nil, ctxinspect.ErrHostUnavailable
	}

	globalPath := session.MCPGlobalConfigPathForTool(tool)
	localName := filepath.Base(session.MCPLocalConfigPathForTool(tool, projectPath))

	refs := make([]ctxinspect.MCPRef, 0, info.Total())
	for _, name := range info.Global {
		refs = append(refs, ctxinspect.MCPRef{Name: name, Scope: ctxinspect.MCPScopeGlobal, SourcePath: globalPath})
	}
	for _, name := range info.Project {
		refs = append(refs, ctxinspect.MCPRef{Name: name, Scope: ctxinspect.MCPScopeProject, SourcePath: globalPath})
	}
	for _, local := range info.LocalMCPs {
		src := local.SourcePath
		// LocalMCP records the directory holding the file; name the file
		// itself so the lever the user copies points at something editable.
		if src != "" && localName != "" && localName != "." && localName != string(filepath.Separator) {
			src = filepath.Join(src, localName)
		}
		refs = append(refs, ctxinspect.MCPRef{Name: local.Name, Scope: ctxinspect.MCPScopeLocal, SourcePath: src})
	}
	return refs, nil
}

// CatalogMCPs implements [ctxinspect.Host] by reading agent-deck's own [mcps.*]
// catalog. This is the inventory no other tool can produce: agent-deck knows
// what it wired in, so those items get an exact command lever.
func (host) CatalogMCPs() ([]ctxinspect.CatalogMCP, error) {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return nil, fmt.Errorf("sessionhost: loading agent-deck config: %w", err)
	}
	if cfg == nil {
		return nil, ctxinspect.ErrHostUnavailable
	}

	configPath, pathErr := session.GetUserConfigPath()
	if pathErr != nil {
		// The catalog is still usable without the path; the lever just loses
		// its "defined in <file>" clause rather than the whole entry.
		configPath = ""
	}

	out := make([]ctxinspect.CatalogMCP, 0, len(cfg.MCPs))
	for name, def := range cfg.MCPs {
		out = append(out, ctxinspect.CatalogMCP{
			Name:        name,
			Description: strings.TrimSpace(def.Description),
			Transport:   def.GetTransport(),
			SourcePath:  configPath,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}
