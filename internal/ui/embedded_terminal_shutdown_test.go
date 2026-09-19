package ui

import (
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

func TestEmbeddedCloseUnblocksPendingTerminalReply(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	// The first reply is read from the emulator, but cannot reach this PTY.
	// Its copier then exits while parsing the second query needs a reader.
	if err := master.Close(); err != nil {
		t.Fatal(err)
	}
	emu := vt.NewSafeEmulator(80, 24)
	terminal := &embeddedTerminal{
		emulator: emu, ptmx: master, cmd: &exec.Cmd{}, cancel: func() {},
		dirty: make(chan struct{}, 1), outputDone: make(chan struct{}),
		replyDone: make(chan struct{}), exited: make(chan struct{}),
	}
	close(terminal.exited)
	go func() {
		defer close(terminal.outputDone)
		terminal.applyEmulatorOutput([]byte("\x1b[6n\x1b[6n"))
	}()
	go terminal.copyTerminalReplies()
	select {
	case <-terminal.replyDone:
	case <-time.After(time.Second):
		_ = emu.InputPipe().(io.Closer).Close()
		t.Fatal("reply copier did not reach the closed PTY")
	}
	select {
	case <-terminal.outputDone:
		t.Fatal("second query did not remain blocked without its reply consumer")
	default:
	}
	done := make(chan struct{})
	go func() { _ = terminal.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		// Allow the original failing implementation to finish so the red
		// regression cannot leave a parser or Close goroutine behind.
		_ = emu.InputPipe().(io.Closer).Close()
		<-done
		t.Fatal("Close waited for output before releasing its blocked reply pipe")
	}
}
