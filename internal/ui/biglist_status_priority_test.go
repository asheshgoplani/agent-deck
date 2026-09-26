package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// priorityFleet is 100 running rows behind a tmux shim that answers every
// probe with exit 0; a polled row leaves StatusRunning, an unpolled one keeps it.
func priorityFleet(t *testing.T) (*Home, func() []int) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := &Home{}
	for i := 0; i < 100; i++ {
		h.instances = append(h.instances, &session.Instance{
			ID: fmt.Sprintf("s%d", i), Tool: "shell", Status: session.StatusRunning,
		})
	}
	updated := func() []int {
		var rows []int
		for i, inst := range h.instances {
			if inst.GetStatusThreadSafe() != session.StatusRunning {
				rows = append(rows, i)
			}
		}
		return rows
	}
	return h, updated
}

func containsRow(rows []int, want int) bool {
	for _, r := range rows {
		if r == want {
			return true
		}
	}
	return false
}

// A row whose hook evidence moves is polled on the very next pass, even though
// the rotation budget would only reach it several passes later.
func TestBigListSweepRefreshesChangedEvidenceNextTick(t *testing.T) {
	h, updated := priorityFleet(t)
	h.backgroundStatusUpdate()
	first := updated()
	if len(first) == 0 || len(first) > fullStatusBatchSize || containsRow(first, 99) {
		t.Fatalf("first tick polled %v", first)
	}
	h.instances[99].UpdateHookStatus(&session.HookStatus{Status: "waiting", Event: "Notification", UpdatedAt: time.Now()})
	h.backgroundStatusUpdate()
	second := updated()
	if !containsRow(second, 99) {
		t.Fatalf("row with new hook evidence not polled on the next tick: %v", second)
	}
	if len(second) > 2*fullStatusBatchSize+1 {
		t.Fatalf("second tick exceeded the budget: %d rows", len(second))
	}
	// Applied evidence is not re-forced on the following tick.
	h.instances[99].SetStatusThreadSafe(session.StatusRunning)
	h.backgroundStatusUpdate()
	if third := updated(); containsRow(third, 99) {
		t.Fatalf("row re-polled without new evidence: %v", third)
	}
}

// Visible rows are refreshed every pass regardless of where the rotation is.
func TestBigListSweepRefreshesVisibleRowsEveryTick(t *testing.T) {
	h, updated := priorityFleet(t)
	ids := make([]string, 0, len(h.instances))
	for _, inst := range h.instances {
		ids = append(ids, inst.ID)
	}
	h.noteSweepVisibleRows(statusUpdateRequest{viewOffset: 90, visibleHeight: 5, flatItemIDs: ids})
	for tick := 0; tick < 2; tick++ {
		for i := 90; i < 95; i++ {
			h.instances[i].SetStatusThreadSafe(session.StatusRunning)
		}
		h.backgroundStatusUpdate()
		rows := updated()
		for i := 90; i < 95; i++ {
			if !containsRow(rows, i) {
				t.Fatalf("tick %d: visible row %d not refreshed: %v", tick, i, rows)
			}
		}
		if len(rows) > (tick+1)*fullStatusBatchSize+5 {
			t.Fatalf("tick %d exceeded the budget: %d rows", tick, len(rows))
		}
	}
}
