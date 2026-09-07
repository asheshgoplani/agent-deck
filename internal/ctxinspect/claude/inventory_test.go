package claude

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitSkillListing(t *testing.T) {
	tests := []struct {
		name    string
		content string
		names   []string
		want    []string
		wantErr bool
	}{
		{
			name:    "simple pair",
			content: "- alpha: does alpha.\n- beta: does beta.",
			names:   []string{"alpha", "beta"},
			want:    []string{"- alpha: does alpha.", "- beta: does beta."},
		},
		{
			// Descriptions are free text and routinely contain their own list
			// markers and newlines, which is why splitting on line shape would
			// mis-attribute cost between skills.
			name:    "description containing a list marker",
			content: "- alpha: does alpha.\n- not a skill line\n- beta: does beta.",
			names:   []string{"alpha", "beta"},
			want:    []string{"- alpha: does alpha.\n- not a skill line", "- beta: does beta."},
		},
		{
			name:    "plugin-qualified names",
			content: "- firecrawl:scrape: scrapes.\n- doc:pdf: reads pdfs.",
			names:   []string{"firecrawl:scrape", "doc:pdf"},
			want:    []string{"- firecrawl:scrape: scrapes.", "- doc:pdf: reads pdfs."},
		},
		{
			name:    "missing marker fails loudly",
			content: "- alpha: does alpha.",
			names:   []string{"alpha", "beta"},
			wantErr: true,
		},
		{
			name:    "out of order fails loudly",
			content: "- beta: does beta.\n- alpha: does alpha.",
			names:   []string{"alpha", "beta"},
			wantErr: true,
		},
		{
			name:    "no names fails",
			content: "- alpha: does alpha.",
			names:   nil,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitSkillListing(tc.content, tc.names)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error: a mis-split listing silently mis-assigns cost between skills")
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitSkillListing: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries, want %d", len(got), len(tc.want))
			}
			for i := range tc.want {
				if got[i].Listing != tc.want[i] {
					t.Errorf("entry %d = %q, want %q", i, got[i].Listing, tc.want[i])
				}
				if got[i].Name != tc.names[i] {
					t.Errorf("entry %d name = %q, want %q", i, got[i].Name, tc.names[i])
				}
				if got[i].Chars != len([]rune(tc.want[i])) {
					t.Errorf("entry %d chars = %d, want %d", i, got[i].Chars, len([]rune(tc.want[i])))
				}
			}
		})
	}
}

func TestSplitSkillListingCoversWholeInput(t *testing.T) {
	// Every character of the listing must land in exactly one entry, or the
	// per-skill costs will not add up to the listing's cost.
	content := "- alpha: a.\n- beta: b.\n- gamma: c."
	entries, err := SplitSkillListing(content, []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("SplitSkillListing: %v", err)
	}
	var total int
	for _, e := range entries {
		total += e.Chars
	}
	// The only characters not attributed are the newlines trimmed between
	// entries, one per boundary.
	if want := len([]rune(content)) - (len(entries) - 1); total != want {
		t.Fatalf("attributed %d characters, want %d out of %d", total, want, len([]rune(content)))
	}
}

func TestDeriveSkillNames(t *testing.T) {
	// Claude Code versions before mid-2026 record the catalogue without a name
	// list. Recovering the names is what keeps those sessions from losing the
	// entire per-skill breakdown.
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "plain catalogue",
			content: "- dataviz: makes charts.\n- run: runs the app.",
			want:    []string{"dataviz", "run"},
		},
		{
			name:    "plugin-qualified names keep their colon",
			content: "- firecrawl:skill-gen: generates.\n- document-skills:xlsx: reads sheets.",
			want:    []string{"firecrawl:skill-gen", "document-skills:xlsx"},
		},
		{
			// A description's own bullet is not a skill: the token before the
			// colon has spaces in it, which no identifier does.
			name:    "description bullets are rejected",
			content: "- dataviz: makes charts.\n- Also use for: pie charts and more.\n- run: runs the app.",
			want:    []string{"dataviz", "run"},
		},
		{
			name:    "indented lines are not catalogue entries",
			content: "- dataviz: makes charts.\n  - nested: not a skill.",
			want:    []string{"dataviz"},
		},
		{
			name:    "nothing recoverable",
			content: "no entries here at all",
			want:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveSkillNames(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("names = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("names = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestSkillNamesRecoveryIsVerifiedAgainstTheHarnessCount(t *testing.T) {
	// A recovery that disagrees with the count the harness published is wrong
	// by definition, and mis-assigning cost between skills is the failure this
	// feature exists to prevent — so it must be discarded, not used.
	tests := []struct {
		name        string
		listing     SkillListing
		wantNames   int
		wantDerived bool
		wantErr     bool
	}{
		{
			name:      "names present are used as-is",
			listing:   SkillListing{Content: "- a: x.", Names: []string{"a"}, Count: 1},
			wantNames: 1,
		},
		{
			name:        "recovery matching the count is accepted",
			listing:     SkillListing{Content: "- a: x.\n- b: y.", Count: 2},
			wantNames:   2,
			wantDerived: true,
		},
		{
			name:    "recovery disagreeing with the count is discarded",
			listing: SkillListing{Content: "- a: x.\n- b: y.", Count: 5},
			wantErr: true,
		},
		{
			name:    "nothing recoverable",
			listing: SkillListing{Content: "no entries", Count: 3},
			wantErr: true,
		},
		{
			name:        "no count published means the recovery stands alone",
			listing:     SkillListing{Content: "- a: x."},
			wantNames:   1,
			wantDerived: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			names, derived, err := skillNames(&tc.listing)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error; got names %v", names)
				}
				return
			}
			if err != nil {
				t.Fatalf("skillNames: %v", err)
			}
			if len(names) != tc.wantNames {
				t.Errorf("names = %v, want %d", names, tc.wantNames)
			}
			if derived != tc.wantDerived {
				t.Errorf("derived = %v, want %v", derived, tc.wantDerived)
			}
		})
	}
}

