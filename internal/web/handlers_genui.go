package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/genui"
)

// The generative command center (v-genui-0) read endpoints. These serve the
// hand-authored, server-VALIDATED whole-UI view specs. The client renderer
// draws a spec; switching specs reshapes the whole UI live. NO LLM in v0 — the
// specs are inert DATA proving the safe substrate.
//
// Every spec served here passes genui.ValidateBytes server-side before it
// crosses to the client, so the browser only ever receives a vetted spec —
// the security control is on the server, not the browser.

// genuiViewMeta is the lightweight list entry (id + title) for the view switch.
type genuiViewMeta struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// handleGenuiViews lists the available hand-authored views.
func (s *Server) handleGenuiViews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	views := make([]genuiViewMeta, 0, len(genui.ViewIDs))
	for _, id := range genui.ViewIDs {
		views = append(views, genuiViewMeta{ID: id, Title: genui.ViewTitle[id]})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"views": views})
}

// handleGenuiSpec serves one validated whole-UI view spec by id. The spec is
// re-validated on every request — a default that ever drifted out of schema
// returns an error card rather than an unvalidated payload.
func (s *Server) handleGenuiSpec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/command-center/genui/spec/")
	id = strings.Trim(id, "/")
	raw, ok := genui.SpecJSON(id)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "unknown view")
		return
	}
	// Validate before serving — the browser must only ever see a vetted spec.
	if _, err := genui.ValidateBytes(raw); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INVALID_SPEC", "spec failed validation")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// genuiComposeRequest is the POST body for the generative endpoint (v-genui-1).
type genuiComposeRequest struct {
	Intent string `json:"intent"`
}

// maxIntentLen bounds the free-text intent (the only model input) so the prompt
// stays small and a pasted blob can't blow up the request.
const maxIntentLen = 2000

// handleGenuiCompose is the genui-1 endpoint: it turns a natural-language intent
// into a validated whole-UI spec via the pluggable composer + bounded repair
// loop. The composer is UNTRUSTED — its output passes the SAME genui validator
// (unchanged from genui-0) before it is returned, so the browser only ever
// receives a vetted spec. On a composer/validation failure the loop surfaces a
// clean error WITH the per-attempt trace; it never returns an unvalidated spec.
func (s *Server) handleGenuiCompose(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	// Rate-limit every compose (it drives compute, possibly an LLM session).
	if !s.checkMutationRateLimit(w) {
		return
	}
	// Only gate as a mutation when the active composer has side effects (the
	// real SessionComposer pokes a managed session). The default StubComposer
	// is pure, so a read-only demo can still compose views.
	if s.genuiComposerMutates() {
		if !s.checkMutationsAllowed(w) {
			return
		}
	}

	var req genuiComposeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "intent is required")
		return
	}
	if len(intent) > maxIntentLen {
		writeAPIError(w, http.StatusBadRequest, "INVALID_BODY", "intent too long")
		return
	}

	// Build a compact, secret-free summary of the in-scope fleet data so the
	// composer can choose widgets that actually have something to show.
	summary := ""
	if snap, err := s.loadCommandCenterSnapshot(nil); err == nil {
		summary = summarizeForCompose(snap)
	}

	result, err := genui.ComposeView(r.Context(), s.genuiComposer, intent, genui.DefaultCatalog(), summary, genui.DefaultMaxTries)
	if err != nil {
		// Clean, structured failure. Surface the per-attempt trace so the pane
		// can show WHY (e.g. the validator rejected an unknown widget) without
		// ever rendering the rejected output.
		var ce *genui.ComposeError
		attempts := []map[string]any{}
		if errors.As(err, &ce) {
			for _, a := range ce.Attempts {
				attempts = append(attempts, map[string]any{"errors": a.Errors})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "could not compose a valid view for that intent",
			"code":  "COMPOSE_FAILED",
			"trace": map[string]any{
				"composer": genuiComposerName(s.genuiComposer),
				"tries":    len(attempts),
				"attempts": attempts,
			},
		})
		return
	}

	// Success: return the EXACT validated bytes as the spec, plus a small trace.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"spec": json.RawMessage(result.Raw),
		"trace": map[string]any{
			"composer": result.Composer,
			"tries":    result.Tries,
			"repaired": result.Repaired,
		},
	})
}

// genuiComposerMutates reports whether the active composer has side effects (so
// the compose endpoint should be gated as a mutation). A composer advertises
// this via an optional Mutates() method (the SessionComposer returns true, the
// StubComposer false). An UNKNOWN composer is gated as a mutation by default —
// fail closed, so a custom side-effectful composer can't silently bypass the
// read-only / mutation gate.
func (s *Server) genuiComposerMutates() bool {
	type mutatingComposer interface{ Mutates() bool }
	if m, ok := s.genuiComposer.(mutatingComposer); ok {
		return m.Mutates()
	}
	return true
}

func genuiComposerName(c genui.SpecComposer) string {
	if c == nil {
		return "<nil>"
	}
	return c.Name()
}

// summarizeForCompose renders a one-line, secret-free summary of the snapshot
// for the composer's user message. Only counts + conductor names — the same
// non-secret surface the Command Center already displays.
func summarizeForCompose(snap *CommandCenterSnapshot) string {
	if snap == nil {
		return ""
	}
	names := make([]string, 0, len(snap.Conductors))
	stuck := 0
	for _, c := range snap.Conductors {
		names = append(names, c.Name)
		for _, sess := range c.Sessions {
			if sess.Status == "error" || sess.Status == "stopped" {
				stuck++
			}
		}
	}
	return fmt.Sprintf("%d running, %d waiting, %d idle; %d conductors (%s); %d decisions waiting; %d stuck sessions",
		snap.Totals.Running, snap.Totals.Waiting, snap.Totals.Idle,
		len(snap.Conductors), strings.Join(names, ", "),
		len(snap.DecisionsWaiting), stuck)
}
