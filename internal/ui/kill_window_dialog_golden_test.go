package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// assertDialogGolden compares the dialog's rendered frame with the golden
// file under testdata (UPDATE_GOLDEN=1 rewrites it).
func assertDialogGolden(t *testing.T, d *ConfirmDialog, name string) {
	t.Helper()
	got := ansi.Strip(d.View()) + "\n"

	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s golden mismatch\nwant %q\ngot  %q", name, want, got)
	}
}

// TestConfirmKillWindowDialogGolden pins the full rendered kill-window
// confirm dialog frame (title, target window by index, stable id and name,
// destructive-action details, and button row) so an unintended wording or
// layout change shows up as a diff here. The id is shown on purpose: it is
// what confirm re-verifies, so the user confirms one specific window and can
// match it against `session window close --json`.
func TestConfirmKillWindowDialogGolden(t *testing.T) {
	d := &ConfirmDialog{}
	d.SetSize(80, 24)
	d.ShowKillWindow("kw-1", 2, "agent", "@42")
	assertDialogGolden(t, d, "kill_window_confirm.golden")
}

// TestKillWindowRefusedGolden pins the two refusal frames a confirmed kill
// can end in. They live in the modal (not the footer, which is clamped away
// on a full viewport) so the user sees why nothing was closed.
func TestKillWindowRefusedGolden(t *testing.T) {
	for _, tc := range []struct {
		golden string
		reason string
	}{
		{"kill_window_refused_changed.golden", `not killing window 2 (@42) "agent": it changed since it was selected, refusing`},
		{"kill_window_refused_last.golden", `not killing window 2 (@42) "agent": it is the session's last window`},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			d := &ConfirmDialog{}
			d.SetSize(80, 24)
			d.ShowKillWindow("kw-1", 2, "agent", "@42")
			d.Hide()
			d.ShowKillWindowRefused(tc.reason)
			if d.GetConfirmType() != ConfirmNotice {
				t.Fatalf("refusal must be an acknowledge-only notice, got %v", d.GetConfirmType())
			}
			assertDialogGolden(t, d, tc.golden)
		})
	}
}
