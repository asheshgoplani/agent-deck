package session

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Issue #3104 follow-up: the legacy/V2 shape detection looks inside
// mcp.servers for a "type" key. A native V2 config whose servers map holds a
// server literally named "type" (or any other name that matches a key the
// detector or a server entry uses) must still be read and written as V2, not
// as a legacy server named "servers" that then gets nested one level deeper.
func TestOpenCodeV2ServerNamedLikeAKey(t *testing.T) {
	names := []string{"type", "command", "url", "servers", "mcp", "enabled", "headers", "args", "env", "environment", "timeout"}
	for _, name := range names {
		for _, v2 := range []bool{true, false} {
			t.Run(name+map[bool]string{true: "/v2", false: "/legacy-call"}[v2], func(t *testing.T) {
				path := setupOpenCodeMergeFixture(t)
				user := map[string]any{"type": "remote", "url": "https://named.invalid/mcp", "timeout": float64(9000)}
				seed, err := json.Marshal(map[string]any{
					"model": "x/y",
					"mcp":   map[string]any{"servers": map[string]any{name: user}},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, seed, 0o600); err != nil {
					t.Fatal(err)
				}

				// Reader: the V2 container must be recognised as such.
				var before opencodeConfig
				if err := json.Unmarshal(seed, &before); err != nil {
					t.Errorf("reader failed on V2 config with server %q: %v", name, err)
				}
				if _, ok := before.MCP[name]; !ok || len(before.MCP) != 1 {
					t.Errorf("reader misread V2 config with server %q: %v", name, before.MCP)
				}

				if err := WriteOpenCodeGlobalMCP([]string{"cat"}, v2); err != nil {
					t.Fatal(err)
				}
				root, mcp := readOpenCodeMCPMap(t, path)
				if root["model"] != "x/y" {
					t.Fatalf("non-mcp key lost: %v", root)
				}
				if len(mcp) != 1 {
					t.Fatalf("mcp should hold only the servers container, got: %v", mcp)
				}
				servers, _ := mcp["servers"].(map[string]any)
				if !reflect.DeepEqual(servers[name], user) {
					t.Fatalf("server %q moved or changed (extra servers level?): %v", name, mcp)
				}
				if servers["cat"] == nil || len(servers) != 2 {
					t.Fatalf("attached entry not written beside %q: %v", name, mcp)
				}

				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var after opencodeConfig
				if err := json.Unmarshal(data, &after); err != nil {
					t.Fatalf("reader failed after write: %v", err)
				}
				if _, ok := after.MCP[name]; !ok {
					t.Fatalf("reader lost %q after write: %v", name, after.MCP)
				}
				if _, ok := after.MCP["cat"]; !ok {
					t.Fatalf("reader does not see the attached entry: %v", after.MCP)
				}
			})
		}
	}
}
