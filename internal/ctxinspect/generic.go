package ctxinspect

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Category names emitted by [GenericAdapter]. They are exported so the CLI and
// the TUI can address them without string literals.
const (
	// CategoryInstructions holds the instruction-file hierarchy.
	CategoryInstructions = "instruction-files"
	// CategoryMCP holds the MCP servers configured for the session.
	CategoryMCP = "mcp"
	// CategoryAgentDeckCatalog holds agent-deck's [mcps.*] catalog entries that
	// are not attached to this session.
	CategoryAgentDeckCatalog = "agent-deck-catalog"
)

// genericUnknownTokensReason is the single sentence the generic adapter uses
// wherever a token count would have to be invented. It is deliberately one
// string: every "—" in a generic report has the same, checkable explanation.
const genericUnknownTokensReason = "this harness exposes no per-item token accounting and agent-deck will not guess one"

// GenericAdapter is the honest floor: the adapter used for every harness with
// no real mechanism of its own — cursor-agent, aider, crush, hermes, pi,
// copilot, opencode, shell, and any custom [tools.*] entry.
//
// It reports provenance without tokens. Every token field is [TokenUnknown] and
// renders as an em dash, because a number invented for a harness that reports
// none is exactly the failure this feature exists to prevent. What it does
// deliver is the inventory: which instruction files are in play and in what
// order, which MCP servers are configured and from which file, and everything
// agent-deck itself wired in — with an exact command for the last group, which
// no other tool can produce. Unsupported is a populated screen, not an error.
type GenericAdapter struct {
	// ReadContent requests instruction-file contents so the detail view can
	// show them. Sizes are always recorded; contents cost one bounded read per
	// file.
	ReadContent bool
	// MaxFileBytes bounds a single instruction-file read. Zero uses the
	// package default.
	MaxFileBytes int64
}

// NewGenericAdapter returns the generic adapter with content reading enabled.
func NewGenericAdapter() *GenericAdapter { return &GenericAdapter{ReadContent: true} }

// Name implements [Adapter].
func (a *GenericAdapter) Name() string { return "generic" }

// Supports implements [Adapter]. The generic adapter is the registry fallback
// and claims every tool, so registering it in the ordered list would shadow
// every specific adapter behind it; pass it as the fallback instead.
func (a *GenericAdapter) Supports(tool string, _ Host) bool {
	return strings.TrimSpace(tool) != ""
}

// Capabilities implements [Adapter]. It is answerable without touching disk and
// states plainly that no number is obtainable.
func (a *GenericAdapter) Capabilities() Capabilities {
	return Capabilities{
		Adapter:           a.Name(),
		CanAnchor:         false,
		CanVerbatimSystem: false,
		Categories: []CategoryCapability{
			{
				Name:  CategoryInstructions,
				Title: "instruction files",
				Text:  TextReconstructed,
				Token: TokenUnknown,
				Note:  "found by re-walking the harness's documented file-name convention; presence on disk is not proof the agent loaded the file",
			},
			{
				Name:  CategoryMCP,
				Title: "MCP servers",
				Text:  TextAbsent,
				Token: TokenUnknown,
				Note:  "server names and their configuring file are known; tool schemas are negotiated at runtime and never written to disk",
			},
			{
				Name:  CategoryAgentDeckCatalog,
				Title: "agent-deck catalog (not attached)",
				Text:  TextAbsent,
				Token: TokenUnknown,
				Note:  "catalog entries that are not attached to this session and therefore cost nothing today",
			},
		},
		Notes: []string{
			"No token figures: this harness reports none, and " + genericUnknownTokensReason + ".",
			"No context-window size and no fixed-prefix anchor, so there is nothing to reconcile against.",
		},
	}
}

// Inspect implements [Adapter]. It produces a projected report: nothing here was
// observed from a session's own startup records, so no anchor is claimed and
// [Report.Reconcile] will record [ReconNoAnchor].
func (a *GenericAdapter) Inspect(_ context.Context, req Request) (*Report, error) {
	host := req.HostOrNone()
	rep := &Report{
		Harness:     req.Tool,
		SessionID:   req.SessionID,
		ProjectPath: req.ProjectPath,
		Model:       req.Model,
		Window:      WindowInfo{Source: WindowUnknown},
		Basis:       BasisProjected,
		GeneratedAt: req.Timestamp(),
	}
	rep.AddCaveat("generic-adapter",
		fmt.Sprintf("%q has no context-inspection mechanism agent-deck can read, so this is an inventory of what is configured, not a measurement of what is loaded.", req.Tool),
		SeverityWarn)

	instructions, err := a.instructionsCategory(req, host)
	if err != nil {
		return nil, err
	}
	if instructions != nil {
		rep.Categories = append(rep.Categories, *instructions)
	}

	catalog, catalogErr := host.CatalogMCPs()
	if catalogErr != nil && !errors.Is(catalogErr, ErrHostUnavailable) {
		return nil, fmt.Errorf("reading the agent-deck MCP catalog: %w", catalogErr)
	}
	catalogByName := make(map[string]CatalogMCP, len(catalog))
	for _, c := range catalog {
		catalogByName[c.Name] = c
	}

	mcpCat, attachedNames, err := a.mcpCategory(req, host, catalogByName)
	if err != nil {
		return nil, err
	}
	if mcpCat != nil {
		rep.Categories = append(rep.Categories, *mcpCat)
	}

	if catalogErr != nil {
		rep.AddCaveat("catalog-unavailable",
			fmt.Sprintf("agent-deck's [mcps.*] catalog could not be read via the %q host probe, so catalog-attributed items are missing from this report.", host.Name()),
			SeverityWarn)
	} else if cat := a.catalogCategory(req, catalog, attachedNames); cat != nil {
		rep.Categories = append(rep.Categories, *cat)
	}

	return rep, nil
}

