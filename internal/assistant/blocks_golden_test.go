package assistant

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateBlocks = flag.Bool("update", false, "rewrite testdata/blocks goldens and docs/assistants/skills")

// goldenConnection renders kindID with a fixed token, path and URL so the
// block is byte-stable.
func goldenConnection(t *testing.T, kindID string) *Connection {
	t.Helper()
	kind, ok := LookupKind(kindID)
	if !ok {
		t.Fatalf("unknown kind %s", kindID)
	}
	name := kindID
	if kindID == DefaultKind {
		name = "assistant"
	}
	rec := Record{Name: name, Assistant: kindID, Scopes: DefaultScopes}
	return Render(kind, rec, "adk_GOLDENTOKEN", "/home/me/.local/share/agent-deck/assistants/"+name+".token", DefaultURL, "/usr/local/bin/agent-deck")
}

func checkGolden(t *testing.T, path, got string) {
	t.Helper()
	if *updateBlocks {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run with -update)", path, err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the rendered output (run with -update and review the diff)\n--- got ---\n%s", path, got)
	}
}

// TestBlockGoldens pins the whole copy-paste block of every assistant.
func TestBlockGoldens(t *testing.T) {
	for _, id := range KindIDs() {
		t.Run(id, func(t *testing.T) {
			checkGolden(t, filepath.Join("testdata", "blocks", id+".golden"), goldenConnection(t, id).Block)
		})
	}
}

// TestSkillFilesInDocs keeps docs/assistants/skills/<kind>/SKILL.md equal to
// SkillFile, so the ready file the block links to never drifts.
func TestSkillFilesInDocs(t *testing.T) {
	kinds := SkillKinds()
	for _, want := range []string{"hermes", "openclaw", "claude", "codex"} {
		if !contains(kinds, want) {
			t.Fatalf("SkillKinds %v misses %s", kinds, want)
		}
	}
	for _, id := range kinds {
		path := filepath.Join("..", "..", "docs", "assistants", "skills", id, "SKILL.md")
		got := SkillFile(id)
		if !strings.HasPrefix(got, "---\nname: agent-deck\ndescription: ") || !strings.Contains(got, "ASK THE USER") {
			t.Fatalf("%s: bad skill file:\n%s", id, got)
		}
		checkGolden(t, path, got)
		c := goldenConnection(t, id)
		if c.SkillURL != skillURLBase+id+"/SKILL.md" || !strings.Contains(c.Block, c.SkillURL) || !strings.Contains(c.Block, c.SkillPath) {
			t.Fatalf("%s: block does not reference its skill file", id)
		}
	}
}

// TestBlockShapes checks the per-assistant facts the research pass verified.
func TestBlockShapes(t *testing.T) {
	tf := "/home/me/.local/share/agent-deck/assistants/"
	cases := map[string][]string{
		"any":      {SetupPromptURL, `"mcpServers"`, `"agentdeck"`, "Authorization: Bearer adk_GOLDENTOKEN", "/api/assistant/v1/sessions"},
		"hermes":   {"hermes mcp add agentdeck --command /usr/local/bin/agent-deck --args connect serve --token-file " + tf + "hermes.token", "mcp_servers:\n  agentdeck:\n", "trust: untrusted", "hermes mcp test agentdeck", "~/.hermes/skills/agent-deck/SKILL.md"},
		"openclaw": {"openclaw mcp set agentdeck '{\"command\":\"/usr/local/bin/agent-deck\",\"args\":[\"connect\",\"serve\",\"--token-file\",\"" + tf + "openclaw.token\"]}'", `"mcp": {`, `"servers": {`, "openclaw mcp probe", "~/.openclaw/workspace/skills/agent-deck/SKILL.md"},
		"grok":     {`"type": "mcp"`, `"server_url": "http://127.0.0.1:8420/mcp"`, `"server_label": "agentdeck"`, `"authorization": "adk_GOLDENTOKEN"`, "api.x.ai/v1/responses", "tailscale funnel"},
		"claude":   {"claude mcp add -s user agentdeck -- /usr/local/bin/agent-deck connect serve --token-file " + tf + "claude.token", "claude_desktop_config.json", `"mcpServers"`, "~/.claude/skills/agent-deck/SKILL.md"},
		"chatgpt":  {"codex mcp add agentdeck -- ", `"require_approval"`, `"never"`, "cannot send a fixed token"},
		"codex":    {"codex mcp add agentdeck -- /usr/local/bin/agent-deck connect serve --token-file " + tf + "codex.token", "[mcp_servers.agentdeck]", "codex mcp list", "~/.agents/skills/agent-deck/SKILL.md"},
		"slack":    {"AGENTDECK_URL=http://127.0.0.1:8420", TokenEnvVar + "=adk_GOLDENTOKEN", "$AGENTDECK_URL/api/assistant/v1/sessions", "conductor setup", "--url http://127.0.0.1:8420"},
		"telegram": {"AGENTDECK_URL=http://127.0.0.1:8420", TokenEnvVar + "=adk_GOLDENTOKEN", "conductor setup", "telegram.md"},
		"mcp":      {`"mcpServers"`, `"type": "http"`, `"url": "http://127.0.0.1:8420/mcp"`},
		"http":     {"/whoami", "/sessions/ID/approve", TokenEnvVar},
	}
	for _, id := range KindIDs() {
		want, ok := cases[id]
		if !ok {
			t.Fatalf("no shape case for kind %s", id)
		}
		c := goldenConnection(t, id)
		for _, w := range want {
			if !strings.Contains(c.Block, w) {
				t.Errorf("%s: block misses %q", id, w)
			}
		}
		// Every block: the guide, the test command, the revoke line.
		for _, w := range []string{"ASK THE USER", "connect test --name " + c.Name, "connect revoke " + c.Name, "--scopes"} {
			if !strings.Contains(c.Block, w) {
				t.Errorf("%s: block misses %q", id, w)
			}
		}
		if n := strings.Count(c.ContextPrompt, "\n"); n > 25 {
			t.Errorf("%s: context prompt has %d lines, keep it under 25", id, n)
		}
	}
}