func TestSplitAgentListing(t *testing.T) {
	got, err := SplitAgentListing([]string{"a", "b"}, []string{"- a: one", "- b: two"})
	if err != nil {
		t.Fatalf("SplitAgentListing: %v", err)
	}
	if len(got) != 2 || got[1].Name != "b" || got[1].Listing != "- b: two" {
		t.Fatalf("entries = %+v", got)
	}
	if _, err := SplitAgentListing([]string{"a"}, []string{"- a: one", "- b: two"}); err == nil {
		t.Fatal("want an error on a length mismatch rather than a silent truncation")
	}
}

func TestMCPServerForTool(t *testing.T) {
	tests := []struct {
		name       string
		want       string
		wantIsMCP  bool
		inputIsMCP string
	}{
		{inputIsMCP: "mcp__telegram__reply", want: "telegram", wantIsMCP: true},
		{inputIsMCP: "mcp__plugin_telegram_telegram__react", want: "plugin_telegram_telegram", wantIsMCP: true},
		{inputIsMCP: "WebFetch", want: "", wantIsMCP: false},
		{inputIsMCP: "mcp__nodoubleunderscore", want: "", wantIsMCP: false},
		{inputIsMCP: "mcp____empty", want: "", wantIsMCP: false},
	}
	for _, tc := range tests {
		t.Run(tc.inputIsMCP, func(t *testing.T) {
			got, ok := mcpServerForTool(tc.inputIsMCP)
			if ok != tc.wantIsMCP || got != tc.want {
				t.Fatalf("mcpServerForTool(%q) = (%q, %v), want (%q, %v)", tc.inputIsMCP, got, ok, tc.want, tc.wantIsMCP)
			}
		})
	}
}

func TestPluginNameFromSkillPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/h/.claude/plugins/cache/marketplace/firecrawl/abc123/skills/scrape/SKILL.md", "firecrawl"},
		{"/h/.claude/plugins/cache/anthropic/document-skills/9d2/skills/xlsx/SKILL.md", "document-skills"},
		{"/h/.claude/skills/dataviz/SKILL.md", ""},
	}
	for _, tc := range tests {
		if got := pluginNameFromSkillPath(tc.in); got != tc.want {
			t.Errorf("pluginNameFromSkillPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDiscoverSkills(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "repo")

	writeFile(t, filepath.Join(config, "skills", "dataviz", "SKILL.md"), "body")
	writeFile(t, filepath.Join(config, "plugins", "cache", "mk", "firecrawl", "v1", "skills", "scrape", "SKILL.md"), "body")
	writeFile(t, filepath.Join(project, ".claude", "skills", "local", "SKILL.md"), "body")
	// A directory with no SKILL.md is not a skill.
	writeFile(t, filepath.Join(config, "skills", "notaskill", "README.md"), "x")

	got := DiscoverSkills(config, project)
	byName := map[string]DiskSkill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	for _, want := range []struct{ name, source string }{
		{"dataviz", "user"},
		{"firecrawl:scrape", "plugin"},
		{"local", "project"},
	} {
		s, ok := byName[want.name]
		if !ok {
			t.Fatalf("missing skill %q; found %v", want.name, byName)
		}
		if s.Source != want.source {
			t.Errorf("%s source = %q, want %q", want.name, s.Source, want.source)
		}
		if s.Size == 0 {
			t.Errorf("%s has no size; potential cost would be unreportable", want.name)
		}
		if s.Dir == "" || !strings.HasSuffix(s.Path, "SKILL.md") {
			t.Errorf("%s = %+v, want the SKILL.md path and its directory", want.name, s)
		}
	}
	if _, ok := byName["notaskill"]; ok {
		t.Error("a directory without SKILL.md must not be reported as a skill")
	}
}

func TestDiscoverAgents(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	project := filepath.Join(root, "repo")
	writeFile(t, filepath.Join(config, "agents", "mine.md"), "x")
	writeFile(t, filepath.Join(project, ".claude", "agents", "theirs.md"), "x")

	got := DiscoverAgents(config, project)
	if len(got) != 2 {
		t.Fatalf("agents = %+v, want two", got)
	}
	bySource := map[string]string{}
	for _, a := range got {
		bySource[a.Name] = a.Source
	}
	if bySource["mine"] != "user" || bySource["theirs"] != "project" {
		t.Fatalf("sources = %v", bySource)
	}
}

func TestDiscoverSkillsEmptyInputs(t *testing.T) {
	if got := DiscoverSkills("", ""); len(got) != 0 {
		t.Fatalf("DiscoverSkills with no directories = %+v, want none", got)
	}
	if got := DiscoverAgents("", ""); len(got) != 0 {
		t.Fatalf("DiscoverAgents with no directories = %+v, want none", got)
	}
}
