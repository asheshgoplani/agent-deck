package ctxinspect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chmodUnreadable makes a file unreadable for the duration of a test, and
// reports why it could not when the platform or the user makes that impossible.
func chmodUnreadable(t *testing.T, path string) error {
	t.Helper()
	if os.Geteuid() == 0 {
		return fmt.Errorf("running as root: a 0000 file is still readable")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		return err
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	return nil
}

func inspectGeneric(t *testing.T, req Request) *Report {
	t.Helper()
	rep, err := NewRegistry(NewGenericAdapter()).Inspect(context.Background(), req)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return rep
}

func TestGenericAdapterReportsEveryTokenAsUnknown(t *testing.T) {
	project := t.TempDir()
	writeFile(t, filepath.Join(project, "AGENTS.md"), "project rules")

	host := &StaticHost{
		MCPTools: []string{"cursor"},
		Attached: map[string][]MCPRef{"cursor": {
			{Name: "exa", Scope: MCPScopeGlobal, SourcePath: "/cfg/mcp.json"},
		}},
		Catalog: []CatalogMCP{
			{Name: "exa", Description: "web search", Transport: "stdio", SourcePath: "/cfg/config.toml"},
			{Name: "telegram", Description: "chat", Transport: "stdio", SourcePath: "/cfg/config.toml"},
		},
	}
	rep := inspectGeneric(t, Request{Tool: "cursor", ProjectPath: project, SessionRef: "my-session", Host: host})

	for _, c := range rep.Categories {
		for _, it := range c.Items {
			if it.Load.State == Available {
				continue // a not-loaded item costs a certain, checkable zero
			}
			if it.Load.Actual.Known() {
				t.Fatalf("category %q item %q carries a token count; a harness that reports none must render an em dash", c.Name, it.ID)
			}
			if it.Load.Actual.Reason() == "" {
				t.Fatalf("category %q item %q has an unexplained unknown", c.Name, it.ID)
			}
		}
	}
	if _, complete := rep.AttributedTotal(); complete {
		t.Fatal("a report with no token counts must never present a complete total")
	}
}

func TestGenericAdapterIsProjectedAndUnanchored(t *testing.T) {
	rep := inspectGeneric(t, Request{Tool: "aider", ProjectPath: t.TempDir()})
	if rep.Basis != BasisProjected {
		t.Fatalf("basis = %s, want projected: nothing here was observed from a session's own records", rep.Basis)
	}
	if rep.Anchor != nil {
		t.Fatal("a projection has no session to measure and must not claim an anchor")
	}
	if rep.Reconciliation.Status != ReconNoAnchor {
		t.Fatalf("status = %s, want no-anchor", rep.Reconciliation.Status)
	}
	if rep.Window.Known() {
		t.Fatal("the generic adapter knows no window size and must not invent one")
	}
	if err := rep.Validate(); err != nil {
		t.Fatalf("the generic report must be internally consistent: %v", err)
	}
}

func TestGenericAdapterUnsupportedIsAPopulatedScreen(t *testing.T) {
	project := t.TempDir()
	writeFile(t, filepath.Join(project, "AGENTS.md"), "rules")

	rep := inspectGeneric(t, Request{Tool: "some-unknown-tool", ProjectPath: project})
	cat, ok := rep.Category(CategoryInstructions)
	if !ok {
		t.Fatal("an unsupported harness must still get an inventory, not an error screen")
	}
	if len(cat.Items) != 1 {
		t.Fatalf("found %d instruction files, want 1", len(cat.Items))
	}
	var warned bool
	for _, cv := range cat.Items[0].Caveats {
		if cv.Code == "instruction-convention-unverified" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("a guessed convention must say that presence on disk is not proof the agent loaded the file")
	}
}

func TestGenericAdapterDistinguishesUnavailableFromEmpty(t *testing.T) {
	project := t.TempDir()

	unwired := inspectGeneric(t, Request{Tool: "cursor", ProjectPath: project}) // no host
	cat, ok := unwired.Category(CategoryMCP)
	if !ok {
		t.Fatal("the MCP category must exist so its absence of data can be explained")
	}
	if !strings.Contains(strings.Join(cat.Notes, " "), "does not apply") {
		t.Fatalf("notes %v must say why the list is empty rather than implying the user has no MCPs", cat.Notes)
	}

	broken := &StaticHost{MCPTools: []string{"cursor"}, Unavailable: true}
	degraded := inspectGeneric(t, Request{Tool: "cursor", ProjectPath: project, Host: broken})
	cat, _ = degraded.Category(CategoryMCP)
	if !strings.Contains(strings.Join(cat.Notes, " "), "unavailable") {
		t.Fatalf("notes %v must report the inventory as missing, not as empty", cat.Notes)
	}
	var flagged bool
	for _, cv := range degraded.Caveats {
		if cv.Code == "catalog-unavailable" {
			flagged = true
		}
	}
	if !flagged {
		t.Fatal("an unreadable catalog must be reported, not silently omitted")
	}
}

func TestGenericAdapterGivesAgentDeckItemsACommandLever(t *testing.T) {
	host := &StaticHost{
		MCPTools: []string{"cursor"},
		Attached: map[string][]MCPRef{"cursor": {
			{Name: "exa", Scope: MCPScopeGlobal, SourcePath: "/cfg/mcp.json"},
			{Name: "custom", Scope: MCPScopeLocal, SourcePath: "/proj/.mcp.json"},
		}},
		Catalog: []CatalogMCP{{Name: "exa", Description: "web search", SourcePath: "/cfg/config.toml"}},
	}
	rep := inspectGeneric(t, Request{Tool: "cursor", ProjectPath: t.TempDir(), SessionRef: "api-fix", Host: host})
	cat, _ := rep.Category(CategoryMCP)

	byID := map[string]Item{}
	for _, it := range cat.Items {
		byID[it.ID] = it
	}

	catalogItem := byID["mcp:global:exa"]
	if catalogItem.Origin != OriginAgentDeck {
		t.Fatalf("origin = %s, want agent-deck: agent-deck knows it wired this one in", catalogItem.Origin)
	}
	if catalogItem.Lever.Kind != LeverRunCommand {
		t.Fatalf("lever kind = %s, want a run-command lever", catalogItem.Lever.Kind)
	}
	if catalogItem.Lever.Command != "agent-deck mcp detach api-fix exa" {
		t.Fatalf("command = %q, want it targeted at this session", catalogItem.Lever.Command)
	}
	if catalogItem.Lever.Payload() != catalogItem.Lever.Command {
		t.Fatal("a command lever must copy its command, not its (empty) path")
	}

	foreign := byID["mcp:local:custom"]
	if foreign.Origin != OriginMCPServer {
		t.Fatalf("origin = %s, want mcp-server for something agent-deck did not attach", foreign.Origin)
	}
	if foreign.Lever.Kind != LeverEditFile || foreign.Lever.Path != "/proj/.mcp.json" {
		t.Fatalf("lever = %+v, want the configuring file", foreign.Lever)
	}
}

func TestGenericAdapterCommandLeverNeverTargetsTheWrongSession(t *testing.T) {
	host := &StaticHost{
		MCPTools: []string{"cursor"},
		Attached: map[string][]MCPRef{"cursor": {{Name: "exa", Scope: MCPScopeGlobal, SourcePath: "/cfg"}}},
		Catalog:  []CatalogMCP{{Name: "exa", SourcePath: "/cfg/config.toml"}},
	}
	rep := inspectGeneric(t, Request{Tool: "cursor", ProjectPath: t.TempDir(), Host: host}) // no SessionRef
	cat, _ := rep.Category(CategoryMCP)
	if !strings.Contains(cat.Items[0].Lever.Command, "<session>") {
		t.Fatalf("command = %q: without a session reference the lever must show a visible placeholder rather than a command that could hit the wrong session", cat.Items[0].Lever.Command)
	}
}

func TestGenericAdapterListsUnattachedCatalogEntriesAsFree(t *testing.T) {
	host := &StaticHost{
		MCPTools: []string{"cursor"},
		Attached: map[string][]MCPRef{"cursor": {{Name: "exa", Scope: MCPScopeGlobal, SourcePath: "/cfg"}}},
		Catalog: []CatalogMCP{
			{Name: "exa", SourcePath: "/cfg/config.toml"},
			{Name: "telegram", SourcePath: "/cfg/config.toml"},
		},
	}
	rep := inspectGeneric(t, Request{Tool: "cursor", ProjectPath: t.TempDir(), SessionRef: "s", Host: host})
	cat, ok := rep.Category(CategoryAgentDeckCatalog)
	if !ok {
		t.Fatal("catalog entries not attached here must be listed so the user knows they cost nothing")
	}
	if len(cat.Items) != 1 || cat.Items[0].Label != "telegram" {
		t.Fatalf("items = %+v, want only the unattached entry", cat.Items)
	}
	v, known := cat.Items[0].Load.Actual.Value()
	if !known || v != 0 {
		t.Fatalf("actual = %d (known=%v), want a certain zero", v, known)
	}
	if total, complete := cat.Total(); !complete || total != 0 {
		t.Fatalf("category total = %d (complete=%v), want a complete zero", total, complete)
	}
}

func TestGenericAdapterOmitsTheCatalogCategoryWhenEverythingIsAttached(t *testing.T) {
	host := &StaticHost{
		MCPTools: []string{"cursor"},
		Attached: map[string][]MCPRef{"cursor": {{Name: "exa", Scope: MCPScopeGlobal, SourcePath: "/cfg"}}},
		Catalog:  []CatalogMCP{{Name: "exa", SourcePath: "/cfg/config.toml"}},
	}
	rep := inspectGeneric(t, Request{Tool: "cursor", ProjectPath: t.TempDir(), SessionRef: "s", Host: host})
	if _, ok := rep.Category(CategoryAgentDeckCatalog); ok {
		t.Fatal("an empty category must be omitted rather than shown as a zeroed row")
	}
}

func TestGenericAdapterSurfacesAnUnreadableInstructionFile(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, "AGENTS.md")
	writeFile(t, path, "rules")
	if err := chmodUnreadable(t, path); err != nil {
		t.Skipf("cannot make the file unreadable here: %v", err)
	}

	rep := inspectGeneric(t, Request{Tool: "codex", ProjectPath: project})
	cat, _ := rep.Category(CategoryInstructions)
	if len(cat.Items) != 1 {
		t.Fatalf("found %d items, want the unreadable file still listed", len(cat.Items))
	}
	if cat.Items[0].Content.Prov != TextAbsent {
		t.Fatal("content we could not read must be marked absent, not shown as empty")
	}
	var flagged bool
	for _, cv := range cat.Items[0].Caveats {
		if cv.Code == "instruction-file-unreadable" {
			flagged = true
		}
	}
	if !flagged {
		t.Fatal("a read failure must surface, never as a silently empty file")
	}
}

