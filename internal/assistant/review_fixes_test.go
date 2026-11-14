package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Refused calls (scope missing, unknown tool) spend from the read budget, so
// a token cannot write unlimited audit lines.
func TestRefusedCallsAreRateLimited(t *testing.T) {
	svc, _ := newTestService(t, Limits{ReadPerMinute: 2, WritePerMinute: 1})
	rec, err := svc.Authenticate(issue(t, svc, "bot"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if r := svc.Call(context.Background(), rec, "nope", nil, TransportAPI); r.Err == nil || r.Err.Code != CodeUnknownTool {
			t.Fatalf("call %d: %+v", i, r)
		}
	}
	if r := svc.Call(context.Background(), rec, "nope", nil, TransportAPI); r.Err == nil || r.Err.Code != CodeRateLimited {
		t.Fatalf("3rd refused call: %+v, want rate_limited", r)
	}

	// A token that keeps calling after its budget is spent must not grow
	// the audit log by one line per call.
	for i := 0; i < 500; i++ {
		if r := svc.Call(context.Background(), rec, ToolWhoami, nil, TransportAPI); r.Err == nil || r.Err.Code != CodeRateLimited {
			t.Fatalf("exhausted call %d: %+v, want rate_limited", i, r)
		}
	}
	entries, err := ReadAudit(svc.Store.AuditPath(), "bot", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 3 {
		t.Fatalf("audit has %d lines after 501 rate-limited calls, want at most 3 (2 refusals + 1 rate-limit line)", len(entries))
	}
	// When the window rolls, one line records how many were not logged.
	svc.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if r := svc.Call(context.Background(), rec, ToolWhoami, nil, TransportAPI); r.Err == nil || r.Err.Code != CodeRateLimited {
		t.Fatalf("call in the next window: %+v, want rate_limited", r)
	}
	entries, err = ReadAudit(svc.Store.AuditPath(), "bot", 0)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if len(entries) != 4 || last.Reason != CodeRateLimited || last.Suppressed != 500 {
		t.Fatalf("after the window rolled: %d lines, last %+v; want 4 lines, the last rate_limited with suppressed=500", len(entries), last)
	}
}

// A JSON-RPC batch or a wrong jsonrpc version is an invalid request.
func TestMCPInvalidRequests(t *testing.T) {
	svc, _ := newTestService(t, DefaultLimits)
	tok := issue(t, svc, "bot")
	h := NewMCPHandler(svc, TransportStdio)
	src := func() (string, error) { return tok, nil }
	for _, in := range []string{
		`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
		`{"jsonrpc":"1.0","id":1,"method":"ping"}`,
	} {
		var resp struct {
			Error *rpcError `json:"error"`
		}
		if err := json.Unmarshal(h.Handle(context.Background(), src, []byte(in)), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil || resp.Error.Code != codeInvalidReq {
			t.Fatalf("%s: %+v, want -32600", in, resp.Error)
		}
	}
}

// A token rotated in place (same size, rename) is seen by a running store
// even if the mtime did not move.
func TestStoreSeesReplacedFile(t *testing.T) {
	dir := t.TempDir()
	a, b := NewStore(dir), NewStore(dir)
	old, _, err := a.Create("bot", "mcp", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Authenticate(old); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(a.Path())
	if _, _, err := a.Create("bot", "mcp", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(a.Path(), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Authenticate(old); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("rotated token still accepted: %v", err)
	}
}
