package web

// Web UI MCP management handlers.
//
// Closes the four MISSING rows under "MCP MANAGEMENT" in
// tests/web/PARITY_MATRIX.md (Attach, Detach, List, Toggle pooled ↔ local).
// The TUI source-of-truth implementation is internal/ui/mcp_dialog.go
// (`m` key handler); this mirrors it for the Web UI.
//
// Endpoints:
//
//	GET    /api/mcps                              -> catalog from config.toml
//	GET    /api/sessions/{id}/mcps                -> per-session attached
//	POST   /api/sessions/{id}/mcps/{name}         -> attach (body: {scope?})
//	DELETE /api/sessions/{id}/mcps/{name}         -> detach (body: {scope?})
//	PATCH  /api/sessions/{id}/mcps/{name}         -> move scope (toggle pooled ↔ local)
//
// Scope is one of "local" (default, writes <project>/.mcp.json), "global"
// (writes the profile's Claude config), or "user" (writes ~/.claude.json).

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// MCPManager is the seam between web HTTP handlers and the on-disk MCP
// catalog + scope-specific config files. Tests inject a fake; production
// gets defaultMCPManager which delegates to internal/session.
type MCPManager interface {
	ListCatalog() []MCPCatalogEntry
	ListAttached(projectPath string) (map[string][]string, error)
	Attach(projectPath, name, scope string) error
	Detach(projectPath, name, scope string) error
	Move(projectPath, name, fromScope, toScope string) error
}

// MCPCatalogEntry describes one MCP available in the catalog (config.toml).
type MCPCatalogEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Transport   string `json:"transport,omitempty"`
	Command     string `json:"command,omitempty"`
	URL         string `json:"url,omitempty"`
}

// MCPCatalogResponse is returned by GET /api/mcps.
type MCPCatalogResponse struct {
	MCPs []MCPCatalogEntry `json:"mcps"`
}

// SessionMCPsResponse is returned by GET /api/sessions/{id}/mcps.
type SessionMCPsResponse struct {
	SessionID string   `json:"sessionId"`
	Local     []string `json:"local"`
	Global    []string `json:"global"`
	User      []string `json:"user"`
}

// mcpMutateRequest is the JSON body for POST/DELETE/PATCH endpoints.
// `scope` is the canonical field. `pooled` is accepted on PATCH as a
// shorthand: pooled=true → global, pooled=false → local.
type mcpMutateRequest struct {
	Scope  string `json:"scope,omitempty"`
	Pooled *bool  `json:"pooled,omitempty"`
}

// SetMCPManager wires the MCP manager implementation (production or test).
func (s *Server) SetMCPManager(m MCPManager) { s.mcpMgr = m }

// HasMCPManager reports whether the MCP manager seam is wired.
func (s *Server) HasMCPManager() bool { return s.mcpMgr != nil }

func (s *Server) requireMCPManager(w http.ResponseWriter) bool {
	if s.mcpMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "MCP manager not available")
		return false
	}
	return true
}

// handleMCPsCatalog serves GET /api/mcps.
func (s *Server) handleMCPsCatalog(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.requireMCPManager(w) {
		return
	}
	catalog := s.mcpMgr.ListCatalog()
	if catalog == nil {
		catalog = []MCPCatalogEntry{}
	}
	writeJSON(w, http.StatusOK, MCPCatalogResponse{MCPs: catalog})
}

// handleSessionMCPsRouter is the ServeMux pattern entrypoint (Go 1.22+).
func (s *Server) handleSessionMCPsRouter(w http.ResponseWriter, r *http.Request) {
	s.handleSessionMCPs(w, r, r.PathValue("id"), r.PathValue("name"))
}

func (s *Server) handleSessionMCPs(w http.ResponseWriter, r *http.Request, sessionID, rawName string) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	if !s.requireMCPManager(w) {
		return
	}
	if sessionID == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "session id is required")
		return
	}
	projectPath, ok := s.lookupSessionProjectPath(sessionID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "session not found")
		return
	}

	if rawName == "" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
			return
		}
		attached, err := s.mcpMgr.ListAttached(projectPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, SessionMCPsResponse{
			SessionID: sessionID,
			Local:     sortedScope(attached, "local"),
			Global:    sortedScope(attached, "global"),
			User:      sortedScope(attached, "user"),
		})
		return
	}

	name, err := url.PathUnescape(rawName)
	if err != nil || name == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "MCP name is required")
		return
	}

	switch r.Method {
	case http.MethodPost:
		s.handleMCPAttach(w, r, projectPath, name)
	case http.MethodDelete:
		s.handleMCPDetach(w, r, projectPath, name)
	case http.MethodPatch:
		s.handleMCPMove(w, r, projectPath, name)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleMCPAttach(w http.ResponseWriter, r *http.Request, projectPath, name string) {
	if !s.checkMutationsAllowed(w) {
		return
	}
	if !s.checkMutationRateLimit(w) {
		return
	}
	req, ok := decodeMCPMutateBody(w, r)
	if !ok {
		return
	}
	scope, ok := resolveScope(req, "local")
	if !ok {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid scope (want local|global|user)")
		return
	}
	if err := s.mcpMgr.Attach(projectPath, name, scope); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		return
	}
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, map[string]string{"attached": name, "scope": scope})
}

func (s *Server) handleMCPDetach(w http.ResponseWriter, r *http.Request, projectPath, name string) {
	if !s.checkMutationsAllowed(w) {
		return
	}
	if !s.checkMutationRateLimit(w) {
		return
	}
	scope := s.detectAttachedScope(projectPath, name)
	if scope == "" {
		scope = "local"
	}
	if r.ContentLength > 0 {
		req, ok := decodeMCPMutateBody(w, r)
		if !ok {
			return
		}
		if resolved, ok := resolveScope(req, scope); ok {
			scope = resolved
		} else {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid scope (want local|global|user)")
			return
		}
	}
	if err := s.mcpMgr.Detach(projectPath, name, scope); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		return
	}
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, map[string]string{"detached": name, "scope": scope})
}

