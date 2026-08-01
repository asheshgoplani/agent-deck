package ui

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

func TestEmbeddedCursorCallbacksDoNotPublishPartialFrame(t *testing.T) {
	terminal := &embeddedTerminal{
		dirty:  make(chan struct{}, 1),
		cursor: embeddedCursorState{Visible: true, Style: vt.CursorBlock},
	}
	callbacks := terminal.emulatorCallbacks()
	callbacks.CursorVisibility(false)
	callbacks.CursorStyle(vt.CursorBar, true)

	select {
	case <-terminal.dirty:
		t.Fatal("cursor callback published a frame before emulator.Write completed")
	default:
	}

	got := terminal.cursor
	if got.Visible || got.Style != vt.CursorBar || !got.Steady {
		t.Fatalf("cursor callback state = %+v", got)
	}
}

func TestEmbeddedCursorSnapshotWaitsForCompleteEmulatorFrame(t *testing.T) {
	emulator := vt.NewSafeEmulator(20, 4)
	defer func() { _ = emulator.Close() }()
	terminal := &embeddedTerminal{
		emulator: emulator,
		dirty:    make(chan struct{}, 1),
		cursor:   embeddedCursorState{Visible: true, Style: vt.CursorBlock},
	}

	hidden := make(chan struct{})
	resumeFrame := make(chan struct{})
	emulator.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			terminal.cursorMu.Lock()
			terminal.cursor.Visible = visible
			terminal.cursorMu.Unlock()
			if !visible {
				close(hidden)
				<-resumeFrame
			}
		},
	})

	frameDone := make(chan struct{})
	go func() {
		terminal.applyEmulatorOutput([]byte("\x1b[?25lworking\x1b[?25h"))
		close(frameDone)
	}()

	select {
	case <-hidden:
	case <-time.After(time.Second):
		t.Fatal("emulator never reached temporary hidden-cursor state")
	}

	snapshot := make(chan embeddedCursorState, 1)
	go func() { snapshot <- terminal.Cursor() }()
	select {
	case got := <-snapshot:
		t.Fatalf("cursor snapshot escaped a partial emulator frame: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}

	close(resumeFrame)
	select {
	case got := <-snapshot:
		if !got.Visible {
			t.Fatalf("cursor snapshot kept transient hidden state after frame commit: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cursor snapshot remained blocked after frame commit")
	}
	select {
	case <-frameDone:
	case <-time.After(time.Second):
		t.Fatal("emulator frame did not finish")
	}
}
