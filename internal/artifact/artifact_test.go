package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a tiny helper for the table tests below.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// SPEC T1: serve-one-artifact is path-confined (cannot escape the conductor
// root; "../" rejected).
func TestConfinedPathRejectsEscape(t *testing.T) {
	root := t.TempDir()

	escapes := []string{
		"../etc/passwd",
		"condA/../../etc/passwd",
		"condA/../../../",
		"..",
		"foo/../../bar",
	}
	for _, rel := range escapes {
		if got, err := ConfinedPath(root, rel); err == nil {
			t.Fatalf("expected %q to be rejected as an escape, got %q", rel, got)
		}
	}
}

func TestConfinedPathAcceptsInRoot(t *testing.T) {
	root := t.TempDir()

	ok := map[string]string{
		"condA/foo.html":      filepath.Join(root, "condA", "foo.html"),
		"condA/sub/bar.html":  filepath.Join(root, "condA", "sub", "bar.html"),
		"/condA/leading.html": filepath.Join(root, "condA", "leading.html"), // leading slash neutralized, not an escape
		"condA/./dot.html":    filepath.Join(root, "condA", "dot.html"),
	}
	for rel, want := range ok {
		got, err := ConfinedPath(root, rel)
		if err != nil {
			t.Fatalf("expected %q to be accepted, got error %v", rel, err)
		}
		if got != want {
			t.Fatalf("ConfinedPath(%q) = %q, want %q", rel, got, want)
		}
		if !strings.HasPrefix(got, root) {
			t.Fatalf("ConfinedPath(%q) = %q escaped root %q", rel, got, root)
		}
	}
}

func TestConfinedPathEmptyRoot(t *testing.T) {
	if _, err := ConfinedPath("", "x.html"); err == nil {
		t.Fatal("expected error for empty root")
	}
}

// SPEC T2: list-artifacts returns sidecar-attributed entries + conductor-level
// fallback for sidecar-less files.
func TestListArtifactsSidecarAndFallback(t *testing.T) {
	root := t.TempDir()

	// Stamped artifact (sidecar present) under conductor "agent-deck".
	writeFile(t, filepath.Join(root, "agent-deck", "perf.html"), "<h1>perf</h1>")
	meta := Meta{
		ArtifactID: "perf-report",
		SessionID:  "sess-123",
		Group:      "agent-deck",
		Profile:    "personal",
		Title:      "ARD Import Perf",
		CreatedAt:  "2026-06-22T11:42:00Z",
	}
	if err := WriteMeta(filepath.Join(root, "agent-deck", "perf.html"), meta); err != nil {
		t.Fatal(err)
	}

	// Legacy artifact (NO sidecar) under conductor "innotrade".
	writeFile(t, filepath.Join(root, "innotrade", "legacy.html"), "<h1>legacy</h1>")

	entries, err := ListArtifacts(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}

	byID := map[string]Entry{}
	for _, e := range entries {
		byID[e.Path] = e
	}

	stamped, ok := byID[filepath.Join("agent-deck", "perf.html")]
	if !ok {
		t.Fatalf("stamped artifact missing from listing: %+v", entries)
	}
	if !stamped.HasSidecar {
		t.Fatalf("expected stamped artifact to report HasSidecar, got %+v", stamped)
	}
	if stamped.SessionID != "sess-123" {
		t.Fatalf("expected sidecar session id sess-123, got %q", stamped.SessionID)
	}
	if stamped.Title != "ARD Import Perf" {
		t.Fatalf("expected sidecar title, got %q", stamped.Title)
	}
	if stamped.Conductor != "agent-deck" {
		t.Fatalf("expected conductor agent-deck, got %q", stamped.Conductor)
	}
	if stamped.ArtifactID != "perf-report" {
		t.Fatalf("expected sidecar artifact id, got %q", stamped.ArtifactID)
	}

	legacy, ok := byID[filepath.Join("innotrade", "legacy.html")]
	if !ok {
		t.Fatalf("legacy artifact missing from listing: %+v", entries)
	}
	if legacy.HasSidecar {
		t.Fatalf("expected legacy artifact to report no sidecar, got %+v", legacy)
	}
	if legacy.SessionID != "" {
		t.Fatalf("expected legacy artifact to have no session id, got %q", legacy.SessionID)
	}
	// Conductor-level fallback: the owning conductor is still derivable from the
	// directory, so routing is never a dead end even without a sidecar.
	if legacy.Conductor != "innotrade" {
		t.Fatalf("expected conductor-level fallback innotrade, got %q", legacy.Conductor)
	}
	// Fallback id/title derive from the filename.
	if legacy.ArtifactID != "legacy" || legacy.Title != "legacy" {
		t.Fatalf("expected filename-derived id/title for legacy, got id=%q title=%q", legacy.ArtifactID, legacy.Title)
	}
}

func TestListArtifactsEmptyRoot(t *testing.T) {
	// A missing/empty root yields no entries and no error (cold start).
	entries, err := ListArtifacts(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected no error for missing root, got %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

// Round-trip: WriteMeta then ReadMeta returns the same provenance.
func TestWriteReadMetaRoundTrip(t *testing.T) {
	root := t.TempDir()
	html := filepath.Join(root, "agent-deck", "doc.html")
	writeFile(t, html, "<h1>doc</h1>")

	in := Meta{ArtifactID: "doc", SessionID: "s1", Group: "agent-deck", Profile: "work", Title: "Doc", CreatedAt: "2026-06-22T00:00:00Z"}
	if err := WriteMeta(html, in); err != nil {
		t.Fatal(err)
	}
	if SidecarPath(html) != html+".meta.json" {
		t.Fatalf("unexpected sidecar path %q", SidecarPath(html))
	}
	out, ok, err := ReadMeta(html)
	if err != nil || !ok {
		t.Fatalf("ReadMeta failed: ok=%v err=%v", ok, err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch: got %+v want %+v", out, in)
	}

	// Absent sidecar reports ok=false, no error.
	_, ok2, err2 := ReadMeta(filepath.Join(root, "agent-deck", "nope.html"))
	if err2 != nil || ok2 {
		t.Fatalf("expected absent sidecar (ok=false,nil err), got ok=%v err=%v", ok2, err2)
	}
}
