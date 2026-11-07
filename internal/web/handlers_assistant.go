package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/assistant"
)

// assistantWiring is what SetAssistantService installs.
type assistantWiring struct {
	svc     *assistant.Service
	http    http.Handler
	command string // agent-deck path written into stdio snippets
}

// SetAssistantService enables the assistant routes: POST /mcp and
// /api/assistant/v1/... (assistant bearer token only, always required),
// the owner endpoints /api/connect/... and the /connect form. command is the
// agent-deck binary path written into generated configs. Without a service
// those routes answer 503.
func (s *Server) SetAssistantService(svc *assistant.Service, command string) {
	if svc == nil {
		s.assistant = nil
		return
	}
	if s.cfg.ReadOnly {
		svc.ReadOnly = true
	}
	s.assistant = &assistantWiring{svc: svc, http: assistant.NewHTTPHandler(svc), command: command}
}

func init() {
	registerFeatureRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc(assistant.MCPPath, s.handleAssistantRoutes)
		mux.HandleFunc(strings.TrimSuffix(assistant.APIPathPrefix, "/"), s.handleAssistantRoutes)
		mux.HandleFunc(assistant.APIPathPrefix, s.handleAssistantRoutes)
		mux.HandleFunc("/api/connect/assistants", s.handleConnectAssistants)
		mux.HandleFunc("/api/connect/tokens", s.handleConnectTokens)
		mux.HandleFunc("/api/connect/test", s.handleConnectTest)
		mux.HandleFunc("/connect", s.handleConnectPage)
	})
}

// handleAssistantRoutes delegates to the assistant handler, which always
// requires an assistant bearer token, whatever s.cfg.Token is.
func (s *Server) handleAssistantRoutes(w http.ResponseWriter, r *http.Request) {
	if s.assistant == nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "assistant connections are not enabled on this server")
		return
	}
	s.assistant.http.ServeHTTP(w, r)
}

func (s *Server) requireAssistantOwner(w http.ResponseWriter, r *http.Request) bool {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return false
	}
	if s.assistant == nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "assistant connections are not enabled on this server")
		return false
	}
	return true
}

// requestBaseURL is the URL this request reached the server on, for the
// HTTP snippets. X-Forwarded-Proto only upgrades the scheme to https.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if requestCameOverHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// GET /api/connect/assistants: the kinds, scopes and existing tokens.
func (s *Server) handleConnectAssistants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireAssistantOwner(w, r) {
		return
	}
	type scopeInfo struct {
		ID          assistant.Scope `json:"id"`
		Description string          `json:"description"`
		Default     bool            `json:"default"`
	}
	scopes := make([]scopeInfo, 0, len(assistant.AllScopes))
	for _, sc := range assistant.AllScopes {
		scopes = append(scopes, scopeInfo{sc, assistant.ScopeDescription[sc], assistant.HasScope(assistant.DefaultScopes, sc)})
	}
	recs, err := s.assistant.svc.Store.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "reading assistant tokens failed")
		return
	}
	type tokenInfo struct {
		Name      string            `json:"name"`
		Assistant string            `json:"assistant"`
		Scopes    []assistant.Scope `json:"scopes"`
		CreatedAt time.Time         `json:"created_at"`
	}
	tokens := make([]tokenInfo, 0, len(recs))
	for _, rec := range recs {
		tokens = append(tokens, tokenInfo{rec.Name, rec.Assistant, rec.Scopes, rec.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"assistants":   assistant.Kinds(),
		"default":      assistant.DefaultKind,
		"scopes":       scopes,
		"tokens":       tokens,
		"url":          requestBaseURL(r),
		"read_only":    s.cfg.ReadOnly,
		"setup_prompt": assistant.SetupPromptURL,
		"write_scopes": s.cfg.WebMutations && !s.cfg.ReadOnly,
	})
}

type connectTokenRequest struct {
	Assistant string   `json:"assistant"`
	Scopes    []string `json:"scopes"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
}

// POST /api/connect/tokens {assistant, scopes, name}: create or rotate a
// token and return the block. Write scopes need web mutations enabled.
func (s *Server) handleConnectTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireAssistantOwner(w, r) {
		return
	}
	var req connectTokenRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid JSON body")
		return
	}
	scopes, err := assistant.NormalizeScopes(req.Scopes)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}
	for _, sc := range scopes {
		if sc != assistant.ScopeRead && (!s.cfg.WebMutations || s.cfg.ReadOnly) {
			writeAPIError(w, http.StatusForbidden, ErrCodeForbidden, "this server is read-only: only the read scope can be granted here")
			return
		}
	}
	if !s.checkMutationRateLimit(w) {
		return
	}
	base := req.URL
	if base == "" || (!strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://")) {
		base = requestBaseURL(r)
	}
	conn, err := assistant.Issue(s.assistant.svc.Store, assistant.ConnectOptions{
		Kind:    req.Assistant,
		Name:    strings.TrimSpace(req.Name),
		Scopes:  scopes,
		Profile: s.cfg.Profile,
		URL:     base,
		Command: s.assistant.command,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, conn)
}

// POST /api/connect/test {token}: run list_sessions with the token.
func (s *Server) handleConnectTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireAssistantOwner(w, r) {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid JSON body")
		return
	}
	rec, err := s.assistant.svc.Authenticate(req.Token)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "unknown or revoked token"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	res := s.assistant.svc.Call(ctx, rec, assistant.ToolListSessions, nil, "web-test")
	if res.Err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": res.Err.Error()})
		return
	}
	count := 0
	if m, ok := res.Data.(map[string]any); ok {
		if c, ok := m["count"].(int); ok {
			count = c
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": rec.Name, "scopes": rec.Scopes, "sessions": count})
}

// GET /connect: the standalone form page. Like the app shell it is served
// without auth; its API calls carry the owner token.
func (s *Server) handleConnectPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	body, err := embeddedStaticFiles.ReadFile("static/connect.html")
	if err != nil {
		http.Error(w, "connect page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
