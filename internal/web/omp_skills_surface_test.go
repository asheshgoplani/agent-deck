package web

import (
	"io/fs"
	"strings"
	"testing"
)

// This pins the shipped JavaScript surface (the embedded asset, not a duplicate
// Go allowlist): OMP must take the supported rendering/attach/detach branch.
func TestWebSkillsPaneShipsOMPSupportAndActions(t *testing.T) {
	b, err := fs.ReadFile(embeddedStaticFiles, "static/app/panes/SkillsPane.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"'omp'", "skill-attach-btn", "skill-detach-btn", "apiFetch('POST'", "apiFetch('DELETE'"} {
		if !strings.Contains(src, want) {
			t.Fatalf("embedded SkillsPane missing %q", want)
		}
	}
}
