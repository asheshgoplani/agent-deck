package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestEditDialogValidate_RejectsInvalidEnv ensures an edited env field with an
// invalid entry is blocked (not silently dropped / unsetting existing keys).
func TestEditDialogValidate_RejectsInvalidEnv(t *testing.T) {
	inst := &session.Instance{ID: "x", Title: "t", Tool: "claude", Env: []string{"FOO=bar"}}
	d := NewEditSessionDialog()
	d.Show(inst)
	for i := range d.fields {
		if d.fields[i].key == session.FieldEnv {
			d.fields[i].area.SetValue("1BAD=x") // invalid key, changed from orig
		}
	}
	if msg := d.Validate(); msg == "" || !strings.Contains(msg, "Invalid env") {
		t.Fatalf("expected invalid-env validation error, got %q", msg)
	}
}

// TestEditDialogValidate_UnchangedEnvPasses ensures an untouched env field (here
// a space-bearing value the multi-line textarea round-trips) does not block edits.
func TestEditDialogValidate_UnchangedEnvPasses(t *testing.T) {
	inst := &session.Instance{ID: "x", Title: "t", Tool: "claude", Env: []string{"MSG=hello world"}}
	d := NewEditSessionDialog()
	d.Show(inst)
	if msg := d.Validate(); msg != "" {
		t.Fatalf("untouched env must not block validation, got %q", msg)
	}
}

// TestNewDialogValidate_RejectsInvalidEnv ensures a bad env line in the new
// dialog surfaces an error instead of being silently dropped at create.
func TestNewDialogValidate_RejectsInvalidEnv(t *testing.T) {
	d := NewNewDialog()
	d.Show()
	d.nameInput.SetValue("demo")
	d.pathInput.SetValue("/tmp/project")
	d.envInput.SetValue("FOO=ok\n1BAD=x")
	if msg := d.Validate(); msg == "" || !strings.Contains(msg, "Invalid env") {
		t.Fatalf("expected invalid-env validation error, got %q", msg)
	}
}
