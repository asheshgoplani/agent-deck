package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/assistant"
)

// writeToolCalls is one request per write tool on each assistant transport.
// TestAssistantWriteToolsCovered keeps it in step with assistant.Tools().
var writeToolCalls = map[string]struct {
	mcpArgs   string
	apiMethod string
	apiPath   string
	apiBody   string
}{
	assistant.ToolSendMessage: {`{"session":"s1","message":"hi"}`, http.MethodPost, "/api/assistant/v1/sessions/s1/send", `{"message":"hi"}`},
	assistant.ToolApprove:     {`{"session":"s1","choice":"once"}`, http.MethodPost, "/api/assistant/v1/sessions/s1/approve", `{"choice":"once"}`},
	assistant.ToolLaunchSession: {`{"path":"/tmp","title":"t","tool":"claude"}`, http.MethodPost, "/api/assistant/v1/sessions",
		`{"path":"/tmp","title":"t","tool":"claude"}`},
}

func TestAssistantWriteToolsCovered(t *testing.T) {
	for _, tool := range assistant.Tools() {
		if _, ok := writeToolCalls[tool.Name]; tool.Write && !ok {
			t.Errorf("write tool %q has no entry in writeToolCalls: add one so the read-only tests cover it", tool.Name)
		}
		if _, ok := writeToolCalls[tool.Name]; !tool.Write && ok {
			t.Errorf("tool %q is in writeToolCalls but is not a write tool", tool.Name)
		}
	}
}

// newWriteScopedAssistant wires the assistant service like
// newAssistantServer and returns a token that holds every scope.
func newWriteScopedAssistant(t *testing.T, cfg Config) (*Server, string, *fakeAssistantExec) {
	t.Helper()
	srv, _, fx := newAssistantServer(t, cfg)
	tok, _, err := srv.assistant.svc.Store.Create("writer", "mcp", assistant.AllScopes, "")
	if err != nil {
		t.Fatal(err)
	}
	return srv, tok, fx
}

func mcpToolCall(name, args string) string {
	return `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

// [web].mutations_enabled = false must make the assistant surface read-only
// too: a token minted from the CLI with send/approve/launch must not drive
// sessions through /mcp or /api/assistant/v1/ on such a server.
func TestAssistantRoutes_MutationsDisabledRefusesEveryWriteTool(t *testing.T) {
	for _, cfg := range []Config{
		{WebMutations: false},
		{WebMutations: false, Token: "owner-secret"},
		{WebMutations: true, ReadOnly: true},
	} {
		srv, tok, fx := newWriteScopedAssistant(t, cfg)
		for name, c := range writeToolCalls {
			rr := serve(srv, http.MethodPost, "/mcp", "Bearer "+tok, mcpToolCall(name, c.mcpArgs))
			var resp struct {
				Result struct {
					IsError           bool `json:"isError"`
					StructuredContent struct {
						Error assistant.CallError `json:"error"`
					} `json:"structuredContent"`
				} `json:"result"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("%+v %s via MCP: decoding %s: %v", cfg, name, rr.Body, err)
			}
			if !resp.Result.IsError || resp.Result.StructuredContent.Error.Code != assistant.CodeReadOnly {
				t.Errorf("mutations=%v readOnly=%v: MCP %s = %s, want a %s error", cfg.WebMutations, cfg.ReadOnly, name, rr.Body, assistant.CodeReadOnly)
			}

			rr = serve(srv, c.apiMethod, c.apiPath, "Bearer "+tok, c.apiBody)
			if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), `"code":"`+assistant.CodeReadOnly+`"`) {
				t.Errorf("mutations=%v readOnly=%v: API %s %s = %d %s, want 403 %s", cfg.WebMutations, cfg.ReadOnly, c.apiMethod, c.apiPath, rr.Code, rr.Body, assistant.CodeReadOnly)
			}
		}
		if fx.calls != 0 {
			t.Errorf("mutations=%v readOnly=%v: a refused write tool ran %d agent-deck command(s)", cfg.WebMutations, cfg.ReadOnly, fx.calls)
		}
		// Read tools keep working.
		if rr := serve(srv, http.MethodGet, "/api/assistant/v1/sessions", "Bearer "+tok, ""); rr.Code != http.StatusOK {
			t.Errorf("mutations=%v readOnly=%v: read tool refused: %d %s", cfg.WebMutations, cfg.ReadOnly, rr.Code, rr.Body)
		}
	}
}

// The control: with mutations enabled the same token may write.
func TestAssistantRoutes_MutationsEnabledAllowsWriteTool(t *testing.T) {
	srv, tok, fx := newWriteScopedAssistant(t, Config{WebMutations: true})
	rr := serve(srv, http.MethodPost, "/api/assistant/v1/sessions/s1/send", "Bearer "+tok, `{"message":"hi"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("send with mutations enabled = %d %s, want 200", rr.Code, rr.Body)
	}
	if fx.calls == 0 {
		t.Fatal("send with mutations enabled ran no command")
	}
}
