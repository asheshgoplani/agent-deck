package assistant

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// HTTP paths served by HTTPHandler.
const (
	MCPPath       = "/mcp"
	APIPathPrefix = "/api/assistant/v1/"
)

// IsAssistantPath reports whether path is served by HTTPHandler: the only
// routes that accept an assistant token, and only as a bearer header.
func IsAssistantPath(path string) bool {
	return path == MCPPath || path == strings.TrimSuffix(APIPathPrefix, "/") || strings.HasPrefix(path, APIPathPrefix)
}

// maxBodyBytes bounds a request body.
const maxBodyBytes = 1 << 20

// HTTPHandler serves MCP over HTTP (POST /mcp, JSON response mode) and the
// JSON API under /api/assistant/v1/. Every request needs an assistant
// bearer token, whatever the surrounding server's own auth is; cookies and
// query-string tokens are never accepted.
type HTTPHandler struct {
	svc *Service
	mcp *MCPHandler
}

// NewHTTPHandler returns the handler for svc.
func NewHTTPHandler(svc *Service) *HTTPHandler {
	return &HTTPHandler{svc: svc, mcp: NewMCPHandler(svc, TransportHTTP)}
}

// BearerToken extracts a bearer token from an Authorization header. A bare
// assistant token (no "Bearer " prefix) is accepted too: xAI's remote MCP
// tool puts its "authorization" value in the header and does not document a
// prefix.
func BearerToken(h string) string {
	h = strings.TrimSpace(h)
	if strings.HasPrefix(h, TokenPrefix) && !strings.ContainsAny(h, " \t") {
		return h
	}
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, e *CallError) {
	writeJSON(w, status, map[string]any{"ok": false, "error": e})
}

func statusFor(code string) int {
	switch code {
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeScopeMissing, CodeReadOnly:
		return http.StatusForbidden
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeInvalid:
		return http.StatusBadRequest
	case CodeUnknownTool:
		return http.StatusNotFound
	}
	return http.StatusBadGateway
}

// originAllowed rejects a browser request from another origin (DNS
// rebinding / drive-by pages). Server-side MCP clients send no Origin.
func originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || origin == "null" {
		return origin == ""
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// ServeHTTP implements http.Handler.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r) {
		writeErr(w, http.StatusForbidden, &CallError{Code: "cross_origin", Message: "cross-origin request blocked"})
		return
	}
	if r.URL.Path == MCPPath {
		h.serveMCP(w, r)
		return
	}
	h.serveAPI(w, r)
}

func (h *HTTPHandler) auth(w http.ResponseWriter, r *http.Request) (Record, bool) {
	tok := BearerToken(r.Header.Get("Authorization"))
	if tok == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agent-deck assistant"`)
		writeErr(w, http.StatusUnauthorized, &CallError{Code: CodeUnauthorized, Message: "assistant token required (Authorization: Bearer adk_...)"})
		return Record{}, false
	}
	rec, err := h.svc.Authenticate(tok)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agent-deck assistant", error="invalid_token"`)
		msg := "unknown or revoked assistant token"
		if !errors.Is(err, ErrUnknownToken) {
			msg = "assistant token store unavailable"
		}
		writeErr(w, http.StatusUnauthorized, &CallError{Code: CodeUnauthorized, Message: msg})
		return Record{}, false
	}
	return rec, true
}

func (h *HTTPHandler) serveMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// No server-initiated stream: JSON response mode only.
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, &CallError{Code: "method_not_allowed", Message: "POST JSON-RPC messages to /mcp"})
		return
	}
	tok := BearerToken(r.Header.Get("Authorization"))
	if _, ok := h.auth(w, r); !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, &CallError{Code: CodeInvalid, Message: "request body too large or unreadable"})
		return
	}
	resp := h.mcp.Handle(r.Context(), func() (string, error) { return tok, nil }, body)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}

// serveAPI maps the JSON API onto tool calls:
//
//	GET  whoami                      -> whoami
//	GET  sessions[?group=&status=]   -> list_sessions
//	POST sessions {path,title,tool,message} -> launch_session
//	GET  sessions/{id}               -> session_status
//	GET  sessions/{id}/output        -> read_output
//	POST sessions/{id}/send {message}       -> send_message
//	POST sessions/{id}/approve {choice}     -> approve
func (h *HTTPHandler) serveAPI(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.auth(w, r)
	if !ok {
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.EscapedPath(), strings.TrimSuffix(APIPathPrefix, "/")), "/")
	parts := strings.Split(rest, "/")
	for i, p := range parts {
		if u, err := url.PathUnescape(p); err == nil {
			parts[i] = u
		}
	}

	var tool string
	args := map[string]any{}
	method := r.Method
	switch {
	case len(parts) == 1 && parts[0] == "whoami" && method == http.MethodGet:
		tool = ToolWhoami
	case len(parts) == 1 && parts[0] == "sessions" && method == http.MethodGet:
		tool = ToolListSessions
		if g := r.URL.Query().Get("group"); g != "" {
			args["group"] = g
		}
		if s := r.URL.Query().Get("status"); s != "" {
			args["status"] = s
		}
	case len(parts) == 1 && parts[0] == "sessions" && method == http.MethodPost:
		tool = ToolLaunchSession
		if !decodeBody(w, r, args, "path", "title", "tool", "message") {
			return
		}
	case len(parts) == 2 && parts[0] == "sessions" && method == http.MethodGet:
		tool = ToolSessionStatus
		args["session"] = parts[1]
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "output" && method == http.MethodGet:
		tool = ToolReadOutput
		args["session"] = parts[1]
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "send" && method == http.MethodPost:
		tool = ToolSendMessage
		if !decodeBody(w, r, args, "message") {
			return
		}
		args["session"] = parts[1]
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "approve" && method == http.MethodPost:
		tool = ToolApprove
		if !decodeBody(w, r, args, "choice") {
			return
		}
		args["session"] = parts[1]
	default:
		writeErr(w, http.StatusNotFound, &CallError{Code: "not_found", Message: "no such assistant API route: " + method + " " + r.URL.Path})
		return
	}

	raw, _ := json.Marshal(args)
	res := h.svc.Call(r.Context(), rec, tool, raw, TransportAPI)
	if res.Err != nil {
		writeErr(w, statusFor(res.Err.Code), res.Err)
		return
	}
	if res.Data != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res.Data})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"text": res.Text}})
}

// decodeBody reads a JSON object body into args, keeping only allowed keys.
// An empty body is an empty object.
func decodeBody(w http.ResponseWriter, r *http.Request, args map[string]any, allowed ...string) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, &CallError{Code: CodeInvalid, Message: "request body too large or unreadable"})
		return false
	}
	if strings.TrimSpace(string(body)) == "" {
		return true
	}
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, &CallError{Code: CodeInvalid, Message: "body must be a JSON object"})
		return false
	}
	for _, k := range allowed {
		if v, ok := in[k]; ok {
			args[k] = v
		}
	}
	return true
}
