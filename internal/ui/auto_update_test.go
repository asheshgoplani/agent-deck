package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestAutomaticUpdatePromptDefaultNoAndOneKeyYes(t *testing.T) {
	p := NewAutomaticUpdatePrompt()
	if p.Enabled() {
		t.Fatal("first-run automatic update prompt must default No")
	}
	if !p.HandleKey("y") || !p.Enabled() {
		t.Fatal("one y keypress must opt in")
	}
	if !strings.Contains(p.View(defaultStyle(), defaultStyle(), defaultStyle()), "updates.auto_install") {
		t.Fatal("prompt must name the later config seam")
	}
}

func TestAutoUpdateGauntletFrames(t *testing.T) {
	outDir := os.Getenv("AGENTDECK_AUTOUPDATE_CAPTURE_DIR")
	if outDir == "" {
		t.Skip("set AGENTDECK_AUTOUPDATE_CAPTURE_DIR to write visual frames")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, size := range []struct{ w, h int }{{100, 30}, {160, 48}} {
		mk := func() *Home {
			h := NewHome()
			h.initialLoading = false
			h.width, h.height = size.w, size.h
			for i := 0; i < 40; i++ {
				inst := session.NewInstance(fmt.Sprintf("session-%02d", i), t.TempDir())
				inst.Title = fmt.Sprintf("Session %02d", i)
				h.instances = append(h.instances, inst)
			}
			h.groupTree = session.NewGroupTree(h.instances)
			h.rebuildFlatItems()
			h.cursor, h.viewOffset = 22, 15
			return h
		}
		write := func(name string, h *Home) {
			frame := ansi.Strip(h.View())
			if err := os.WriteFile(filepath.Join(outDir, fmt.Sprintf("%s-%dx%d.txt", name, size.w, size.h)), []byte(frame), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		before := mk()
		write("before-reexec-selection-scroll", before)
		after := mk()
		after.maintenanceMsg = "Updated to v1.15.0 — selection + list scroll restored; live tmux sessions were unaffected (brief screen blink is expected)"
		write("after-reexec-selection-scroll", after)
		busy := mk()
		busy.autoUpdateVersion = "1.15.0"
		busy.creatingSessions["busy"] = &CreatingSession{}
		busy.maintenanceMsg = busy.autoUpdateHint()
		write("busy-hint", busy)
		idle := mk()
		idle.maintenanceMsg = "Updated to v1.15.0 — selection + list scroll restored; live tmux sessions were unaffected (brief screen blink is expected)"
		write("idle-toast", idle)
	}
}

func defaultStyle() lipgloss.Style { return lipgloss.NewStyle() }

// Revert pin: all three tiers share this guard; an open dialog or in-flight
// operation must never permit a forced re-exec.
func TestAutoUpdateReexecSafetyGuard(t *testing.T) {
	h := NewHome()
	h.autoUpdateVersion = "1.15.0"
	h.initialLoading = false
	if !h.safeForUpdateReexec() {
		t.Fatal("idle home should be safe")
	}
	h.settingsPanel.Show()
	if h.safeForUpdateReexec() {
		t.Fatal("open dialog must block re-exec")
	}
	h.settingsPanel.Hide()
	h.creatingSessions["pending"] = &CreatingSession{}
	if h.safeForUpdateReexec() {
		t.Fatal("in-flight operation must block re-exec")
	}
	model, _ := h.handleMainKey(tea.KeyMsg{Type: tea.KeyF12})
	if model.(*Home).reexecRequested {
		t.Fatal("F12 must not force re-exec through unsafe state")
	}
}
