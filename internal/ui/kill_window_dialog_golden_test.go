package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestConfirmKillWindowDialogGolden pins the full rendered kill-window
// confirm dialog frame (title, target window, destructive-action details,
// and button row) so an unintended wording or layout change shows up as a
// diff here. The internal window id captured for the identity guard
// (WindowID, used to re-verify at confirm time — see ErrWindowChanged in
// internal/tmux) is plumbing, not user-facing text, so it must never appear
// in the rendered frame.
func TestConfirmKillWindowDialogGolden(t *testing.T) {
	d := &ConfirmDialog{}
	d.SetSize(80, 24)
	d.ShowKillWindow("kw-1", 2, "agent", "@42")

	got := ansi.Strip(d.View()) + "\n"

	path := filepath.Join("testdata", "kill_window_confirm.golden")
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
		t.Fatalf("kill-window confirm dialog golden mismatch\nwant %q\ngot  %q", want, got)
	}
}