func (s *Server) handleMCPMove(w http.ResponseWriter, r *http.Request, projectPath, name string) {
	if !s.checkMutationsAllowed(w) {
		return
	}
	if !s.checkMutationRateLimit(w) {
		return
	}
	req, ok := decodeMCPMutateBody(w, r)
	if !ok {
		return
	}
	if req.Scope == "" && req.Pooled == nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "scope or pooled is required")
		return
	}
	toScope, ok := resolveScope(req, "")
	if !ok {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid scope (want local|global|user)")
		return
	}
	fromScope := s.detectAttachedScope(projectPath, name)
	if fromScope == "" {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "MCP not attached to this session")
		return
	}
	if fromScope == toScope {
		writeJSON(w, http.StatusOK, map[string]string{"scope": toScope})
		return
	}
	if err := s.mcpMgr.Move(projectPath, name, fromScope, toScope); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		return
	}
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, map[string]string{
		"name": name, "fromScope": fromScope, "toScope": toScope,
	})
}

func (s *Server) lookupSessionProjectPath(sessionID string) (string, bool) {
	if s.menuData == nil {
		return "", false
	}
	snap, err := s.menuData.LoadMenuSnapshot()
	if err != nil || snap == nil {
		return "", false
	}
	for _, item := range snap.Items {
		if item.Type == MenuItemTypeSession && item.Session != nil && item.Session.ID == sessionID {
			return item.Session.ProjectPath, true
		}
	}
	return "", false
}

func (s *Server) detectAttachedScope(projectPath, name string) string {
	attached, err := s.mcpMgr.ListAttached(projectPath)
	if err != nil {
		return ""
	}
	for _, scope := range []string{"local", "global", "user"} {
		for _, n := range attached[scope] {
			if n == name {
				return scope
			}
		}
	}
	return ""
}

func decodeMCPMutateBody(w http.ResponseWriter, r *http.Request) (mcpMutateRequest, bool) {
	var req mcpMutateRequest
	if r.ContentLength <= 0 {
		return req, true
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body")
		return req, false
	}
	return req, true
}

func resolveScope(req mcpMutateRequest, defaultScope string) (string, bool) {
	scope := req.Scope
	if scope == "" && req.Pooled != nil {
		if *req.Pooled {
			scope = "global"
		} else {
			scope = "local"
		}
	}
	if scope == "" {
		scope = defaultScope
	}
	switch scope {
	case "local", "global", "user":
		return scope, true
	default:
		return "", false
	}
}

func sortedScope(m map[string][]string, scope string) []string {
	out := append([]string(nil), m[scope]...)
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

// ---------------------------------------------------------------------------
// defaultMCPManager — production wiring against internal/session.
// ---------------------------------------------------------------------------

type defaultMCPManager struct{}

// NewDefaultMCPManager returns the production MCPManager that reads/writes
// real config files via internal/session helpers.
func NewDefaultMCPManager() MCPManager { return defaultMCPManager{} }

func (defaultMCPManager) ListCatalog() []MCPCatalogEntry {
	mcps := session.GetAvailableMCPs()
	out := make([]MCPCatalogEntry, 0, len(mcps))
	for name, def := range mcps {
		out = append(out, MCPCatalogEntry{
			Name:        name,
			Description: def.Description,
			Transport:   def.GetTransport(),
			Command:     def.Command,
			URL:         def.URL,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (defaultMCPManager) ListAttached(projectPath string) (map[string][]string, error) {
	return map[string][]string{
		"local":  filterDefined(session.GetProjectMCPNames(projectPath)),
		"global": filterDefined(session.GetGlobalMCPNames()),
		"user":   filterDefined(session.GetUserMCPNames()),
	}, nil
}

func (m defaultMCPManager) Attach(projectPath, name, scope string) error {
	names, err := m.namesAt(projectPath, scope)
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return m.writeScope(projectPath, scope, append(names, name))
}

func (m defaultMCPManager) Detach(projectPath, name, scope string) error {
	names, err := m.namesAt(projectPath, scope)
	if err != nil {
		return err
	}
	out := names[:0]
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return m.writeScope(projectPath, scope, out)
}

func (m defaultMCPManager) Move(projectPath, name, fromScope, toScope string) error {
	if err := m.Detach(projectPath, name, fromScope); err != nil {
		return err
	}
	return m.Attach(projectPath, name, toScope)
}

func (defaultMCPManager) namesAt(projectPath, scope string) ([]string, error) {
	switch scope {
	case "local":
		return filterDefined(session.GetProjectMCPNames(projectPath)), nil
	case "global":
		return filterDefined(session.GetGlobalMCPNames()), nil
	case "user":
		return filterDefined(session.GetUserMCPNames()), nil
	default:
		return nil, errInvalidScope(scope)
	}
}

func (defaultMCPManager) writeScope(projectPath, scope string, names []string) error {
	switch scope {
	case "local":
		return session.WriteMCPJsonFromConfig(projectPath, names)
	case "global":
		return session.WriteGlobalMCP(names)
	case "user":
		return session.WriteUserMCP(names)
	default:
		return errInvalidScope(scope)
	}
}

// filterDefined keeps only catalog-defined names. Write paths preserve any
// other entries on disk (WriteMCPJsonFromConfig #146).
func filterDefined(names []string) []string {
	catalog := session.GetAvailableMCPs()
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := catalog[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

type errInvalidScope string

func (e errInvalidScope) Error() string { return "invalid MCP scope: " + string(e) }
