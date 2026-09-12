package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIssue2062DeadLetterPanelListShowRetryAndConfirmedPurge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	session.ClearUserConfigCache()

	path := session.DeadLetterPathFor("gone-child")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"child_session_id":"gone-child","child_title":"worker","profile":"default","target_session_id":"gone-parent","timestamp":"` + time.Now().Add(-time.Hour).Format(time.RFC3339Nano) + `","attempts":5,"dead_letter_reason":"parent_removed","done_summary":"private prompt content"}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	panel := NewDeadLetterPanel()
	panel.SetSize(100, 30)
	panel.Show()
	view := panel.View()
	if !strings.Contains(view, "gone-child") || !strings.Contains(view, "parent_removed") {
		t.Fatalf("list missing record metadata: %q", view)
	}
	if strings.Contains(view, "private prompt") {
		t.Fatalf("list exposed content: %q", view)
	}

	panel, _ = panel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if detail := panel.View(); !strings.Contains(detail, "Payload:") || strings.Contains(detail, "private prompt") {
		t.Fatalf("detail is missing or unsafe: %q", detail)
	}
	panel, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !strings.Contains(panel.View(), "no longer exists") || len(panel.records) != 1 {
		t.Fatalf("failed retry was not honest/retained: %q records=%d", panel.View(), len(panel.records))
	}

	panel, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !panel.confirmPurge {
		t.Fatal("purge did not require confirmation")
	}
	panel, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if len(panel.records) != 0 {
		t.Fatalf("confirmed selected purge retained records: %+v", panel.records)
	}
}
