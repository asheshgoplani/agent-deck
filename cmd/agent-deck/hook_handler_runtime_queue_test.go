package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func isolateStopHookRuntimeQueue(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
}

func TestStopHookRuntimeQueueSuccessfulWriteAcknowledges(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-runtime-success"
	if _, err := session.EnqueueRuntimeMessage(id, "deliver once"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := emitStopHookDecision(id, false, &out); err != nil {
		t.Fatal(err)
	}
	if session.RuntimeQueueHasPending(id) {
		t.Fatal("successful full response write left runtime queue pending")
	}
	batch, err := session.StageRuntimeQueue(id)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Token != "" || len(batch.Messages) != 0 {
		t.Fatalf("acknowledged message reappeared: %#v", batch)
	}
	if got := out.String(); !strings.Contains(got, "deliver once") || !strings.HasSuffix(got, "}\n") {
		t.Fatalf("response = %q", got)
	}
}

func TestStopHookRuntimeQueueWriteFailureRedeliversIdentically(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-runtime-write-fail"
	if _, err := session.EnqueueRuntimeMessage(id, "survive write failure"); err != nil {
		t.Fatal(err)
	}

	err := emitStopHookDecision(id, false, stopHookErrWriter{err: errors.New("broken stdout")})
	if err == nil || !strings.Contains(err.Error(), "broken stdout") {
		t.Fatalf("write error = %v", err)
	}
	if !session.RuntimeQueueHasPending(id) {
		t.Fatal("write failure consumed active runtime queue")
	}
	var retry bytes.Buffer
	if err := emitStopHookDecision(id, false, &retry); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(retry.String(), "survive write failure") {
		t.Fatalf("retry response = %q", retry.String())
	}
}

func TestStopHookRuntimeQueueMarshalFailureLeavesQueue(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-runtime-marshal-fail"
	if _, err := session.EnqueueRuntimeMessage(id, "survive marshal failure"); err != nil {
		t.Fatal(err)
	}
	previous := marshalStopHookDecision
	marshalStopHookDecision = func(any) ([]byte, error) { return nil, errors.New("marshal unavailable") }
	t.Cleanup(func() { marshalStopHookDecision = previous })

	err := emitStopHookDecision(id, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "marshal unavailable") {
		t.Fatalf("marshal error = %v", err)
	}
	if !session.RuntimeQueueHasPending(id) {
		t.Fatal("marshal failure consumed active runtime queue")
	}
}

func TestStopHookRuntimeQueueAcknowledgmentFailureSurfaced(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-runtime-ack-fail"
	if _, err := session.EnqueueRuntimeMessage(id, "ack must fail"); err != nil {
		t.Fatal(err)
	}
	writer := writerFunc(func(p []byte) (int, error) {
		if err := session.DiscardRuntimeQueue(id); err != nil {
			return 0, err
		}
		return len(p), nil
	})
	err := emitStopHookDecision(id, false, writer)
	if err == nil || !strings.Contains(err.Error(), "acknowledg") {
		t.Fatalf("acknowledgment error = %v", err)
	}
}

type stopHookErrWriter struct{ err error }

func (w stopHookErrWriter) Write([]byte) (int, error) { return 0, w.err }

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestStopHookArchivedRaceCannotEmitStaleRuntimeQueue(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-runtime-archived-race"
	if _, err := session.EnqueueRuntimeMessage(id, "stale archived work"); err != nil {
		t.Fatal(err)
	}

	discarded := make(chan struct{})
	releaseStop := make(chan struct{})
	go func() {
		if err := session.DiscardRuntimeQueue(id); err != nil {
			t.Errorf("discard: %v", err)
		}
		close(discarded)
		<-releaseStop
	}()
	<-discarded
	var out bytes.Buffer
	err := emitStopHookDecision(id, false, &out)
	close(releaseStop)
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("archived session emitted stale work: %q", out.String())
	}
	if session.RuntimeQueueHasPending(id) {
		t.Fatal("discarded message reappeared")
	}
}