func TestGenericAdapterMustBeUsedAsAFallbackNotAListEntry(t *testing.T) {
	g := NewGenericAdapter()
	if !g.Supports("claude", NoHost()) {
		t.Fatal("the generic adapter claims every tool, which is why it belongs in the fallback slot")
	}
	if g.Supports("  ", NoHost()) {
		t.Fatal("a nameless tool is not a tool")
	}
}

func TestGenericCapabilitiesAreAnswerableWithoutDisk(t *testing.T) {
	caps := NewGenericAdapter().Capabilities()
	if caps.CanAnchor || caps.CanVerbatimSystem {
		t.Fatal("the generic adapter must not claim capabilities it does not have")
	}
	if len(caps.Categories) == 0 || len(caps.Notes) == 0 {
		t.Fatal("the honest-degradation contract must state what it cannot do")
	}
	for _, c := range caps.Categories {
		if c.Token != TokenUnknown {
			t.Fatalf("category %q declares token provenance %s, want unknown", c.Name, c.Token)
		}
		if c.Note == "" {
			t.Fatalf("category %q must explain its mechanism or its absence", c.Name)
		}
	}
}

func TestNoHostReportsUnavailableRatherThanEmpty(t *testing.T) {
	h := NoHost()
	if _, err := h.AttachedMCPs("claude", "/x"); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("error = %v, want ErrHostUnavailable", err)
	}
	if _, err := h.CatalogMCPs(); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("error = %v, want ErrHostUnavailable", err)
	}
	if !h.IsClaudeCompatible("claude") || h.IsClaudeCompatible("claude-yolo") {
		t.Fatal("with no host wired, compatibility must be exact-name only: under-reporting beats routing to the wrong adapter")
	}
}
