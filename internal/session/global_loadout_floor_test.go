package session

import (
	"reflect"
	"testing"
)

// The global [claude]/[codex] loadout floor resolves as the outermost
// ancestor: every group inherits it, an ungrouped session still receives it,
// and a group can add to it but never subtract from it.

func TestGlobalCodexLoadoutFloorAppliesToUngroupedSession(t *testing.T) {
	cfg := &UserConfig{
		Codex: CodexSettings{
			Plugins:      []string{"andrej-karpathy-skills@karpathy-skills"},
			Marketplaces: []string{"/srv/karpathy"},
			Skills:       []string{"shared/port-registry"},
		},
	}

	if got := cfg.GetGroupCodexPlugins(""); !reflect.DeepEqual(got, []string{"andrej-karpathy-skills@karpathy-skills"}) {
		t.Fatalf("ungrouped session lost the global plugin floor: %v", got)
	}
	if got := cfg.GetGroupCodexMarketplaces(""); !reflect.DeepEqual(got, []string{"/srv/karpathy"}) {
		t.Fatalf("ungrouped session lost the global marketplace floor: %v", got)
	}
	if got := cfg.GetGroupCodexSkills(""); !reflect.DeepEqual(got, []string{"shared/port-registry"}) {
		t.Fatalf("ungrouped session lost the global skill floor: %v", got)
	}
}

func TestGlobalCodexLoadoutFloorPrecedesGroupEntriesAndDedupes(t *testing.T) {
	cfg := &UserConfig{
		Codex: CodexSettings{
			Plugins: []string{"global@mp", "shared@mp"},
		},
		Groups: map[string]GroupSettings{
			"team": {Codex: GroupCodexSettings{
				Plugins: []string{"shared@mp", "team@mp"},
			}},
			"team/sub": {Codex: GroupCodexSettings{
				Plugins: []string{"leaf@mp"},
			}},
		},
	}

	// Root-first ordering: global floor, then the ancestor chain, then leaf.
	// "shared@mp" appears in both global and group and must not duplicate.
	want := []string{"global@mp", "shared@mp", "team@mp", "leaf@mp"}
	if got := cfg.GetGroupCodexPlugins("team/sub"); !reflect.DeepEqual(got, want) {
		t.Fatalf("global floor did not merge root-first:\n got %v\nwant %v", got, want)
	}
}

func TestGroupCannotSubtractFromGlobalCodexFloor(t *testing.T) {
	cfg := &UserConfig{
		Codex: CodexSettings{MCPs: []string{"house-mcp"}},
		Groups: map[string]GroupSettings{
			// Declares its own MCP list and deliberately omits house-mcp.
			"team": {Codex: GroupCodexSettings{MCPs: []string{"team-mcp"}}},
		},
	}

	got := cfg.GetGroupCodexMCPs("team")
	want := []string{"house-mcp", "team-mcp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("group omission must not detach the global floor:\n got %v\nwant %v", got, want)
	}
}

func TestGlobalClaudeLoadoutFloorAppliesToUngroupedSession(t *testing.T) {
	cfg := &UserConfig{
		Claude: ClaudeSettings{
			Skills:  []string{"shared/port-registry"},
			Plugins: []string{"agent-deck"},
			MCPs:    []string{"dart"},
		},
	}

	if got := cfg.GetGroupClaudeSkills(""); !reflect.DeepEqual(got, []string{"shared/port-registry"}) {
		t.Fatalf("ungrouped Claude session lost the global skill floor: %v", got)
	}
	if got := cfg.GetGroupClaudePlugins(""); !reflect.DeepEqual(got, []string{"agent-deck"}) {
		t.Fatalf("ungrouped Claude session lost the global plugin floor: %v", got)
	}
	if got := cfg.GetGroupClaudeMCPs(""); !reflect.DeepEqual(got, []string{"dart"}) {
		t.Fatalf("ungrouped Claude session lost the global MCP floor: %v", got)
	}
}

func TestGlobalClaudeLoadoutFloorPrecedesGroupEntries(t *testing.T) {
	cfg := &UserConfig{
		Claude: ClaudeSettings{Plugins: []string{"agent-deck", "i-have-adhd"}},
		Groups: map[string]GroupSettings{
			"doozyx":            {Claude: GroupClaudeSettings{Plugins: []string{"i-have-adhd", "frontend-design"}}},
			"doozyx/agent-deck": {Claude: GroupClaudeSettings{Plugins: []string{"playwright"}}},
		},
	}

	want := []string{"agent-deck", "i-have-adhd", "frontend-design", "playwright"}
	if got := cfg.GetGroupClaudePlugins("doozyx/agent-deck"); !reflect.DeepEqual(got, want) {
		t.Fatalf("global Claude floor did not merge root-first:\n got %v\nwant %v", got, want)
	}
}

// A config with neither a global floor nor group entries must still return nil
// rather than an empty non-nil slice — callers treat nil as "nothing to do".
func TestEmptyLoadoutStaysNil(t *testing.T) {
	cfg := &UserConfig{Groups: map[string]GroupSettings{"team": {}}}

	if got := cfg.GetGroupClaudePlugins("team"); got != nil {
		t.Fatalf("expected nil Claude loadout, got %v", got)
	}
	if got := cfg.GetGroupCodexPlugins("team"); got != nil {
		t.Fatalf("expected nil Codex loadout, got %v", got)
	}
}
