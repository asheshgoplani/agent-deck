package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestHookHandlerStopEntryPointDeliversAndAcknowledgesRuntimeQueue(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-entrypoint-runtime"
	t.Setenv("AGENTDECK_INSTANCE_ID", id)
	if _, err := session.EnqueueRuntimeMessage(id, "entrypoint delivery"); err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalIn, originalOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = originalIn, originalOut }()
	if _, err := inW.Write([]byte(`{"hook_event_name":"Stop","stop_hook_active":false}`)); err != nil {
		t.Fatal(err)
	}
	_ = inW.Close()
	handleHookHandlerArgs([]string{"--source", "claude"})
	_ = outW.Close()
	out, err := io.ReadAll(outR)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("entrypoint delivery")) {
		t.Fatalf("Stop entrypoint output = %q", out)
	}
	if session.RuntimeQueueHasPending(id) {
		t.Fatal("Stop entrypoint did not acknowledge runtime queue")
	}
}

func TestStopHookRuntimeQueueWriteFailureRedeliversIdentically(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-runtime-write-fail"
	if _, err := session.EnqueueRuntimeMessage(id, "survive write failure"); err != nil {
		t.Fatal(err)
	}

	var attempted bytes.Buffer
	err := emitStopHookDecision(id, false, writerFunc(func(p []byte) (int, error) {
		attempted.Write(p)
		return 0, errors.New("broken stdout")
	}))
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
	if got, want := retry.String(), attempted.String(); got != want {
		t.Fatalf("retry response differs from first attempted line:\n got %q\nwant %q", got, want)
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
		if err := os.WriteFile(session.RuntimeQueuePathFor(id), nil, 0o644); err != nil {
			return 0, err
		}
		return len(p), nil
	})
	err := emitStopHookDecision(id, false, writer)
	if err == nil || !strings.Contains(err.Error(), "acknowledg") {
		t.Fatalf("acknowledgment error = %v", err)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestStopHookArchivedRaceCannotEmitStaleRuntimeQueue(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-runtime-archived-race"
	if _, err := session.EnqueueRuntimeMessage(id, "stale archived work"); err != nil {
		t.Fatal(err)
	}
	if err := session.CommitToInbox(id, session.TransitionNotificationEvent{
		ChildSessionID: "child-preserved", ChildTitle: "preserved child",
		FromStatus: "running", ToStatus: "waiting", LastOutputHash: "archive-race",
		Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	staged := make(chan struct{})
	releaseMarshal := make(chan struct{})
	var releaseMarshalOnce sync.Once
	release := func() { releaseMarshalOnce.Do(func() { close(releaseMarshal) }) }
	defer release()
	previous := marshalStopHookDecision
	var pauseOnce sync.Once
	marshalStopHookDecision = func(v any) ([]byte, error) {
		pauseOnce.Do(func() {
			close(staged)
			<-releaseMarshal
		})
		return previous(v)
	}
	t.Cleanup(func() { marshalStopHookDecision = previous })

	var out bytes.Buffer
	emitErr := make(chan error, 1)
	go func() { emitErr <- emitStopHookDecision(id, false, &out) }()
	<-staged
	if err := session.DiscardRuntimeQueue(id); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-emitErr; err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "child-preserved") || strings.Contains(got, "stale archived work") {
		t.Fatalf("archive race response lost inbox or emitted stale runtime work: %q", got)
	}
	if session.RuntimeQueueHasPending(id) {
		t.Fatal("discarded message reappeared")
	}
}

func TestStopHookRuntimeQueueReplacementTokenSuppressesStaleBatch(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-runtime-replaced-token"
	if _, err := session.EnqueueRuntimeMessage(id, "stale batch"); err != nil {
		t.Fatal(err)
	}

	staged := make(chan struct{})
	releaseMarshal := make(chan struct{})
	var releaseMarshalOnce sync.Once
	release := func() { releaseMarshalOnce.Do(func() { close(releaseMarshal) }) }
	defer release()
	previous := marshalStopHookDecision
	marshalStopHookDecision = func(v any) ([]byte, error) {
		close(staged)
		<-releaseMarshal
		return previous(v)
	}
	t.Cleanup(func() { marshalStopHookDecision = previous })

	var out bytes.Buffer
	emitErr := make(chan error, 1)
	go func() { emitErr <- emitStopHookDecision(id, false, &out) }()
	<-staged
	if err := session.DiscardRuntimeQueue(id); err != nil {
		t.Fatal(err)
	}
	if _, err := session.EnqueueRuntimeMessage(id, "replacement batch"); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-emitErr; err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("replaced token emitted stale response: %q", out.String())
	}
	queued, err := session.PeekRuntimeQueue(id)
	if err != nil || len(queued) != 1 || queued[0].Message != "replacement batch" {
		t.Fatalf("replacement queue = %#v, %v", queued, err)
	}
}

func TestStopHookRuntimeQueueDiscardDuringWriteReturnsInProgress(t *testing.T) {
	isolateStopHookRuntimeQueue(t)
	const id = "handler-runtime-discard-during-write"
	if _, err := session.EnqueueRuntimeMessage(id, "finish current delivery"); err != nil {
		t.Fatal(err)
	}

	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	releasedWrite := false
	defer func() {
		if !releasedWrite {
			close(releaseWrite)
		}
	}()
	var out bytes.Buffer
	writer := writerFunc(func(p []byte) (int, error) {
		close(writeEntered)
		<-releaseWrite
		return out.Write(p)
	})
	emitErr := make(chan error, 1)
	go func() { emitErr <- emitStopHookDecision(id, false, writer) }()
	<-writeEntered
	discardStarted := make(chan struct{})
	discardDone := make(chan error, 1)
	go func() {
		close(discardStarted)
		discardDone <- session.TryDiscardRuntimeQueue(id)
	}()
	<-discardStarted
	select {
	case err := <-discardDone:
		if !errors.Is(err, session.ErrRuntimeQueueDeliveryInProgress) {
			t.Fatalf("discard during writer error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("discard during writer did not return deterministically")
	}
	close(releaseWrite)
	releasedWrite = true
	if err := <-emitErr; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "finish current delivery") {
		t.Fatalf("response = %q", out.String())
	}
}
