package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Phase 1 v1.9 regression coverage for the CLI/JS schema-stability surface
// (S-CLI-1 in USE-CASES-AND-TESTS.md, J1 in TEST-PLAN.md) — case status-001.
//
// Today's TestParity_AllStatusesPreservedThroughGetSessions decodes the
// response with `Status: session.Status` and round-trips back to the typed
// constant. That round-trip would PASS even if a refactor changed the wire
// literal from "running" to "RUNNING" or "Running", because Go's
// json.Unmarshal on a string-aliased type accepts any case-sensitive bytes
// and stuffs them in. The downstream JS client (`SessionRow.js:9-16`,
// `parity-state.spec.js`) and the CLI consumer (`agent-deck list --json |
// jq .sessions[].status`) match against the lowercase literals — a casing
// drift would silently break both surfaces.
//
// This test asserts the EXACT wire bytes for every documented Status. If a
// future refactor switches the marshaler (e.g. introduces MarshalJSON that
// upper-cases) the response body shape changes and this test fails before
// the JS bundle and the CLI script silently start producing wrong colors.

func TestStatusWireFormat_ExactJSONLiteralPerStatus(t *testing.T) {
	t.Parallel()

	// Every Status value the codebase documents at instance.go:47-52, in
	// the same order. If a new value is added, append it here AND add the
	// expected wire literal. The pair is what stabilizes the schema.
	wireByStatus := map[session.Status]string{
		session.StatusRunning:  `"status":"running"`,
		session.StatusWaiting:  `"status":"waiting"`,
		session.StatusIdle:     `"status":"idle"`,
		session.StatusError:    `"status":"error"`,
		session.StatusStarting: `"status":"starting"`,
		session.StatusStopped:  `"status":"stopped"`,
	}

	fx := newParityFixture()
	installLoaderOnServer(fx.server, nil) // no overlay so the seeded value flows through

	// Replace the seeded sessions with one per Status, all on tool="shell"
	// so the hook-overlay branch is a no-op (toolEmitsLifecycleHooks=false)
	// and the seeded Status reaches the wire verbatim.
	fx.store.mu.Lock()
	fx.store.sessions = make(map[string]*MenuSession)
	fx.store.order = nil
	i := 0
	for st := range wireByStatus {
		id := "status-wire-" + strconv.Itoa(i)
		fx.store.sessions[id] = &MenuSession{
			ID: id, Title: string(st), Tool: "shell",
			Status: st, GroupPath: "work", ProjectPath: "/srv/x",
			Order: i, CreatedAt: fx.store.now(),
		}
		fx.store.order = append(fx.store.order, id)
		i++
	}
	fx.store.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	fx.server.handleSessionsCollection(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions: status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()

	// Precompute the response so we can assert each literal appears in body.
	for _, wire := range wireByStatus {
		if !bytes.Contains(body, []byte(wire)) {
			// Pretty-print body for diagnostic; capped so a future regression
			// dumping all sessions doesn't produce an unreadable failure.
			snippet := string(body)
			if len(snippet) > 1024 {
				snippet = snippet[:1024] + "...(truncated)"
			}
			t.Errorf("/api/sessions response missing exact wire literal %q.\n"+
				"This is a CLI/JS schema-stability break (S-CLI-1). The downstream\n"+
				"JS at SessionRow.js:9-16 and CLI consumers depend on the lower-\n"+
				"case literal — a marshaler that upper-cases or aliases the\n"+
				"value will silently mis-color every session row.\nbody: %s",
				wire, snippet)
		}
	}

	// Belt-and-suspenders: decode and assert every Status round-trips, so a
	// regression that produced *correct* literals but dropped a session
	// entirely also fails.
	var resp struct {
		Sessions []*MenuSession `json:"sessions"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	gotByStatus := make(map[session.Status]bool, len(resp.Sessions))
	for _, s := range resp.Sessions {
		gotByStatus[s.Status] = true
	}
	for st := range wireByStatus {
		if !gotByStatus[st] {
			t.Errorf("status %q missing from response after round-trip", st)
		}
	}
}
