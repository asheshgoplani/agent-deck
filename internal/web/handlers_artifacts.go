package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/artifact"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The Fleet Console turns the conductor HTML artifacts (which used to scp to the
// Mac and pile up as Chrome tabs) into inline, in-app cards, and lets a
// highlight-to-comment on a card auto-route the annotation to the artifact's
// owning session. Three endpoints, mirroring the Command Center triad:
//   - GET  /api/artifacts          — provenance-attributed listing
//   - GET  /api/artifacts/serve    — read-only, path-confined, relay-injected
//   - POST /api/artifacts/comment  — resolve owner → deliver via the session layer
//
// Security: serving is read-only and path-confined to the conductor root; the
// rendered artifact runs in an opaque-origin sandbox iframe (allow-scripts only,
// NO allow-same-origin) so its scripts — including the injected selection relay
// — cannot reach this origin, the parent DOM, or any credential. The comment
// POST is a mutation behind the existing CSRF + auth + mutation-rate gates.

// artifactCandidate is one option offered when an artifact's owner can't be
// resolved automatically (so the operator is never left at a dead end).
type artifactCandidate struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Group     string `json:"group"`
}

// artifactTarget is the resolved routing decision for an annotation.
type artifactTarget struct {
	Kind       string // "session" | "conductor" | "picker"
	SessionID  string // concrete delivery target (empty for picker)
	Label      string
	Busy       bool
	Candidates []artifactCandidate
}

// isBusyStatus reports whether a session is actively working, in which case a
// raw send-keys would be swallowed by the live pane and the comment must take
// the durable-inbox path instead.
func isBusyStatus(st session.Status) bool {
	return st == session.StatusRunning || st == session.StatusStarting
}

// resolveArtifactTarget implements the SPEC resolution ladder:
// sidecar session (if live) → owning conductor → picker (never a dead end).
func resolveArtifactTarget(meta artifact.Meta, conductor string, sessions []*MenuSession) artifactTarget {
	byID := make(map[string]*MenuSession, len(sessions))
	for _, s := range sessions {
		byID[s.ID] = s
	}

	// 1) Sidecar names a session that is still live → route straight to it.
	if meta.SessionID != "" {
		if s := byID[meta.SessionID]; s != nil {
			return artifactTarget{Kind: "session", SessionID: s.ID, Label: s.Title, Busy: isBusyStatus(s.Status)}
		}
	}

	// 2) Fall back to the conductor that owns the directory.
	condTitle := "conductor-" + conductor
	for _, s := range sessions {
		if s.Title == condTitle || (s.IsConductor && s.GroupPath == conductor) {
			return artifactTarget{Kind: "conductor", SessionID: s.ID, Label: s.Title, Busy: isBusyStatus(s.Status)}
		}
	}

	// 3) Picker — offer the conductor's group first, else the whole fleet, so
	// the operator can always land the comment somewhere.
	var cands []artifactCandidate
	for _, s := range sessions {
		if s.GroupPath == conductor {
			cands = append(cands, toCandidate(s))
		}
	}
	if len(cands) == 0 {
		for _, s := range sessions {
			cands = append(cands, toCandidate(s))
		}
	}
	return artifactTarget{Kind: "picker", Candidates: cands}
}

func toCandidate(s *MenuSession) artifactCandidate {
	return artifactCandidate{SessionID: s.ID, Title: s.Title, Status: string(s.Status), Group: s.GroupPath}
}

