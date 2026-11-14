package assistant

import (
	"strings"
	"testing"
)

// A URL given with the request (or --url) is printed into shell commands.
// It must stay literal data when the command is pasted: no command
// substitution, no variable expansion, no word splitting.
func TestPrintedCommandsQuoteTheURL(t *testing.T) {
	const evil = "https://deck.example/$(id)`id`\"x"
	for _, id := range []string{"http", "slack", "telegram", "any"} {
		kind, ok := LookupKind(id)
		if !ok {
			t.Fatalf("unknown kind %s", id)
		}
		rec := Record{Name: id, Assistant: id, Scopes: DefaultScopes}
		c := Render(kind, rec, "adk_TOKEN", "/tmp/"+id+".token", evil, "/usr/local/bin/agent-deck")
		for _, line := range strings.Split(c.Block, "\n") {
			isCurl := strings.HasPrefix(line, "curl ")
			if !isCurl && !strings.HasPrefix(line, "AGENTDECK_URL=") {
				continue
			}
			if isCurl && strings.Contains(line, "$AGENTDECK_URL") {
				// The env-style examples expand the variable on purpose.
				if !strings.Contains(line, `"$AGENTDECK_URL/api/assistant/v1/`) {
					t.Errorf("%s: env-style curl lost its variable: %s", id, line)
				}
				continue
			}
			if !strings.Contains(line, `'https://deck.example/$(id)`+"`id`"+`"x`) {
				t.Errorf("%s: URL not single-quoted as literal data:\n%s", id, line)
			}
		}
	}
	// A plain URL keeps the old, readable form.
	kind, _ := LookupKind("http")
	c := Render(kind, Record{Name: "http", Assistant: "http", Scopes: DefaultScopes}, "adk_TOKEN", "/tmp/t", "http://127.0.0.1:8420", "/usr/local/bin/agent-deck")
	if !strings.Contains(c.Block, "AGENTDECK_URL=http://127.0.0.1:8420\n") {
		t.Errorf("plain URL assignment changed:\n%s", c.Block)
	}
	for _, id := range []string{"any"} {
		k, _ := LookupKind(id)
		c := Render(k, Record{Name: id, Assistant: id, Scopes: DefaultScopes}, "adk_TOKEN", "/tmp/t", "http://127.0.0.1:8420", "/usr/local/bin/agent-deck")
		if !strings.Contains(c.Block, `"http://127.0.0.1:8420/api/assistant/v1/sessions"`) {
			t.Errorf("%s: plain URL curl lost its double quotes:\n%s", id, c.Block)
		}
	}
}
