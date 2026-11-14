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

// openCodeGlobalAttach and openCodeGlobalDetach follow `agent-deck mcp attach
// --global` / `mcp detach --global` for an OpenCode session: read the current
// global names through the reader, add or drop one, and hand the full list to
// the same writer (WriteOpenCodeGlobalMCP). Detach has no separate code path.
func openCodeGlobalAttach(t *testing.T, name string, v2 bool) {
	t.Helper()
	names := append(getOpenCodeMCPInfoUncached("").Global, name)
	if err := WriteOpenCodeGlobalMCP(names, v2); err != nil {
		t.Fatal(err)
	}
}

func openCodeGlobalDetach(t *testing.T, name string, v2 bool) {
	t.Helper()
	var names []string
	found := false
	for _, n := range getOpenCodeMCPInfoUncached("").Global {
		if n == name {
			found = true
			continue
		}
		names = append(names, n)
	}
	if !found {
		t.Fatalf("detach %q: not attached according to the reader", name)
	}
	if err := WriteOpenCodeGlobalMCP(names, v2); err != nil {
		t.Fatal(err)
	}
}

// Reviewer request on #3143: servers named "type" and "servers" in one V2
// config, attach and detach, plus "type": null in the container and a V2
// write over a legacy server named "servers". Each step checks the exact mcp
// map and what the reader lists.
func TestOpenCodeNamedServersAttachDetach(t *testing.T) {
	const catJSON = `{"type": "remote", "url": "https://cat.invalid/mcp", "headers": {"Authorization": "Bearer cat"}}`
	const typeAndServers = `"type": {"type": "remote", "url": "https://t.invalid/mcp", "headers": {"Authorization": "Bearer t"}},
		"servers": {"type": "local", "command": ["srv"], "environment": {"K": "v"}},
		"keep": {"type": "remote", "url": "https://keep.invalid/mcp", "headers": {"X-Keep": "1"}, "timeout": 3000}`
	const typeNull = `"type": null,
		"ok": {"type": "remote", "url": "https://ok.invalid/mcp", "headers": {"X-Ok": "1"}}`
	const legacy = `"servers": {"type": "local", "command": ["legacy-srv"], "environment": {"L": "1"}},
		"gh": {"type": "remote", "url": "https://gh.invalid/mcp", "headers": {"Authorization": "Bearer gh"}}`

	cases := []struct {
		name       string
		seed       string // the mcp object
		v2         bool
		names      []string // reader's global names before attach
		wantAttach string
		wantDetach string
	}{
		{"v2 type and servers/v2", `{"servers": {` + typeAndServers + `}}`, true, []string{"keep", "servers", "type"},
			`{"servers": {` + typeAndServers + `, "cat": ` + catJSON + `}}`, `{"servers": {` + typeAndServers + `}}`},
		{"v2 type and servers/legacy-call", `{"servers": {` + typeAndServers + `}}`, false, []string{"keep", "servers", "type"},
			`{"servers": {` + typeAndServers + `, "cat": ` + catJSON + `}}`, `{"servers": {` + typeAndServers + `}}`},
		{"v2 type null/v2", `{"servers": {` + typeNull + `}}`, true, []string{"ok", "type"},
			`{"servers": {` + typeNull + `, "cat": ` + catJSON + `}}`, `{"servers": {` + typeNull + `}}`},
		{"v2 type null/legacy-call", `{"servers": {` + typeNull + `}}`, false, []string{"ok", "type"},
			`{"servers": {` + typeNull + `, "cat": ` + catJSON + `}}`, `{"servers": {` + typeNull + `}}`},
		// A V2 write over a legacy config moves the legacy entries, the one
		// named "servers" included, into the V2 container (one level, not two).
		{"legacy servers/v2", `{` + legacy + `}`, true, []string{"gh", "servers"},
			`{"servers": {` + legacy + `, "cat": ` + catJSON + `}}`, `{"servers": {` + legacy + `}}`},
		{"legacy servers/legacy-call", `{` + legacy + `}`, false, []string{"gh", "servers"},
			`{` + legacy + `, "cat": ` + catJSON + `}`, `{` + legacy + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := setupOpenCodeMergeFixture(t)
			if err := os.WriteFile(path, []byte(`{"model": "x/y", "mcp": `+tc.seed+`}`), 0o600); err != nil {
				t.Fatal(err)
			}
			check := func(step, want string, wantNames []string) {
				t.Helper()
				var wantMCP map[string]any
				if err := json.Unmarshal([]byte(want), &wantMCP); err != nil {
					t.Fatalf("bad want JSON: %v", err)
				}
				root, mcp := readOpenCodeMCPMap(t, path)
				if root["model"] != "x/y" {
					t.Fatalf("%s: non-mcp key lost: %v", step, root)
				}
				if !reflect.DeepEqual(mcp, wantMCP) {
					t.Fatalf("%s: mcp map\n got: %v\nwant: %v", step, mcp, wantMCP)
				}
				got := getOpenCodeMCPInfoUncached("").Global
				if !reflect.DeepEqual(got, wantNames) {
					t.Fatalf("%s: reader lists %v, want %v", step, got, wantNames)
				}
			}
			check("seed", tc.seed, tc.names)

			openCodeGlobalAttach(t, "cat", tc.v2)
			withCat := append([]string{"cat"}, tc.names...)
			check("attach", tc.wantAttach, withCat)

			openCodeGlobalDetach(t, "cat", tc.v2)
			check("detach", tc.wantDetach, tc.names)
		})
	}
}
