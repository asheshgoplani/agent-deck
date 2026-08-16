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
// Scope is one of "local", "global" or "user". Which files those name depends
// on the session's TOOL, not just its project path: Claude, Codex, Gemini,
// Cursor and OpenCode each keep MCP servers somewhere different, and Codex and
// Gemini have no local scope at all. Requests carry an MCPTarget (tool +
// project path) and route through the same per-tool helpers the TUI uses, so a
// Codex session can never rewrite Claude's config. Scopes a tool does not have
// are refused, not redirected.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// MCPTarget identifies the session whose MCP configuration is being read or
// written. The tool is part of the identity, not decoration: Claude, Codex,
// Gemini, Cursor and OpenCode each keep their MCP servers in a different file,
// so a target without a tool cannot say which store to touch. Passing only a
// project path is what let the web manager write Claude config for a Codex
// session.
type MCPTarget struct {
	Tool        string
	ProjectPath string
}

// MCPManager is the seam between web HTTP handlers and the on-disk MCP
// catalog + scope-specific config files. Tests inject a fake; production
// gets defaultMCPManager which delegates to internal/session.
type MCPManager interface {
	ListCatalog() []MCPCatalogEntry
	ListAttached(target MCPTarget) (map[string][]string, error)
	Attach(target MCPTarget, name, scope string) error
	Detach(target MCPTarget, name, scope string) error
	Move(target MCPTarget, name, fromScope, toScope string) error
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
	target, ok := s.lookupSessionMCPTarget(sessionID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "session not found")
		return
	}
	// Refuse rather than silently writing some other tool's config. The TUI
	// gates its MCP dialog on exactly this predicate (see home.go), and the
	// web surface has to agree or selecting an unsupported session would
	// mutate an unrelated store.
	if !session.ToolSupportsMCPManager(target.Tool) {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest,
			"MCP management is not supported for tool "+strconv.Quote(target.Tool))
		return
	}

	if rawName == "" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
			return
		}
		attached, err := s.mcpMgr.ListAttached(target)
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
		s.handleMCPAttach(w, r, target, name)
	case http.MethodDelete:
		s.handleMCPDetach(w, r, target, name)
	case http.MethodPatch:
		s.handleMCPMove(w, r, target, name)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleMCPAttach(w http.ResponseWriter, r *http.Request, target MCPTarget, name string) {
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
	if err := s.mcpMgr.Attach(target, name, scope); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		return
	}
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, map[string]string{"attached": name, "scope": scope})
}

func (s *Server) handleMCPDetach(w http.ResponseWriter, r *http.Request, target MCPTarget, name string) {
	if !s.checkMutationsAllowed(w) {
		return
	}
	if !s.checkMutationRateLimit(w) {
		return
	}
	scope := s.detectAttachedScope(target, name)
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
	if err := s.mcpMgr.Detach(target, name, scope); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		return
	}
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, map[string]string{"detached": name, "scope": scope})
}

func (s *Server) handleMCPMove(w http.ResponseWriter, r *http.Request, target MCPTarget, name string) {
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
	fromScope := s.detectAttachedScope(target, name)
	if fromScope == "" {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "MCP not attached to this session")
		return
	}
	if fromScope == toScope {
		writeJSON(w, http.StatusOK, map[string]string{"scope": toScope})
		return
	}
	if err := s.mcpMgr.Move(target, name, fromScope, toScope); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		return
	}
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, map[string]string{
		"name": name, "fromScope": fromScope, "toScope": toScope,
	})
}

// lookupSessionMCPTarget resolves a session id to the tool + project path that
// together select an on-disk MCP store. Both halves come from the same menu
// snapshot entry, so they cannot disagree.
func (s *Server) lookupSessionMCPTarget(sessionID string) (MCPTarget, bool) {
	if s.menuData == nil {
		return MCPTarget{}, false
	}
	snap, err := s.menuData.LoadMenuSnapshot()
	if err != nil || snap == nil {
		return MCPTarget{}, false
	}
	for _, item := range snap.Items {
		if item.Type == MenuItemTypeSession && item.Session != nil && item.Session.ID == sessionID {
			return MCPTarget{Tool: item.Session.Tool, ProjectPath: item.Session.ProjectPath}, true
		}
	}
	return MCPTarget{}, false
}

