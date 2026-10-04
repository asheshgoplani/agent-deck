package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func TestActiveFilterSurvivesRestart(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "default"
		if custom {
			name = "custom"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)
			storage, err := session.NewStorageWithProfile("_filter_restart")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { storage.Close() })
			rows := []*session.Instance{
				{ID: "stopped", Title: "stopped-row", GroupPath: "dead", Status: session.StatusStopped},
				{ID: "error", Title: "error-row", GroupPath: "dead", Status: session.StatusError},
			}
			setup := func() *Home {
				h := NewHome()
				h.storage = storage
				h.groupTree = session.NewGroupTree(rows)
				h.instances = rows
				if custom {
					h.activeFilterExcludes = map[session.Status]bool{session.StatusError: true}
				}
				h.rebuildFlatItems()
				return h
			}
			before := setup()
			press := func(h *Home) { _, _ = h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'%'}}) }
			press(before)
			if custom {
				press(before)
			}
			if len(before.flatItems) != 0 {
				t.Fatalf("filter retained %d dead rows/groups", len(before.flatItems))
			}
			if err := before.saveUIStateErr(); err != nil {
				t.Fatal(err)
			}
			after := setup()
			after.loadUIState()
			after.rebuildFlatItems()
			if after.statusFilter != FilterModeActive || len(after.flatItems) != 0 {
				t.Fatalf("restart lost active filter: filter=%q rows=%d", after.statusFilter, len(after.flatItems))
			}
			for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
				after.width, after.height = size[0], size[1]
				after.initialLoading = false
				frame := stripAnsi(after.renderFilterBar() + "\n" + after.renderSessionList(after.sessionsPaneWidth(), size[1]-5))
				path := filepath.Join("testdata", "active_filter", fmt.Sprintf("%dx%d.txt", size[0], size[1]))
				if os.Getenv("UPDATE_GOLDEN") != "" {
					if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(frame), 0644); err != nil {
						t.Fatal(err)
					}
				} else {
					want, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if frame != string(want) {
						t.Fatalf("%s differs:\n%s", path, frame)
					}
				}
			}
			press(after)
			if after.statusFilter != "" || len(after.flatItems) == 0 {
				t.Fatal("next % did not restore All")
			}
		})
	}
}

func TestActiveFilterLegacyState(t *testing.T) {
	for _, raw := range []string{`{"status_filter":"active"}`, `{"status_filter":"error","active_filter_hide_stopped":true}`} {
		var state uiState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
		h := NewHome()
		h.activeFilterHideStopped = true
		h.applyUIState(state)
		if h.activeFilterHideStopped {
			t.Fatalf("stale extra step retained for %s", raw)
		}
	}
}
