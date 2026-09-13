package session

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

type sshAttachBarrierWriter struct {
	entered chan struct{}
	release chan struct{}
	bytes   bytes.Buffer
}

func (w *sshAttachBarrierWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.bytes.Write(p)
}

func TestSSHAttachDetachOrdering(t *testing.T) {
	failure := errors.New("SSH transport closed")
	t.Run("buffered_write_completion", func(t *testing.T) {
		writer := &sshAttachBarrierWriter{entered: make(chan struct{}), release: make(chan struct{})}
		input := sshAttachInput{writer: writer}
		done := make(chan bool, 1)
		go func() { detached, _ := input.forward([]byte("prefix\x11discard")); done <- detached }()
		select {
		case <-writer.entered:
		case <-time.After(time.Second):
			close(writer.release)
			t.Fatal("input did not reach writer barrier")
		}
		// The error arrives while prefix forwarding is blocked, before the
		// caller can close its detach-notification channel.
		if got := input.result(failure); got != nil {
			t.Errorf("detected detach reported failure during write: %v", got)
		}
		close(writer.release)
		select {
		case detached := <-done:
			if !detached {
				t.Error("Ctrl+Q was not intercepted")
			}
		case <-time.After(time.Second):
			t.Fatal("input did not finish")
		}
		if got := writer.bytes.String(); got != "prefix" {
			t.Errorf("forwarded %q, want only prefix", got)
		}
	})
	t.Run("both_ready_command_result", func(t *testing.T) {
		var output bytes.Buffer
		input := sshAttachInput{writer: &output}
		detached, err := input.forward([]byte{0x11})
		if err != nil || !detached {
			t.Fatalf("detach = %v, %v", detached, err)
		}
		detachCh := make(chan struct{})
		close(detachCh)
		cmdDone := make(chan error, 1)
		cmdDone <- failure
		// Both events are ready. Force the legal command-completion outcome
		// instead of hoping that select happens to choose it in this run.
		select {
		case <-detachCh:
		default:
			t.Fatal("detach notification not ready")
		}
		if got := input.result(<-cmdDone); got != nil {
			t.Fatalf("intentional detach reported command error: %v", got)
		}
		if output.Len() != 0 {
			t.Fatalf("Ctrl+Q forwarded %q", output.String())
		}
	})
	t.Run("transport_failure_without_detach", func(t *testing.T) {
		input := sshAttachInput{writer: io.Discard}
		if got := input.result(failure); !errors.Is(got, failure) {
			t.Fatalf("transport failure lost: %v", got)
		}
	})
	t.Run("successful_remote_exit", func(t *testing.T) {
		input := sshAttachInput{writer: io.Discard}
		if got := input.result(nil); got != nil {
			t.Fatal(got)
		}
	})
	t.Run("ordinary_input", func(t *testing.T) {
		var output bytes.Buffer
		input := sshAttachInput{writer: &output}
		detached, err := input.forward([]byte("ordinary input"))
		if err != nil || detached || output.String() != "ordinary input" {
			t.Fatalf("forward = %v, %v, %q", detached, err, output.String())
		}
		if got := input.result(failure); !errors.Is(got, failure) {
			t.Fatalf("ordinary input suppressed error: %v", got)
		}
	})
}
