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
	// Project is Claude's projects[path].mcpServers map — a distinct store
	// from both Local (<project>/.mcp.json) and Global (root mcpServers).
	Project []string `json:"project"`
	Global  []string `json:"global"`
	User    []string `json:"user"`
	// Scopes lists the scopes this session's tool actually has, most specific
	// first. The client renders and defaults from this instead of assuming
	// every tool has all four.
	Scopes []string `json:"scopes"`
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
			Project:   sortedScope(attached, "project"),
			Global:    sortedScope(attached, "global"),
			User:      sortedScope(attached, "user"),
			Scopes:    scopesForTool(target.Tool),
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
	// Default to the tool's most specific store rather than a hardcoded
	// "local": Codex and Gemini have no local scope, so a bodyless attach
	// would otherwise always be refused for them.
	scope, ok := resolveScope(req, defaultAttachScope(target.Tool))
	if !ok {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid scope (want local|project|global|user)")
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
		// Not found in any scope; fall back to the tool's default rather than
		// a hardcoded "local" that Codex and Gemini do not have.
		scope = defaultAttachScope(target.Tool)
	}
	if r.ContentLength > 0 {
		req, ok := decodeMCPMutateBody(w, r)
		if !ok {
			return
		}
		if resolved, ok := resolveScope(req, scope); ok {
			scope = resolved
		} else {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid scope (want local|project|global|user)")
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
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid scope (want local|project|global|user)")
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
	for _, scope := range scopesForTool(target.Tool) {
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
	case "local", "project", "global", "user":
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
		// "project" is Claude's projects[path].mcpServers map. It is a real,
		// separately-written store, so it gets its own scope instead of being
		// folded into "global": folding it there meant a detach reported
		// success while leaving the server attached at project level, and an
		// attach could copy project-only entries up into the root map.
		return []string{"local", "project", "global", "user"}
	default:
		return nil
	}
}

// defaultAttachScope is the scope the UI should preselect for a tool: the most
// specific store it actually has. Codex and Gemini have no local scope, which
// is why the pane must ask instead of hardcoding "local".
func defaultAttachScope(tool string) string {
	scopes := scopesForTool(tool)
	if len(scopes) == 0 {
		return ""
	}
	return scopes[0]
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
// Claude's projects[path] entries are their OWN scope here ("project"), not part
// of the global bucket. That matters because the global write path rewrites only
// the root mcpServers and cannot remove a projects[path] entry: folding the two
// together would report a server the global write can never detach, and any
// attempt to "correct" that by clearing the project half on a global write would
// delete project-scoped servers the user never touched.
// TestClaudeProjectEntriesAreTheirOwnScope pins the separation.
// scopeNames maps each scope a tool has to the names currently in that scope's
// store. Every entry must be read from the exact file its writeScope
// counterpart targets, or a read-modify-write crosses a store boundary.
func attachedNamesForTool(target MCPTarget) map[string][]string {
	out := map[string][]string{}
	switch {
	case session.IsCodexCompatible(target.Tool):
		out["global"] = mcpInfoGlobal(session.GetCodexMCPInfo(""))
	case target.Tool == "gemini":
		out["global"] = mcpInfoGlobal(session.GetGeminiMCPInfo(target.ProjectPath))
	case target.Tool == "cursor":
		info := session.GetCursorMCPInfo(target.ProjectPath)
		out["local"], out["global"] = mcpInfoLocal(info), mcpInfoGlobal(info)
	case target.Tool == "opencode":
		info := session.GetOpenCodeMCPInfo(target.ProjectPath)
		out["local"], out["global"] = mcpInfoLocal(info), mcpInfoGlobal(info)
	case session.IsClaudeCompatible(target.Tool):
		info := session.GetMCPInfo(target.ProjectPath)
		out["local"] = mcpInfoLocal(info)                               // <project>/.mcp.json
		out["project"] = session.GetProjectMCPNames(target.ProjectPath) // projects[path].mcpServers
		out["global"] = session.GetGlobalMCPNames()                     // root mcpServers
		out["user"] = session.GetUserMCPNames()                         // ~/.claude.json
	}
	return out
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
	out := map[string][]string{}
	for scope, names := range attachedNamesForTool(target) {
		out[scope] = filterDefined(names)
	}
	return out, nil
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

// Move relocates an MCP between two scopes without ever leaving it detached
// from both.
//
// The previous implementation detached first and only discovered an
// unsupported destination when the subsequent attach failed — by which point
// the server had already been removed from disk and the caller got nothing but
// an error. That was reachable straight from the UI, which offered every scope
// for every tool.
//
// Both scopes are validated before anything is written, and if the attach
// still fails the source attachment is restored, so a failed move is a no-op
// rather than a deletion.
func (m defaultMCPManager) Move(target MCPTarget, name, fromScope, toScope string) error {
	if err := checkScope(target.Tool, fromScope); err != nil {
		return err
	}
	if err := checkScope(target.Tool, toScope); err != nil {
		return err
	}
	if fromScope == toScope {
		return nil
	}

	if err := m.Detach(target, name, fromScope); err != nil {
		return err
	}
	if err := m.Attach(target, name, toScope); err != nil {
		// Put it back. If the restore also fails, say both things: the caller
		// needs to know the configuration was left short a server.
		if restoreErr := m.Attach(target, name, fromScope); restoreErr != nil {
			return fmt.Errorf("move %q from %q to %q failed: %w; restoring the original scope ALSO failed: %v — %q is now detached from both scopes",
				name, fromScope, toScope, err, restoreErr, name)
		}
		return fmt.Errorf("move %q from %q to %q failed: %w (original scope restored)", name, fromScope, toScope, err)
	}
	return nil
}

// namesAt returns the current contents of one scope's store. It must stay the
// exact inverse of writeScope: every read/write pair has to name the same file,
// or attach becomes a partial overwrite.
func (defaultMCPManager) namesAt(target MCPTarget, scope string) ([]string, error) {
	if err := checkScope(target.Tool, scope); err != nil {
		return nil, err
	}
	return filterDefined(attachedNamesForTool(target)[scope]), nil
}

func (defaultMCPManager) writeScope(target MCPTarget, scope string, names []string) error {
	if err := checkScope(target.Tool, scope); err != nil {
		return err
	}
	var err error
	switch scope {
	case "local":
		err = session.WriteLocalMCPConfigForTool(target.Tool, target.ProjectPath, names)
	case "project":
		// checkScope has already established the tool is Claude-compatible.
		err = session.WriteProjectMCP(target.ProjectPath, names)
	case "global":
		err = session.WriteGlobalMCPConfigForTool(target.Tool, names)
	default:
		// ~/.claude.json is Claude's own user-level store.
		err = session.WriteUserMCP(names)
	}
	if err != nil {
		return err
	}
	// The readers above are cached (30s TTL for the Claude, Cursor and
	// OpenCode project readers). Without this, the pane's refresh immediately
	// after a mutation returns the pre-mutation list and the change looks like
	// it failed. The TUI apply path invalidates for the same reason.
	session.InvalidateProjectMCPIntegrationsCache(target.ProjectPath)
	return nil
}

func checkScope(tool, scope string) error {
	switch scope {
	case "local", "project", "global", "user":
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
