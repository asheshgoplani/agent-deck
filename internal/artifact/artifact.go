// Package artifact is the provenance + listing layer for the Fleet Console.
//
// A conductor session that writes an HTML report also drops a sidecar
// "<file>.meta.json" next to it (via `agent-deck artifact stamp`). The sidecar
// records WHICH session produced the artifact so a highlight-to-comment in the
// web console can auto-route the annotation back to its owning session. Files
// without a sidecar (the legacy corpus) still attribute to their owning
// conductor — derivable from the "<conductor>/<file>.html" directory layout —
// so routing is never a dead end.
//
// This package is deliberately free of any session/web dependency: it only
// reads and writes JSON sidecars and globs a directory tree. That keeps the
// provenance contract unit-testable in isolation and shared by both the CLI
// (the producer) and the web handlers (the consumer).
package artifact

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// metaSuffix is appended to an artifact's path to locate its sidecar.
const metaSuffix = ".meta.json"

// ErrPathEscape is returned when a requested artifact path would resolve
// outside the conductor root (a traversal attempt).
var ErrPathEscape = errors.New("artifact: path escapes root")

// Meta is the sidecar provenance record written next to an artifact HTML.
type Meta struct {
	ArtifactID string `json:"artifact_id"`
	SessionID  string `json:"session_id"`
	Group      string `json:"group"`
	Profile    string `json:"profile"`
	Title      string `json:"title"`
	CreatedAt  string `json:"created_at"`
}

// Entry is one row in the Fleet Console artifact list. Sidecar-attributed
// fields fall back to filename/directory derivation when no sidecar exists.
type Entry struct {
	ArtifactID string `json:"artifactId"`
	Title      string `json:"title"`
	// Conductor is always known — it is the top-level directory component, so
	// even a sidecar-less artifact routes to its owning conductor.
	Conductor string `json:"conductor"`
	SessionID string `json:"sessionId,omitempty"`
	Group     string `json:"group,omitempty"`
	Profile   string `json:"profile,omitempty"`
	// Path is relative to the conductor root ("<conductor>/<file>.html"). It is
	// the opaque handle the serve + comment endpoints pass back, always
	// re-confined server-side before any filesystem access.
	Path       string `json:"path"`
	CreatedAt  string `json:"createdAt,omitempty"`
	HasSidecar bool   `json:"hasSidecar"`
}

// SidecarPath returns the sidecar path for an artifact HTML path.
func SidecarPath(htmlPath string) string {
	return htmlPath + metaSuffix
}

// WriteMeta writes (or overwrites) the sidecar for the given artifact HTML.
func WriteMeta(htmlPath string, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(SidecarPath(htmlPath), data, 0o644)
}

// ReadMeta reads the sidecar for an artifact HTML. ok is false (with nil error)
// when no sidecar exists — the common legacy case, not a failure.
func ReadMeta(htmlPath string) (m Meta, ok bool, err error) {
	data, err := os.ReadFile(SidecarPath(htmlPath))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, false, nil
		}
		return Meta{}, false, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, false, err
	}
	return m, true, nil
}

// ConfinedPath resolves a caller-supplied relative artifact path against root,
// guaranteeing the result stays inside root. A leading "/" is treated as
// root-relative (neutralized, not an escape); any ".." that would climb above
// root is rejected with ErrPathEscape.
func ConfinedPath(root, rel string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("artifact: empty root")
	}
	// Reject any traversal segment outright rather than silently absorbing it —
	// "../" must be a hard error so an audit of the access log shows the attempt
	// instead of a quietly-neutralized path. A leading "/" and "." are fine.
	slashRel := filepath.ToSlash(rel)
	if slices.Contains(strings.Split(slashRel, "/"), "..") {
		return "", ErrPathEscape
	}
	cleaned := filepath.Clean("/" + slashRel)
	full := filepath.Join(root, cleaned)

	// Belt-and-suspenders: confirm the joined path is genuinely within root.
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	rel2, err := filepath.Rel(rootAbs, fullAbs)
	if err != nil {
		return "", ErrPathEscape
	}
	if rel2 == ".." || strings.HasPrefix(rel2, ".."+string(os.PathSeparator)) {
		return "", ErrPathEscape
	}
	return fullAbs, nil
}

// ListArtifacts globs "<root>/<conductor>/<file>.html" and returns one Entry
// per artifact, enriched from its sidecar when present and falling back to
// filename/directory derivation otherwise. A missing root yields no entries
// (cold start), not an error.
func ListArtifacts(root string) ([]Entry, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	conductors, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var entries []Entry
	for _, cd := range conductors {
		if !cd.IsDir() {
			continue
		}
		conductor := cd.Name()
		files, err := os.ReadDir(filepath.Join(root, conductor))
		if err != nil {
			continue // unreadable conductor dir — skip rather than fail the whole list
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".html") {
				continue
			}
			rel := filepath.Join(conductor, f.Name())
			htmlPath := filepath.Join(root, rel)
			base := strings.TrimSuffix(f.Name(), ".html")

			e := Entry{
				ArtifactID: base,
				Title:      base,
				Conductor:  conductor,
				Path:       rel,
			}
			if meta, ok, _ := ReadMeta(htmlPath); ok {
				e.HasSidecar = true
				if meta.ArtifactID != "" {
					e.ArtifactID = meta.ArtifactID
				}
				if meta.Title != "" {
					e.Title = meta.Title
				}
				if meta.Group != "" {
					e.Group = meta.Group
				}
				e.SessionID = meta.SessionID
				e.Profile = meta.Profile
				e.CreatedAt = meta.CreatedAt
			}
			entries = append(entries, e)
		}
	}

	// Newest first when timestamps are present, else stable by path.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].CreatedAt != entries[j].CreatedAt {
			return entries[i].CreatedAt > entries[j].CreatedAt
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}