// conductorOf returns the first path component of a root-relative artifact path,
// which by the conductor/<name>/<file>.html layout is the owning conductor.
func conductorOf(rel string) string {
	parts := strings.SplitN(strings.TrimPrefix(filepath.ToSlash(rel), "/"), "/", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func (s *Server) liveSessions() []*MenuSession {
	snapshot, err := s.menuData.LoadMenuSnapshot()
	if err != nil || snapshot == nil {
		return nil
	}
	var out []*MenuSession
	for _, it := range snapshot.Items {
		if it.Type == MenuItemTypeSession && it.Session != nil {
			out = append(out, it.Session)
		}
	}
	return out
}

// handleArtifactsList returns the provenance-attributed artifact listing.
func (s *Server) handleArtifactsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	entries, err := artifact.ListArtifacts(s.artifactRoot)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to list artifacts")
		return
	}
	if entries == nil {
		entries = []artifact.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": entries})
}

// handleArtifactServe serves one artifact read-only, path-confined to the
// conductor root, with the selection relay injected. Auth uses the stream
// authorizer so an iframe src (which cannot set an Authorization header) may
// carry a ?token= when a token is configured.
func (s *Server) handleArtifactServe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorizeStreamRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	rel := r.URL.Query().Get("path")
	full, err := artifact.ConfinedPath(s.artifactRoot, rel)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid artifact path")
		return
	}
	if !strings.HasSuffix(strings.ToLower(full), ".html") {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "not an html artifact")
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "artifact not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	// G705 (XSS taint) false positive: serving the conductor HTML artifact verbatim
	// is the feature. The path is confined to artifactRoot (ConfinedPath above) and the
	// artifact renders only inside an opaque-origin sandbox iframe — allow-scripts, NO
	// allow-same-origin — so it cannot reach this origin, the parent DOM, or any
	// credential (see the security note in the file header).
	_, _ = w.Write([]byte(injectSelectionRelay(string(data), rel))) //nolint:gosec // G705: read-only, path-confined HTML served into an opaque-origin sandbox iframe (see header)
}

// artifactCommentRequest is the POST /api/artifacts/comment body.
type artifactCommentRequest struct {
	Path    string `json:"path"`
	Excerpt string `json:"excerpt"`
	Comment string `json:"comment"`
}

// handleArtifactComment resolves an artifact's owning session and routes the
// annotation there. An unresolved owner returns the picker candidates rather
// than failing — the operator chooses, and re-posts (future: with a target).
func (s *Server) handleArtifactComment(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	if !s.checkMutationsAllowed(w) {
		return
	}
	if !s.checkMutationRateLimit(w) {
		return
	}

	var req artifactCommentRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Comment) == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "comment is required")
		return
	}

	full, err := artifact.ConfinedPath(s.artifactRoot, req.Path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid artifact path")
		return
	}
	meta, _, _ := artifact.ReadMeta(full)
	conductor := conductorOf(req.Path)
	target := resolveArtifactTarget(meta, conductor, s.liveSessions())

	if target.Kind == "picker" {
		candidates := target.Candidates
		if candidates == nil {
			candidates = []artifactCandidate{} // marshal as [] not null even on an empty fleet
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"needsPicker": true,
			"conductor":   conductor,
			"candidates":  candidates,
		})
		return
	}

	base := strings.TrimSuffix(filepath.Base(full), ".html")
	c := session.ArtifactComment{
		ArtifactID:    firstNonEmpty(meta.ArtifactID, base),
		ArtifactTitle: firstNonEmpty(meta.Title, base),
		Excerpt:       req.Excerpt,
		Comment:       req.Comment,
		Profile:       s.cfg.Profile,
	}
	if err := s.artifactDeliver(target.SessionID, target.Busy, c); err != nil {
		writeAPIError(w, http.StatusBadGateway, "SEND_FAILED", "failed to deliver comment")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"routedTo": target.SessionID,
		"kind":     target.Kind,
		"label":    target.Label,
		"busy":     target.Busy,
	})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// injectSelectionRelay appends the selection-relay script to the served
// artifact. Cross-iframe text selections do not bubble to the parent, so the
// artifact must postMessage them out itself. The script runs inside the
// opaque-origin sandbox and can only talk to the parent via postMessage.
func injectSelectionRelay(htmlBody, relPath string) string {
	relay := fmt.Sprintf(`<script>
(function(){
  var ARTIFACT_PATH = %q;
  function emit(){
    var sel = window.getSelection && window.getSelection();
    if(!sel || sel.isCollapsed){ return; }
    var text = String(sel).trim();
    if(!text){ return; }
    var rect = null;
    try { rect = sel.getRangeAt(0).getBoundingClientRect(); } catch(e){}
    parent.postMessage({
      type: 'fleet-artifact-selection',
      path: ARTIFACT_PATH,
      text: text,
      rect: rect ? {top:rect.top,left:rect.left,bottom:rect.bottom,right:rect.right,width:rect.width,height:rect.height} : null
    }, '*');
  }
  document.addEventListener('mouseup', function(){ setTimeout(emit, 0); });
})();
</script>`, relPath)

	lower := strings.ToLower(htmlBody)
	if idx := strings.LastIndex(lower, "</body>"); idx >= 0 {
		return htmlBody[:idx] + relay + htmlBody[idx:]
	}
	return htmlBody + relay
}
