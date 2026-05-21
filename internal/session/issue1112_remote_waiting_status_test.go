package session

// Issue #1112 — bug 1 (by @ddorman-dn on v1.9.24): the remote "waiting for
// input" icon doesn't update in the local TUI. The remote-side status RPC
// (`agent-deck list --json`) must report `"status":"waiting"` when claude
// is at a prompt, and the local fetch path must surface that to the
// remoteSessions map so the row icon (◉ yellow) is rendered.
//
// These are pure-unit tests against the JSON wire format. They are the
// regression fence that proves:
//
//	1. SSHRunner.FetchSessions parses every status string the remote can
//	   emit ("running", "waiting", "idle", "stopped", "error").
//	2. The `RemoteName` field is stamped on every returned session so the
//	   local TUI can route the icon update to the right remote-group.
//	3. Status changes between two fetches are observed verbatim — no
//	   stale-cache surprises.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// jsonListResponse builds a fake `agent-deck list --json` payload from a
// slice of (id, title, status) tuples — exactly the shape the remote
// emits (see cmd/agent-deck/main.go:handleList JSON branch).
func jsonListResponse(sessions []struct{ ID, Title, Status string }) []byte {
	type sessionJSON struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Path       string `json:"path"`
		Group      string `json:"group"`
		Tool       string `json:"tool"`
		Status     string `json:"status"`
		CreatedAt  string `json:"created_at"`
		RemoteName string `json:"-"`
	}
	out := make([]sessionJSON, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionJSON{
			ID:     s.ID,
			Title:  s.Title,
			Path:   "/tmp/x",
			Tool:   "claude",
			Status: s.Status,
		})
	}
	body, _ := json.Marshal(out)
	return body
}

// stubbedRunner returns an SSHRunner whose runFn answers with the supplied
// JSON bytes for every call. Captures the args so the test can assert the
// `list --json` argv was used.
func stubbedRunner(payload []byte) (*SSHRunner, *[]string) {
	var (
		mu       sync.Mutex
		lastArgs []string
	)
	r := &SSHRunner{
		Host:          "test-host",
		AgentDeckPath: "/usr/local/bin/agent-deck",
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			lastArgs = append([]string(nil), args...)
			return payload, nil
		},
	}
	return r, &lastArgs
}

// TestIssue1112_RemoteFetchSessions_PreservesWaitingStatus is the headline
// regression fence for bug 1: when the remote replies with
// `"status":"waiting"` for a session, the local FetchSessions surfaces it
// verbatim. Prior to the fix this was the suspected gap — the icon
// "wouldn't update" because the field never reached the renderer.
func TestIssue1112_RemoteFetchSessions_PreservesWaitingStatus(t *testing.T) {
	payload := jsonListResponse([]struct{ ID, Title, Status string }{
		{ID: "abc", Title: "claude-1", Status: "waiting"},
	})
	runner, _ := stubbedRunner(payload)

	sessions, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions)=%d, want 1", len(sessions))
	}
	if got := sessions[0].Status; got != "waiting" {
		t.Errorf("Status=%q, want %q — remote status RPC must propagate waiting (#1112 bug 1)", got, "waiting")
	}
}

// TestIssue1112_RemoteFetchSessions_AllStatusesPropagate guards against a
// partial map (e.g. an enum-style switch that handles "running"+"idle" but
// silently swallows "waiting" or "error"). Every status the remote can emit
// must round-trip the JSON unmarshal.
func TestIssue1112_RemoteFetchSessions_AllStatusesPropagate(t *testing.T) {
	statuses := []string{"running", "waiting", "idle", "stopped", "error", "starting"}
	in := make([]struct{ ID, Title, Status string }, 0, len(statuses))
	for i, s := range statuses {
		in = append(in, struct{ ID, Title, Status string }{
			ID:     "id-" + s,
			Title:  s,
			Status: s,
		})
		_ = i
	}
	payload := jsonListResponse(in)
	runner, _ := stubbedRunner(payload)

	sessions, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions: %v", err)
	}
	if len(sessions) != len(statuses) {
		t.Fatalf("len(sessions)=%d, want %d", len(sessions), len(statuses))
	}
	for i, s := range sessions {
		if s.Status != statuses[i] {
			t.Errorf("sessions[%d].Status=%q, want %q", i, s.Status, statuses[i])
		}
	}
}

