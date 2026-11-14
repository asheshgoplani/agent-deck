package assistant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hermesTrustCommand marks the agentdeck server untrusted after `hermes mcp
// add`, which has no trust option: Hermes treats a server without a trust
// key as fully trusted and runs send_message, approve and launch_session
// without asking.
const hermesTrustCommand = "hermes config set mcp_servers." + ServerKey + ".trust untrusted"

// Every `hermes mcp add` we print (the block and the setup prompt) sets
// trust: untrusted on the same line, like the YAML variant does.
func TestHermesCLIInstallSetsUntrustedTrust(t *testing.T) {
	block := goldenConnection(t, "hermes").Block
	prompt, err := os.ReadFile(filepath.Join("..", "..", "docs", "assistants", "SETUP-PROMPT.md"))
	if err != nil {
		t.Fatal(err)
	}
	for src, text := range map[string]string{"hermes block": block, "SETUP-PROMPT.md": string(prompt)} {
		found := 0
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "hermes mcp add "+ServerKey+" ") || strings.Contains(line, "--url") {
				continue
			}
			found++
			if !strings.Contains(line, "&& "+hermesTrustCommand) {
				t.Errorf("%s: hermes CLI install leaves the server fully trusted:\n%s\nwant it followed by: && %s", src, line, hermesTrustCommand)
			}
		}
		if found == 0 {
			t.Errorf("%s: no `hermes mcp add %s` line found", src, ServerKey)
		}
	}
	if !strings.Contains(block, "trust: untrusted") {
		t.Error("hermes block: the YAML variant lost trust: untrusted")
	}
}
