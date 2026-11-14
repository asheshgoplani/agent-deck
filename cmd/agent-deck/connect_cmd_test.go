package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runConnectSubprocess runs the built binary with stdin in the goldens
// sandbox (private HOME, hermetic tmux).
func runConnectSubprocess(t *testing.T, bin string, env []string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = withBinaryFirstOnPath(env, bin)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}
	return out.String(), errOut.String(), code
}

func TestConnectCLIEndToEnd(t *testing.T) {
	bin := goldensBinary(t)
	_, env := goldensSandbox(t)

	out, stderr, code := runConnectSubprocess(t, bin, env, "", "connect", "hermes", "--json")
	if code != 0 {
		t.Fatalf("connect hermes --json: exit %d\n%s\n%s", code, out, stderr)
	}
	var conn struct {
		Name      string   `json:"name"`
		Token     string   `json:"token"`
		TokenFile string   `json:"token_file"`
		Scopes    []string `json:"scopes"`
		MCPStdio  struct {
			Command string
			Args    []string
		} `json:"mcp_stdio"`
		ConfigSnippet string `json:"config_snippet"`
		ContextPrompt string `json:"context_prompt"`
	}
	if err := json.Unmarshal([]byte(out), &conn); err != nil {
		t.Fatalf("connect --json output: %v\n%s", err, out)
	}
	if conn.Name != "hermes" || strings.Join(conn.Scopes, ",") != "read" || !strings.HasPrefix(conn.Token, "adk_") {
		t.Fatalf("connection: %+v", conn)
	}
	if !strings.Contains(conn.ConfigSnippet, "mcp_servers:") || !strings.Contains(conn.ContextPrompt, "ASK THE USER") {
		t.Fatalf("snippet/prompt missing:\n%s\n%s", conn.ConfigSnippet, conn.ContextPrompt)
	}
	info, err := os.Stat(conn.TokenFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file %s: %v %v", conn.TokenFile, info, err)
	}

	out, _, code = runConnectSubprocess(t, bin, env, "", "connect", "list", "--json")
	if code != 0 || !strings.Contains(out, `"name": "hermes"`) {
		t.Fatalf("connect list --json: %d\n%s", code, out)
	}

	// The stdio bridge: initialize, tools/list and whoami (no CLI exec).
	script := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"send_message","arguments":{"session":"x","message":"BODY-NOT-LOGGED"}}}`,
	}, "\n") + "\n"
	out, stderr, code = runConnectSubprocess(t, bin, env, script, "connect", "serve", "--token-file", conn.TokenFile)
	if code != 0 {
		t.Fatalf("connect serve: exit %d\n%s", code, stderr)
	}
	var replies []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("stdout carries a non-JSON line %q (stdout is the protocol channel)", sc.Text())
		}
		replies = append(replies, m)
	}
	if len(replies) != 4 {
		t.Fatalf("got %d replies, want 4:\n%s", len(replies), out)
	}
	if !strings.Contains(out, `"name":"agent-deck"`) || !strings.Contains(out, `"launch_session"`) || !strings.Contains(out, `\"name\": \"hermes\"`) {
		t.Fatalf("unexpected replies:\n%s", out)
	}
	denied := replies[3]["result"].(map[string]any)
	if denied["isError"] != true || !strings.Contains(out, "scope_missing") {
		t.Fatalf("send_message with read scope was not refused: %v", denied)
	}

	out, _, code = runConnectSubprocess(t, bin, env, "", "connect", "audit", "--json")
	if code != 0 || !strings.Contains(out, `"tool": "send_message"`) || strings.Contains(out, "BODY-NOT-LOGGED") {
		t.Fatalf("connect audit --json: %d\n%s", code, out)
	}

	if _, _, code = runConnectSubprocess(t, bin, env, "", "connect", "revoke", "hermes"); code != 0 {
		t.Fatalf("revoke exit %d", code)
	}
	_, _, code = runConnectSubprocess(t, bin, env, script, "connect", "serve", "--token-file", conn.TokenFile)
	if code == 0 {
		t.Fatal("connect serve started with a revoked token")
	}
	if _, _, code = runConnectSubprocess(t, bin, env, "", "connect", "nope"); code == 0 {
		t.Fatal("unknown assistant accepted")
	}
	if _, _, code = runConnectSubprocess(t, bin, env, "", "connect", "--scopes", "read,delete"); code == 0 {
		t.Fatal("unknown scope accepted")
	}
}