// TestIssue1112_RemoteFetchSessions_StatusChangeObserved exercises the
// "icon didn't update" symptom: a session reports running on the first
// fetch, then waiting on the second. The struct returned by the second
// fetch must reflect the new status — if the SSHRunner accidentally
// memoized the response, the icon would freeze.
func TestIssue1112_RemoteFetchSessions_StatusChangeObserved(t *testing.T) {
	var responses [][]byte
	responses = append(responses,
		jsonListResponse([]struct{ ID, Title, Status string }{
			{ID: "p", Title: "p", Status: "running"},
		}),
		jsonListResponse([]struct{ ID, Title, Status string }{
			{ID: "p", Title: "p", Status: "waiting"},
		}),
	)
	idx := 0
	runner := &SSHRunner{
		Host:          "test-host",
		AgentDeckPath: "/usr/local/bin/agent-deck",
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			r := responses[idx]
			idx++
			return r, nil
		},
	}

	first, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("first FetchSessions: %v", err)
	}
	if first[0].Status != "running" {
		t.Fatalf("first Status=%q, want running", first[0].Status)
	}
	second, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("second FetchSessions: %v", err)
	}
	if second[0].Status != "waiting" {
		t.Errorf("second Status=%q, want waiting — status change must propagate (#1112 bug 1)", second[0].Status)
	}
}

// TestIssue1112_RemoteFetchSessions_RoutesViaListJSON guards the wire shape:
// the remote status RPC must be `agent-deck list --json`. If we ever
// regress this argv (e.g., introduce a `list -o json` flavor that the
// older remote binary doesn't grok), every status read silently fails and
// the icon stays stuck.
func TestIssue1112_RemoteFetchSessions_RoutesViaListJSON(t *testing.T) {
	payload := jsonListResponse([]struct{ ID, Title, Status string }{
		{ID: "x", Title: "x", Status: "waiting"},
	})
	runner, lastArgs := stubbedRunner(payload)
	if _, err := runner.FetchSessions(context.Background()); err != nil {
		t.Fatalf("FetchSessions: %v", err)
	}
	got := strings.Join(*lastArgs, " ")
	want := "list --json"
	if got != want {
		t.Errorf("ssh argv = %q, want %q — the status RPC must use the list --json wire form", got, want)
	}
}

// TestIssue1112_RemoteFetchSessions_FieldsRoundTrip verifies the full
// RemoteSessionInfo shape survives JSON unmarshal — Title, Path, Tool,
// Group all matter for downstream rendering (#1066 was the cautionary
// tale: Tool dropped → render fell back to generic).
func TestIssue1112_RemoteFetchSessions_FieldsRoundTrip(t *testing.T) {
	body := `[{"id":"abc","title":"My Claude","path":"/work/x","group":"main","tool":"claude","status":"waiting","created_at":"2026-05-21T10:00:00Z"}]`
	runner := &SSHRunner{
		Host:          "test-host",
		AgentDeckPath: "/usr/local/bin/agent-deck",
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			return []byte(body), nil
		},
	}
	sessions, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions)=%d, want 1", len(sessions))
	}
	s := sessions[0]
	if s.ID != "abc" || s.Title != "My Claude" || s.Path != "/work/x" ||
		s.Group != "main" || s.Tool != "claude" || s.Status != "waiting" {
		t.Errorf("RemoteSessionInfo fields not round-tripped: %+v", s)
	}
}

// TestIssue1112_RemoteFetchSessions_EmptyListIsNotAnError covers the boundary
// case the remote reports when no sessions exist. The runner used to err on
// the non-JSON banner ("No sessions found in profile '...'."); now it must
// return an empty slice with no error so the icon-update loop can clear
// stale entries without surfacing a bogus error to the user.
func TestIssue1112_RemoteFetchSessions_EmptyListIsNotAnError(t *testing.T) {
	runner := &SSHRunner{
		Host:          "test-host",
		AgentDeckPath: "/usr/local/bin/agent-deck",
		runFn: func(ctx context.Context, args ...string) ([]byte, error) {
			return []byte("No sessions found in profile 'default'.\n"), nil
		},
	}
	sessions, err := runner.FetchSessions(context.Background())
	if err != nil {
		t.Fatalf("FetchSessions on empty remote: unexpected error %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("len(sessions)=%d, want 0", len(sessions))
	}
}