// instructionsCategory discovers the instruction-file hierarchy. It returns nil
// when the walk found nothing and there is nothing honest to say about it.
func (a *GenericAdapter) instructionsCategory(req Request, host Host) (*Category, error) {
	spec := InstructionSpecFor(req.Tool, host)
	files, err := DiscoverInstructionFiles(InstructionWalkOptions{
		Spec:        spec,
		ProjectPath: req.ProjectPath,
		ReadContent: a.ReadContent,
		MaxBytes:    a.MaxFileBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("walking the instruction-file hierarchy: %w", err)
	}

	cat := &Category{
		Name:        CategoryInstructions,
		Title:       "instruction files",
		Description: "files the harness's own convention would load, in load order from the furthest ancestor down to the working directory",
		Notes:       []string{"looked for: " + strings.Join(spec.ProjectNames, ", ")},
	}
	if spec.Note != "" {
		cat.Notes = append(cat.Notes, spec.Note)
	}
	if !spec.Verified {
		cat.Notes = append(cat.Notes, "this convention is unconfirmed for "+req.Tool+": treat the list as candidates, not as loaded context")
	}

	for _, f := range files {
		cat.Items = append(cat.Items, a.instructionItem(f, spec, req.Tool))
	}
	if len(cat.Items) == 0 {
		cat.Notes = append(cat.Notes, "none found on disk")
	}
	return cat, nil
}

// instructionItem converts one discovered file into an item.
func (a *GenericAdapter) instructionItem(f InstructionFile, spec InstructionSpec, tool string) Item {
	content := Content{Prov: TextAbsent, Note: "file contents were not read"}
	switch {
	case f.ReadErr != "":
		content = AbsentContent("found on disk but unreadable: " + f.ReadErr)
	case a.ReadContent:
		note := fmt.Sprintf("read from disk by re-walking %s's instruction-file convention", tool)
		if f.Truncated {
			note += fmt.Sprintf("; showing the first %d of %d bytes", len(f.Content), f.Size)
		}
		content = ReconstructedContent(KindMarkdown, f.Content, note)
		content.Truncated = f.Truncated
	}

	origin := OriginProject
	if f.Scope == ScopeUser {
		origin = OriginUserConfig
	}

	it := Item{
		ID:      "instructions:" + f.Path,
		Label:   f.Name,
		Detail:  fmt.Sprintf("%s · %s · %s", f.Scope, humanBytes(f.Size), filepath.Dir(f.Path)),
		Content: content,
		Load:    LoadedNowCost(UnknownTokens(genericUnknownTokensReason)),
		Origin:  origin,
		Lever:   FileLever(f.Path, fmt.Sprintf("%s scope, depth %d in the instruction-file chain", f.Scope, f.Depth)),
	}
	if f.ReadErr != "" {
		it.Caveats = append(it.Caveats, Caveat{
			Code:     "instruction-file-unreadable",
			Message:  fmt.Sprintf("%s exists but could not be read: %s", f.Path, f.ReadErr),
			Severity: SeverityWarn,
			Category: CategoryInstructions,
		})
	}
	if !spec.Verified {
		it.Caveats = append(it.Caveats, Caveat{
			Code:     "instruction-convention-unverified",
			Message:  fmt.Sprintf("%s's instruction-file convention is unconfirmed: this file's presence on disk does not prove the agent loaded it", tool),
			Severity: SeverityWarn,
			Category: CategoryInstructions,
		})
	}
	return it
}

// mcpCategory lists the MCP servers configured for this tool and project, and
// returns the set of attached names so the catalog category can exclude them.
func (a *GenericAdapter) mcpCategory(req Request, host Host, catalog map[string]CatalogMCP) (*Category, map[string]bool, error) {
	attachedNames := make(map[string]bool)

	if !host.SupportsMCPManager(req.Tool) {
		// Not a gap: agent-deck's MCP surfaces genuinely do not apply here.
		return &Category{
			Name:        CategoryMCP,
			Title:       "MCP servers",
			Description: "MCP servers configured for this session",
			Notes: []string{
				fmt.Sprintf("agent-deck's MCP management does not apply to %q, so no MCP inventory is claimed for it", req.Tool),
			},
		}, attachedNames, nil
	}

	refs, err := host.AttachedMCPs(req.Tool, req.ProjectPath)
	if err != nil {
		if !errors.Is(err, ErrHostUnavailable) {
			return nil, nil, fmt.Errorf("reading MCP configuration for %q: %w", req.Tool, err)
		}
		return &Category{
			Name:        CategoryMCP,
			Title:       "MCP servers",
			Description: "MCP servers configured for this session",
			Notes: []string{
				fmt.Sprintf("MCP inventory unavailable: the %q host probe cannot read this session's MCP configuration, so this list is missing rather than empty", host.Name()),
			},
		}, attachedNames, nil
	}

	sortMCPRefs(refs)
	cat := &Category{
		Name:        CategoryMCP,
		Title:       "MCP servers",
		Description: "MCP servers configured for this session, with the file that configured each one",
		Notes: []string{
			"tool schemas are negotiated at runtime and never written to disk, so their size is not knowable without launching each server",
		},
	}
	if p := host.MCPGlobalConfigPath(req.Tool); p != "" {
		cat.Notes = append(cat.Notes, "global config: "+p)
	}
	if p := host.MCPLocalConfigPath(req.Tool, req.ProjectPath); p != "" {
		cat.Notes = append(cat.Notes, "project config: "+p)
	}

	for _, ref := range refs {
		attachedNames[ref.Name] = true
		cat.Items = append(cat.Items, a.mcpItem(req, ref, catalog))
	}
	if len(cat.Items) == 0 {
		cat.Notes = append(cat.Notes, "none configured")
	}
	return cat, attachedNames, nil
}

// mcpItem converts one MCP reference into an item, preferring the agent-deck
// command lever when agent-deck itself put the server there.
func (a *GenericAdapter) mcpItem(req Request, ref MCPRef, catalog map[string]CatalogMCP) Item {
	origin := OriginMCPServer
	lever := FileLever(ref.SourcePath, fmt.Sprintf("configured in %s scope", ref.Scope))
	detail := fmt.Sprintf("%s · %s", ref.Scope, ref.SourcePath)

	if entry, ok := catalog[ref.Name]; ok {
		origin = OriginAgentDeck
		lever = CommandLever(
			fmt.Sprintf("agent-deck mcp detach %s %s", sessionRef(req), ref.Name),
			"attached by agent-deck from the [mcps.*] catalog in "+entry.SourcePath,
		)
		if entry.Description != "" {
			detail = fmt.Sprintf("%s · %s", ref.Scope, entry.Description)
		}
	}
	if ref.SourcePath == "" && origin == OriginMCPServer {
		lever = ImmovableLever("no configuring file was reported for this server")
	}

	return Item{
		ID:      fmt.Sprintf("mcp:%s:%s", ref.Scope, ref.Name),
		Label:   ref.Name,
		Detail:  detail,
		Content: AbsentContent("MCP tool schemas are exchanged over the wire at startup and are not persisted; agent-deck does not launch servers to price them"),
		Load:    LoadedNowCost(UnknownTokens(genericUnknownTokensReason)),
		Origin:  origin,
		Lever:   lever,
	}
}

// catalogCategory lists catalog entries that are not attached to this session.
// They cost a certain zero today, which is the point: it tells the user what is
// safe to leave alone.
func (a *GenericAdapter) catalogCategory(req Request, catalog []CatalogMCP, attached map[string]bool) *Category {
	cat := &Category{
		Name:        CategoryAgentDeckCatalog,
		Title:       "agent-deck catalog (not attached)",
		Description: "MCP servers agent-deck knows about that are not attached to this session",
		Notes:       []string{"these cost nothing in this session's context today"},
	}
	for _, entry := range catalog {
		if attached[entry.Name] {
			continue
		}
		detail := entry.Description
		if detail == "" {
			detail = entry.Transport
		}
		cat.Items = append(cat.Items, Item{
			ID:      "catalog:" + entry.Name,
			Label:   entry.Name,
			Detail:  detail,
			Content: AbsentContent("not attached to this session; nothing was sent"),
			Load:    AvailableCost(UnknownTokens(genericUnknownTokensReason)),
			Origin:  OriginAgentDeck,
			Lever: CommandLever(
				fmt.Sprintf("agent-deck mcp attach %s %s", sessionRef(req), entry.Name),
				"defined in the [mcps.*] catalog in "+entry.SourcePath+"; not attached here",
			),
		})
	}
	if len(cat.Items) == 0 {
		return nil
	}
	return cat
}

// sessionRef returns the reference to interpolate into a command lever, falling
// back to a visible placeholder so a copied command can never silently target
// the wrong session.
func sessionRef(req Request) string {
	if s := strings.TrimSpace(req.SessionRef); s != "" {
		return s
	}
	return "<session>"
}

// humanBytes renders a byte count compactly for a detail line.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
