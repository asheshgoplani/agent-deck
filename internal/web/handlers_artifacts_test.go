package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/artifact"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// recordingDeliverer captures DeliverArtifactComment calls for assertions.
type recordingDeliverer struct {
	called  bool
	target  string
	busy    bool
	comment session.ArtifactComment
}

func (d *recordingDeliverer) deliver(target string, busy bool, c session.ArtifactComment) error {
	d.called = true
	d.target = target
	d.busy = busy
	d.comment = c
	return nil
}

// artifactTestSessions is a small live fleet: one busy worker (sidecar target)
// and the conductor that owns the agent-deck group (fallback target).
func artifactTestSessions() *MenuSnapshot {
	return &MenuSnapshot{
		Profile: "personal",
		Items: []MenuItem{
			{Type: MenuItemTypeSession, Session: &MenuSession{
				ID: "sess-123", Title: "ard-import-perf-fix", Status: session.StatusRunning,
				GroupPath: "agent-deck",
			}},
			{Type: MenuItemTypeSession, Session: &MenuSession{
				ID: "cond-ad", Title: "conductor-agent-deck", Status: session.StatusWaiting,
				GroupPath: "agent-deck", IsConductor: true,
			}},
		},
	}
}

func sessionsFromSnapshot(s *MenuSnapshot) []*MenuSession {
	var out []*MenuSession
	for _, it := range s.Items {
		if it.Type == MenuItemTypeSession && it.Session != nil {
			out = append(out, it.Session)
		}
	}
	return out
}

// SPEC T3: artifact->session resolution: sidecar present -> session; absent ->
// conductor; unknown -> picker (never dead-end).
func TestResolveArtifactTargetSidecarToSession(t *testing.T) {
	sessions := sessionsFromSnapshot(artifactTestSessions())
	meta := artifact.Meta{SessionID: "sess-123", Group: "agent-deck"}

	got := resolveArtifactTarget(meta, "agent-deck", sessions)
	if got.Kind != "session" {
		t.Fatalf("expected kind session, got %q", got.Kind)
	}
	if got.SessionID != "sess-123" {
		t.Fatalf("expected target sess-123, got %q", got.SessionID)
	}
	if !got.Busy {
		t.Fatalf("expected busy=true for a running target")
	}
}

func TestResolveArtifactTargetFallsBackToConductor(t *testing.T) {
	sessions := sessionsFromSnapshot(artifactTestSessions())
	// No sidecar session id -> route to the conductor that owns the directory.
	got := resolveArtifactTarget(artifact.Meta{}, "agent-deck", sessions)
	if got.Kind != "conductor" {
		t.Fatalf("expected kind conductor, got %q", got.Kind)
	}
	if got.SessionID != "cond-ad" {
		t.Fatalf("expected conductor session cond-ad, got %q", got.SessionID)
	}
	if got.Busy {
		t.Fatalf("expected busy=false for a waiting conductor")
	}
}

func TestResolveArtifactTargetUnknownYieldsPickerNeverDeadEnd(t *testing.T) {
	sessions := sessionsFromSnapshot(artifactTestSessions())
	// Sidecar session is gone AND no conductor owns "ghost" -> picker, but with
	// candidates so the operator is never stuck.
	got := resolveArtifactTarget(artifact.Meta{SessionID: "vanished"}, "ghost", sessions)
	if got.Kind != "picker" {
		t.Fatalf("expected kind picker, got %q", got.Kind)
	}
	if got.SessionID != "" {
		t.Fatalf("expected no concrete target for picker, got %q", got.SessionID)
	}
	if len(got.Candidates) == 0 {
		t.Fatalf("picker must never be a dead end: expected candidates, got none")
	}
}

// --- HTTP layer ------------------------------------------------------------

