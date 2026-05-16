package ui

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Regression tests for #936 — Input line drifts off the visible viewport
// after host-terminal zoom or session switch.
//
// Reporter: Kevsosmooth, on agent-deck running on Linux Synology DSM 7
// via SSH from a macOS host terminal. Reliable repro: zoom in (Cmd++),
// then switch to another agent-deck session — input area renders above
// the real viewport bottom and long prompts run off the right edge.
//
// Root cause: cached column/row count from the previous viewport is not
// invalidated when the host terminal sends a resize after a zoom, or
// when the attached-to session has different dimensions. SIGWINCH
// propagation through nested SSH+tmux is late or lost.
//
// Fix: force-poll terminal dims by emitting tea.WindowSize() from the
// statusUpdateMsg (post-attach) handler. Bubbletea intercepts the
// returned windowSizeMsg{} and replies with a fresh tea.WindowSizeMsg,
// which the existing WindowSizeMsg handler already turns into a
// updateSizes()+syncViewport() recompute.

// Test_Issue936_StatusUpdateMsg_ForcesWindowSizeRepoll asserts that the
// post-attach handler includes a tea.WindowSize() command in its
// returned batch. Without this, host-terminal zoom that happens during
// attach is silently dropped and the input line renders at the old
// viewport bottom.
func Test_Issue936_StatusUpdateMsg_ForcesWindowSizeRepoll(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 40

	_, cmd := home.Update(statusUpdateMsg{})
	if cmd == nil {
		t.Fatal("statusUpdateMsg returned nil cmd — viewport revalidation hook missing")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg from statusUpdateMsg handler, got %T", cmd())
	}

	if !batchContainsWindowSizeRepoll(batch) {
		t.Fatalf(
			"statusUpdateMsg batch must contain a tea.WindowSize() command after #936.\n" +
				"Without this, host-terminal zoom during attach is dropped — input renders " +
				"above the real viewport bottom and long prompts run off the right edge.\n" +
				"Reported by Kevsosmooth on Linux Synology DSM 7 via SSH from macOS.",
		)
	}
}

// Test_Issue936_WindowSizeMsg_RebuildsViewport pins the downstream
// contract: when a fresh WindowSizeMsg arrives (because we polled, or
// because SIGWINCH did fire), the handler must update both
// h.width / h.height. Without this the re-poll is wasted.
func Test_Issue936_WindowSizeMsg_RebuildsViewport(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 40

	model, _ := home.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h, ok := model.(*Home)
	if !ok {
		t.Fatalf("Update returned %T, want *Home", model)
	}
	if h.width != 80 {
		t.Errorf("h.width = %d after WindowSizeMsg{80,24}, want 80 (cached pre-zoom dim survived)", h.width)
	}
	if h.height != 24 {
		t.Errorf("h.height = %d after WindowSizeMsg{80,24}, want 24 (cached pre-zoom dim survived)", h.height)
	}
}

// batchContainsWindowSizeRepoll walks a tea.BatchMsg looking for the
// bubbletea-internal windowSizeMsg{} (the message tea.WindowSize()
// emits). The type is package-private to bubbletea, so we identify it
// by reflect-name. Narrow by design: "the batch contains the cmd that
// tea.WindowSize() returns", nothing more.
func batchContainsWindowSizeRepoll(batch tea.BatchMsg) bool {
	for _, c := range batch {
		if c == nil {
			continue
		}
		msg := c()
		if msg == nil {
			continue
		}
		t := reflect.TypeOf(msg)
		if t == nil {
			continue
		}
		if t.PkgPath() == "github.com/charmbracelet/bubbletea" && t.Name() == "windowSizeMsg" {
			return true
		}
	}
	return false
}
