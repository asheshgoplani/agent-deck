package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/filelinks"
	"github.com/asheshgoplani/agent-deck/internal/openpages"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// openPageCSP is openpages.PageCSP (inline scripts and styles, inlined
// images and fonts, no network, no frames, no forms) inside a sandbox without
// allow-same-origin or allow-popups: the page gets an opaque origin, so it
// can read neither the deck's cookies nor its API, and cannot open windows.
// The header carries the same per-document script hashes as the document's
// own <meta> (openpages.DocumentCSP): no 'unsafe-inline'.
func openPageCSP(doc []byte) string {
	return "sandbox allow-scripts; " + openpages.DocumentCSP(doc) + "; " + frameAncestorsNone
}

// handleSessionOpenPages serves the pages `agent-deck open` showed for a
// session (the HTTP form of `agent-deck open --list/--show`):
//
//	GET /api/sessions/{id}/open-pages            newest first
//	GET /api/sessions/{id}/open-pages/{pageId}   the page as one self-contained HTML document
//	                                             (?format=json: with metadata, as `open --show --json`)
//
// The caller is already authorized (handleSessionByAction). Files are read
// under the session's project path and [web] file_link_roots only.
func (s *Server) handleSessionOpenPages(w http.ResponseWriter, r *http.Request, sessionID, sub string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !openpages.ValidSessionID(sessionID) {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid session id")
		return
	}
	if sub != "" && !openpages.ValidPageID(sub) {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "page not found")
		return
	}
	service, ok := s.menuData.(*SessionDataService)
	if !ok {
		service = NewSessionDataService(s.cfg.Profile)
	}
	inst, _, err := loadTimelineSession(service, sessionID)
	if err != nil {
		if errors.Is(err, errTimelineSessionNotFound) {
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "session not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load session data")
		return
	}
	profile := service.Profile()
	w.Header().Set("Cache-Control", "no-store")
	if sub == "" {
		pages, err := openpages.List(profile, inst.ID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to read the page list")
			return
		}
		for i := range pages {
			pages[i] = pages[i].ForPhone()
		}
		writeJSON(w, http.StatusOK, map[string]any{"session": inst.ID, "title": inst.Title, "pages": pages})
		return
	}
	page, err := openpages.Get(profile, inst.ID, sub)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "page not found")
		return
	}
	roots := filelinks.WithProjectRoot(session.GetWebFileLinkRoots(), inst.ProjectPath)
	all, _ := openpages.List(profile, inst.ID)
	rendered, err := openpages.RenderLinked(roots, page, openpages.Links(all))
	if err != nil {
		status, code := fileLinkErrorStatus(err)
		switch {
		case errors.Is(err, openpages.ErrTooLarge):
			status, code = http.StatusRequestEntityTooLarge, ErrCodeFileLinkRefused
		case errors.Is(err, openpages.ErrPrivate), errors.Is(err, openpages.ErrUnsupported):
			status, code = http.StatusForbidden, ErrCodeFileLinkRefused
		}
		writeAPIError(w, status, code, err.Error())
		return
	}
	if strings.EqualFold(r.URL.Query().Get("format"), "json") {
		resp := map[string]any{
			"id": page.ID, "kind": page.Kind, "title": rendered.Title, "name": page.Name, "url": page.URL,
			"html": string(rendered.HTML), "bytes": len(rendered.HTML), "inlined": rendered.Inlined, "skipped": rendered.Skipped,
			"modified_at": "",
		}
		if !rendered.ModifiedAt.IsZero() {
			resp["modified_at"] = rendered.ModifiedAt.Format(time.RFC3339)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if page.Kind == openpages.KindURL {
		writeJSON(w, http.StatusOK, map[string]any{"id": page.ID, "kind": page.Kind, "url": page.URL})
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", openPageCSP(rendered.HTML))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(rendered.HTML)
	}
}
