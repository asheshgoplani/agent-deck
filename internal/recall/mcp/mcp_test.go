package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

type fakeHandler struct {
	searches []query.SearchOptions
}

func (f *fakeHandler) Search(_ context.Context, o query.SearchOptions) (query.SearchResult, string, error) {
	f.searches = append(f.searches, o)
	return query.SearchResult{Query: o.Query, Hits: []query.Hit{{SessID: 7, Harness: "codex", NativeID: "abc", Title: "retry budget"}}, Candidates: 1}, "index is behind by 2 source(s)", nil
}

func (f *fakeHandler) Show(_ context.Context, ref string, turns int) (query.Detail, error) {
	if ref == "missing" {
		return query.Detail{}, query.ErrNotFound
	}
	return query.Detail{Session: query.SessionRow{SessID: 7, NativeID: ref, Turns: turns}}, nil
}

func (f *fakeHandler) Context(_ context.Context, ref, tier string, budget int) (query.ContextResult, error) {
	if tier != query.TierCard && tier != query.TierBrief && tier != query.TierExcerpt {
		return query.ContextResult{}, query.ErrTier
	}
	return query.ContextResult{Text: "Recalled conversation #7 (" + tier + ", budget " + itoa(budget) + ")"}, nil
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func serve(t *testing.T, h Handler, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	srv := New(h, "test", strings.NewReader(strings.Join(lines, "\n")+"\n"), &out)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("reply is not JSON: %v\n%s", err, line)
		}
		replies = append(replies, m)
	}
	return replies
}

func TestServe_InitializeToolsListAndCallOverStdio(t *testing.T) {
	h := &fakeHandler{}
	replies := serve(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall_search","arguments":{"query":"retry budget","harness":"codex","limit":5,"hint":{"ticket":"SB-1"},"tag":["auth"]}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"recall_show","arguments":{"session":"abc","tier":"card"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"recall_context","arguments":{"session":"#7","tier":"brief","budget":300}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"recall_show","arguments":{"session":"missing"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"nope"}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"recall_search","arguments":{}}}`,
		`this is not json`,
		`{"jsonrpc":"2.0","id":"s9","method":"ping"}`,
	)
	// The notification got no reply: 10 requests, 10 replies.
	if len(replies) != 10 {
		t.Fatalf("replies = %d\n%+v", len(replies), replies)
	}
	init := replies[0]["result"].(map[string]any)
	if init["protocolVersion"] != ProtocolVersion || init["serverInfo"].(map[string]any)["name"] != ServerName {
		t.Fatalf("initialize: %+v", init)
	}
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("tools/list: %+v", tools)
	}
	names := map[string]bool{}
	for _, tl := range tools {
		tool := tl.(map[string]any)
		names[tool["name"].(string)] = true
		if tool["inputSchema"].(map[string]any)["type"] != "object" {
			t.Fatalf("tool without an object schema: %+v", tool)
		}
	}
	if !names["recall_search"] || !names["recall_show"] || !names["recall_context"] {
		t.Fatalf("tool names: %v", names)
	}
	search := replies[2]["result"].(map[string]any)
	text := search["content"].([]any)[0].(map[string]any)["text"].(string)
	if search["isError"] != nil || !strings.Contains(text, `"native_id": "abc"`) || !strings.Contains(text, "index is behind") {
		t.Fatalf("search result: %+v", search)
	}
	if len(h.searches) != 1 || h.searches[0].Harness != "codex" || h.searches[0].Limit != 5 || h.searches[0].Hints["ticket"] != "SB-1" || len(h.searches[0].Tags) != 1 {
		t.Fatalf("search options: %+v", h.searches)
	}
	show := replies[3]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(show, `"turns": -1`) {
		t.Fatalf("show card tier must ask for no messages: %s", show)
	}
	ctxText := replies[4]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if ctxText != "Recalled conversation #7 (brief, budget 300)" {
		t.Fatalf("context: %s", ctxText)
	}
	// A tool failure is a tool result with isError, not a protocol error.
	missing := replies[5]["result"].(map[string]any)
	if missing["isError"] != true || !strings.Contains(missing["content"].([]any)[0].(map[string]any)["text"].(string), "no such session") {
		t.Fatalf("missing session: %+v", missing)
	}
	if code := replies[6]["error"].(map[string]any)["code"].(float64); code != codeMethodMissing {
		t.Fatalf("unknown method: %+v", replies[6])
	}
	if code := replies[7]["error"].(map[string]any)["code"].(float64); code != codeInvalidParams {
		t.Fatalf("missing query: %+v", replies[7])
	}
	if code := replies[8]["error"].(map[string]any)["code"].(float64); code != codeParse || replies[8]["id"] != nil {
		t.Fatalf("parse error: %+v", replies[8])
	}
	if replies[9]["id"] != "s9" {
		t.Fatalf("ping must echo a string id: %+v", replies[9])
	}
}

func TestServe_BadTierIsInvalidParams(t *testing.T) {
	replies := serve(t, &fakeHandler{}, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall_context","arguments":{"session":"#7","tier":"raw"}}}`)
	if len(replies) != 1 || replies[0]["error"] == nil {
		t.Fatalf("%+v", replies)
	}
	if !errors.Is(query.ErrTier, query.ErrTier) {
		t.Fatal("sanity")
	}
}