func (s *Server) detectAttachedScope(target MCPTarget, name string) string {
	attached, err := s.mcpMgr.ListAttached(target)
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

// scopesForTool lists the MCP scopes a tool actually has, mirroring the TUI
// dialog (internal/ui/mcp_dialog.go), which is the source of truth:
//
//   - Codex and Gemini keep MCPs only in their own user-level config;
//   - Cursor and OpenCode have a project file and a user file;
//   - Claude-compatible tools additionally have ~/.claude.json ("user").
//
// A scope missing here is refused rather than silently redirected to Claude's
// store, which is what the previous project-path-only manager did.
func scopesForTool(tool string) []string {
	switch {
	case session.IsCodexCompatible(tool), tool == "gemini":
		return []string{"global"}
	case tool == "cursor", tool == "opencode":
		return []string{"local", "global"}
	case session.IsClaudeCompatible(tool):
		return []string{"local", "global", "user"}
	default:
		return nil
	}
}

func toolHasScope(tool, scope string) bool {
	return slices.Contains(scopesForTool(tool), scope)
}

// attachedNamesForTool reads each scope from the store that tool actually
// uses, and — critically — from the store that scope's write path targets.
//
// The bug this replaces: "local" was read from GetProjectMCPNames, which is
// the Claude config's projects[path].mcpServers map, while "local" was written
// to <project>/.mcp.json. Those are different files (MCPInfo keeps them as
// Project vs LocalMCPs), so attaching one server rewrote .mcp.json from a list
// that never contained the servers already in it, silently dropping them.
// Claude's projects[path] entries belong to the GLOBAL bucket here, exactly as
// the TUI groups them.
func attachedNamesForTool(target MCPTarget) (local, global, user []string) {
	switch {
	case session.IsCodexCompatible(target.Tool):
		return nil, mcpInfoGlobal(session.GetCodexMCPInfo("")), nil
	case target.Tool == "gemini":
		return nil, mcpInfoGlobal(session.GetGeminiMCPInfo(target.ProjectPath)), nil
	case target.Tool == "cursor":
		info := session.GetCursorMCPInfo(target.ProjectPath)
		return mcpInfoLocal(info), mcpInfoGlobal(info), nil
	case target.Tool == "opencode":
		info := session.GetOpenCodeMCPInfo(target.ProjectPath)
		return mcpInfoLocal(info), mcpInfoGlobal(info), nil
	case session.IsClaudeCompatible(target.Tool):
		info := session.GetMCPInfo(target.ProjectPath)
		// Global mirrors the TUI: the config's own mcpServers plus its
		// per-project entries.
		globals := append([]string(nil), session.GetGlobalMCPNames()...)
		globals = append(globals, session.GetProjectMCPNames(target.ProjectPath)...)
		return mcpInfoLocal(info), globals, session.GetUserMCPNames()
	default:
		return nil, nil, nil
	}
}

func mcpInfoLocal(info *session.MCPInfo) []string {
	if info == nil {
		return nil
	}
	return info.Local()
}

func mcpInfoGlobal(info *session.MCPInfo) []string {
	if info == nil {
		return nil
	}
	return info.Global
}

// ListAttached reports the catalog-defined MCPs attached to the target, per
// scope, reading the same store each scope's write path targets.
func (defaultMCPManager) ListAttached(target MCPTarget) (map[string][]string, error) {
	local, global, user := attachedNamesForTool(target)
	return map[string][]string{
		"local":  filterDefined(local),
		"global": filterDefined(global),
		"user":   filterDefined(user),
	}, nil
}

func (m defaultMCPManager) Attach(target MCPTarget, name, scope string) error {
	names, err := m.namesAt(target, scope)
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return m.writeScope(target, scope, append(names, name))
}

func (m defaultMCPManager) Detach(target MCPTarget, name, scope string) error {
	names, err := m.namesAt(target, scope)
	if err != nil {
		return err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return m.writeScope(target, scope, out)
}

func (m defaultMCPManager) Move(target MCPTarget, name, fromScope, toScope string) error {
	if err := m.Detach(target, name, fromScope); err != nil {
		return err
	}
	return m.Attach(target, name, toScope)
}

// namesAt returns the current contents of one scope's store. It must stay the
// exact inverse of writeScope: every read/write pair has to name the same file,
// or attach becomes a partial overwrite.
func (defaultMCPManager) namesAt(target MCPTarget, scope string) ([]string, error) {
	if err := checkScope(target.Tool, scope); err != nil {
		return nil, err
	}
	local, global, user := attachedNamesForTool(target)
	switch scope {
	case "local":
		return filterDefined(local), nil
	case "global":
		return filterDefined(global), nil
	default:
		return filterDefined(user), nil
	}
}

func (defaultMCPManager) writeScope(target MCPTarget, scope string, names []string) error {
	if err := checkScope(target.Tool, scope); err != nil {
		return err
	}
	switch scope {
	case "local":
		return session.WriteLocalMCPConfigForTool(target.Tool, target.ProjectPath, names)
	case "global":
		return session.WriteGlobalMCPConfigForTool(target.Tool, names)
	default:
		// ~/.claude.json is Claude's own user-level store; checkScope has
		// already established the tool is Claude-compatible.
		return session.WriteUserMCP(names)
	}
}

func checkScope(tool, scope string) error {
	switch scope {
	case "local", "global", "user":
	default:
		return errInvalidScope(scope)
	}
	if !toolHasScope(tool, scope) {
		return errUnsupportedScope{scope: scope, tool: tool}
	}
	return nil
}

// errUnsupportedScope explains a scope that exists for some tools but not this
// one, so the API can say why instead of failing opaquely.
type errUnsupportedScope struct {
	scope string
	tool  string
}

func (e errUnsupportedScope) Error() string {
	return fmt.Sprintf("scope %q is not supported for tool %q", e.scope, e.tool)
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
