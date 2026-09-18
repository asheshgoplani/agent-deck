package ui

// Finding 2 (live UI audit, rc.6, 2026-09-18): opening New Session from a
// remote root row pre-filled Model ID with "fable" before any tool was even
// chosen, while the identical local dialog showed "tool default (↓ to
// browse)". The remote host's own configured default_model traveled through
// the creation catalog (RemoteCreationTool.DefaultModel) and was written
// straight into modelInput by SetRemoteCreationCatalog, with none of the
// gating the local dialog's preselectDefaultModel applies to the *local
// user's own* config.claude.default_model. That local gate only prefills a
// value the local user actually configured; a remote's own default_model is
// never something this user chose in this dialog, so the fix simply never
// seeds it — the field starts and stays empty (placeholder: "tool default
// (↓ to browse)") until the user picks a model themselves.
//
// Golden frame: testdata/newdialog_flow/07-remote-model-default.txt.
// Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ui/ -run TestRemoteDialog_ModelDefault_Golden

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// remoteCatalogWithDefaultModel builds a non-legacy catalog whose default
// tool carries a bogus/unvalidated DefaultModel, reproducing the exact shape
// the audit's agentbox remote reported.
func remoteCatalogWithDefaultModel(tool, defaultModel string) *session.RemoteCreationCatalog {
	catalog := remoteDialogTestCatalog()
	catalog.DefaultTool = tool
	for i := range catalog.Tools {
		if catalog.Tools[i].Name == tool {
			catalog.Tools[i].DefaultModel = defaultModel
			catalog.Tools[i].Models = []string{"claude-sonnet-5", "claude-opus-5"}
		}
	}
	return catalog
}

// TestRemoteDialog_SetCreationCatalog_NeverSeedsModelFromRemoteDefault is a
// table test over the seed logic: whatever the remote reports as its
// default_model for whichever tool ends up selected, SetRemoteCreationCatalog
// must never write it into modelInput. Only the user (typing, or picking a
// suggestion) sets a concrete model on a remote target.
func TestRemoteDialog_SetCreationCatalog_NeverSeedsModelFromRemoteDefault(t *testing.T) {
	forceTrueColorProfile()
	tests := []struct {
		name         string
		tool         string
		defaultModel string
	}{
		{"bogus short name, matches the audit", "claude", "fable"},
		{"a real known Claude model id", "claude", "claude-sonnet-5"},
		{"empty default", "claude", ""},
		{"non-claude tool with a default", "codex", "gpt-5.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, _ := newRemoteHome(t, remoteGroupItem("agentbox"), "")
			h := pressN(t, home)
			catalog := remoteCatalogWithDefaultModel(tt.tool, tt.defaultModel)
			h.newDialog.SetRemoteCreationCatalog(catalog)
			if got := h.newDialog.GetSelectedCommand(); got != tt.tool {
				t.Fatalf("precondition: selected tool = %q, want %q", got, tt.tool)
			}
			if got := h.newDialog.modelInput.Value(); got != "" {
				t.Fatalf("remote dialog seeded Model ID = %q from the remote's own default_model %q; want empty (tool default) until the user picks one", got, tt.defaultModel)
			}
			if got := h.newDialog.modelInput.Placeholder; got != "tool default (↓ to browse)" {
				t.Fatalf("remote dialog placeholder = %q, want the same sentinel local shows", got)
			}
		})
	}
}

// TestRemoteDialog_ModelDefault_Golden is the golden-frame counterpart:
// renders the dialog's Model row for a remote target whose default tool
// reports a DefaultModel, and checks the frame shows the same "tool default
// (↓ to browse)" placeholder the local dialog shows — not a concrete model.
func TestRemoteDialog_ModelDefault_Golden(t *testing.T) {
	forceTrueColorProfile()
	home, _ := newRemoteHome(t, remoteGroupItem("agentbox"), "")
	h := pressN(t, home)
	catalog := remoteCatalogWithDefaultModel("claude", "fable")
	h.newDialog.SetRemoteCreationCatalog(catalog)
	d := h.newDialog
	d.SetSize(100, 50)
	d.nameInput.SetValue("demo")
	flowFocus(t, d, focusModel)

	got := strings.TrimRight(stripAnsi(d.View()), "\n") + "\n"
	path := filepath.Join("testdata", "newdialog_flow", "07-remote-model-default.txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (UPDATE_GOLDEN=1 to create)", path, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s differs from the rendered dialog.\n--- want\n%s\n--- got\n%s", path, want, got)
	}
	if strings.Contains(got, "fable") {
		t.Fatalf("frame must not show the remote's unvalidated default_model:\n%s", got)
	}
	if !strings.Contains(got, "tool default (↓ to browse)") {
		t.Fatalf("frame must show the same sentinel the local dialog shows:\n%s", got)
	}
}
