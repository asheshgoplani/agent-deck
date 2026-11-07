package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/terminal"
)

// ASK-201: Shift+Enter on a remote session opens a window that runs this
// machine's `agent-deck remote attach`, so Ctrl+V image paste works there as
// it does after Enter (it was a raw ssh line, internal/terminal/launcher.go).
func TestASK201_ShiftEnterRemoteRunsLocalRemoteAttach(t *testing.T) {
	home := armHomeWithOneRemoteSession(t)
	home.profile = "personal"
	var captured terminal.AttachRequest
	home.openInNewWindowSink = func(req terminal.AttachRequest) error { captured = req; return nil }

	_, _ = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{shiftEnterMarker}})

	if captured.Remote == nil {
		t.Fatal("no remote request")
	}
	exe, _ := os.Executable()
	if captured.Remote.LocalCLI != exe || captured.Remote.RemoteName != "lab" || captured.Remote.LocalProfile != "personal" {
		t.Fatalf("request = %+v", captured.Remote)
	}
	cmd := terminal.BuildAttachCommand(captured)
	if !strings.Contains(cmd, " remote attach 'lab' 'remote-id-xyz'") || strings.HasPrefix(cmd, "ssh ") {
		t.Fatalf("Shift+Enter window runs %q", cmd)
	}
}