// TestSnippetsParse checks that the JSON parts of the snippets are valid
// and that scoped tool lists follow the token's scopes.
func TestSnippetsParse(t *testing.T) {
	kind, _ := LookupKind("grok")
	rec := Record{Name: "grok", Assistant: "grok", Scopes: []Scope{ScopeRead, ScopeSend}}
	c := Render(kind, rec, "adk_X", "/t", "https://deck.example.ts.net", "agent-deck")
	var entry struct {
		AllowedTools []string `json:"allowed_tools"`
		ServerURL    string   `json:"server_url"`
	}
	if err := json.Unmarshal([]byte(c.ConfigSnippet), &entry); err != nil {
		t.Fatalf("grok snippet is not JSON: %v\n%s", err, c.ConfigSnippet)
	}
	if strings.Join(entry.AllowedTools, ",") != "whoami,list_sessions,session_status,read_output,send_message" || entry.ServerURL != "https://deck.example.ts.net/mcp" {
		t.Fatalf("grok entry %+v", entry)
	}
	if !strings.Contains(c.TestCommand, "--url https://deck.example.ts.net") {
		t.Fatalf("http kind test command %q", c.TestCommand)
	}
	for _, id := range []string{"openclaw", "mcp"} {
		k, _ := LookupKind(id)
		c := Render(k, Record{Name: id, Assistant: id, Scopes: DefaultScopes}, "adk_X", "/t", "", "agent-deck")
		js := c.ConfigSnippet[strings.Index(c.ConfigSnippet, "\n{")+1:]
		var v map[string]any
		if err := json.NewDecoder(strings.NewReader(js)).Decode(&v); err != nil {
			t.Fatalf("%s JSON part does not parse: %v\n%s", id, err, js)
		}
	}
}

// TestToolAnnotations: read tools are marked read-only, write tools not.
func TestToolAnnotations(t *testing.T) {
	for _, tool := range Tools() {
		ro, ok := tool.Annotations["readOnlyHint"].(bool)
		if !ok || ro == tool.Write {
			t.Errorf("%s: readOnlyHint %v, write %v", tool.Name, tool.Annotations["readOnlyHint"], tool.Write)
		}
	}
}

// TestSetupPromptCoversEveryKind keeps the primary door in step with the
// kinds and the rules: every kind is in its table, and the safety rules
// and the read-only start are spelled out.
func TestSetupPromptCoversEveryKind(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "assistants", "SETUP-PROMPT.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, id := range KindIDs() {
		if id == DefaultKind {
			continue
		}
		if !strings.Contains(doc, "`"+id+"`") {
			t.Errorf("SETUP-PROMPT.md does not list kind %s", id)
		}
	}
	for _, w := range []string{"agent-deck version", "install.sh", "agent-deck connect <kind> --json", "connect serve --token-file",
		"list_sessions", "--scopes read,send", "connect revoke", "Never show the token", "never stop, delete, restart or kill", ServerKey} {
		if !strings.Contains(doc, w) {
			t.Errorf("SETUP-PROMPT.md misses %q", w)
		}
	}
	if !strings.Contains(doc, SetupPromptURL) {
		t.Error("SETUP-PROMPT.md does not quote its own URL in the paste-in line")
	}
}