func newArtifactServer(t *testing.T, cfg Config) (*Server, string) {
	t.Helper()
	srv := NewServer(cfg)
	srv.menuData = &fakeMenuDataLoader{snapshot: artifactTestSessions()}

	root := t.TempDir()
	html := filepath.Join(root, "agent-deck", "perf.html")
	if err := os.MkdirAll(filepath.Dir(html), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "<!doctype html><html><body><h1>PERF REPORT BODY</h1></body></html>"
	if err := os.WriteFile(html, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := artifact.WriteMeta(html, artifact.Meta{
		ArtifactID: "perf-report", SessionID: "sess-123", Group: "agent-deck",
		Profile: "personal", Title: "ARD Import Perf",
	}); err != nil {
		t.Fatal(err)
	}
	srv.artifactRoot = root
	return srv, root
}

func TestListArtifactsEndpoint(t *testing.T) {
	srv, _ := newArtifactServer(t, Config{ListenAddr: "127.0.0.1:0"})

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Artifacts []artifact.Entry `json:"artifacts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid list json: %v (%s)", err, rr.Body.String())
	}
	if len(resp.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d: %+v", len(resp.Artifacts), resp.Artifacts)
	}
	if resp.Artifacts[0].SessionID != "sess-123" || !resp.Artifacts[0].HasSidecar {
		t.Fatalf("unexpected entry: %+v", resp.Artifacts[0])
	}
}

func TestServeArtifactInjectsRelayAndConfinesPath(t *testing.T) {
	srv, _ := newArtifactServer(t, Config{ListenAddr: "127.0.0.1:0"})

	// Happy path: serves the artifact body AND injects the selection relay.
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/serve?path=agent-deck/perf.html", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected text/html, got %q", ct)
	}
	out := rr.Body.String()
	if !strings.Contains(out, "PERF REPORT BODY") {
		t.Fatalf("served body missing original artifact content: %s", out)
	}
	// The injected relay is what makes cross-iframe selections reach the parent.
	if !strings.Contains(out, "fleet-artifact-selection") {
		t.Fatalf("served artifact missing injected selection relay: %s", out)
	}

	// Path traversal is rejected.
	bad := httptest.NewRequest(http.MethodGet, "/api/artifacts/serve?path=../../etc/passwd", nil)
	br := httptest.NewRecorder()
	srv.Handler().ServeHTTP(br, bad)
	if br.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for traversal, got %d: %s", br.Code, br.Body.String())
	}
}

func TestServeArtifactRequiresAuthWhenTokenSet(t *testing.T) {
	srv, _ := newArtifactServer(t, Config{ListenAddr: "127.0.0.1:0", Token: "secret"})
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/serve?path=agent-deck/perf.html", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// SPEC T4 (HTTP layer): a comment to a BUSY session is delivered with busy=true
// (the handler hands off to the durable-inbox path) and the resolved target is
// reported back to the caller.
func TestCommentEndpointRoutesToResolvedBusyTarget(t *testing.T) {
	srv, _ := newArtifactServer(t, Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	rec := &recordingDeliverer{}
	srv.artifactDeliver = rec.deliver

	body := `{"path":"agent-deck/perf.html","excerpt":"~14,000 queries","comment":"batch into IN(...)"}`
	req := httptest.NewRequest(http.MethodPost, "/api/artifacts/comment", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host) // same-origin -> CSRF gate passes
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		RoutedTo string `json:"routedTo"`
		Kind     string `json:"kind"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid response json: %v (%s)", err, rr.Body.String())
	}
	if resp.RoutedTo != "sess-123" || resp.Kind != "session" {
		t.Fatalf("expected routedTo=sess-123 kind=session, got %+v", resp)
	}
	if !rec.called {
		t.Fatal("expected deliverer to be called")
	}
	if !rec.busy {
		t.Fatal("expected busy=true for a running target (durable-inbox path)")
	}
	if rec.target != "sess-123" {
		t.Fatalf("expected delivery target sess-123, got %q", rec.target)
	}
	// Verbatim payload (title from sidecar + excerpt + comment) reaches delivery.
	if rec.comment.Excerpt != "~14,000 queries" || rec.comment.Comment != "batch into IN(...)" {
		t.Fatalf("payload not carried verbatim: %+v", rec.comment)
	}
	if rec.comment.ArtifactTitle != "ARD Import Perf" {
		t.Fatalf("expected sidecar title, got %q", rec.comment.ArtifactTitle)
	}
}

func TestCommentEndpointForbiddenWhenMutationsDisabled(t *testing.T) {
	srv, _ := newArtifactServer(t, Config{ListenAddr: "127.0.0.1:0", WebMutations: false})
	rec := &recordingDeliverer{}
	srv.artifactDeliver = rec.deliver

	body := `{"path":"agent-deck/perf.html","excerpt":"x","comment":"y"}`
	req := httptest.NewRequest(http.MethodPost, "/api/artifacts/comment", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
	if rec.called {
		t.Fatal("deliverer must not be called when mutations are disabled")
	}
}

// Never a dead end: a comment whose owner can't be resolved returns the picker
// candidates instead of delivering or failing.
func TestCommentEndpointUnresolvedReturnsPicker(t *testing.T) {
	srv, root := newArtifactServer(t, Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	// A legacy artifact under a conductor no live session owns.
	ghost := filepath.Join(root, "ghost", "legacy.html")
	if err := os.MkdirAll(filepath.Dir(ghost), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ghost, []byte("<h1>legacy</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &recordingDeliverer{}
	srv.artifactDeliver = rec.deliver

	body := `{"path":"ghost/legacy.html","excerpt":"x","comment":"y"}`
	req := httptest.NewRequest(http.MethodPost, "/api/artifacts/comment", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		NeedsPicker bool             `json:"needsPicker"`
		Candidates  []map[string]any `json:"candidates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !resp.NeedsPicker || len(resp.Candidates) == 0 {
		t.Fatalf("expected picker with candidates (never dead end), got %+v", resp)
	}
	if rec.called {
		t.Fatal("must not deliver when target is unresolved; await picker choice")
	}
}
