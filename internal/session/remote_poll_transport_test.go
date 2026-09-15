package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemotePollTransportInheritedPipes(t *testing.T) {
	dir := t.TempDir()
	// A surviving child models ControlPersist retaining the command's pipes.
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho 'Permission denied (publickey)' >&2\nsleep 2 &\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	r := &SSHRunner{Host: "example.invalid", commandTimeout: 30 * time.Millisecond}
	started := time.Now()
	_, err := r.Run(context.Background(), "list")
	if time.Since(started) > time.Second {
		t.Errorf("inherited pipes delayed return: %v", time.Since(started))
	}
	if err == nil || !strings.Contains(err.Error(), "Permission denied (publickey)") {
		t.Fatalf("wanted captured SSH error, got %v", err)
	}
}

func TestRemotePollTransportCloseDoesNotWait(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ch := newRemoteChannel("test", "test", nil)
	ch.closeFn = func() { <-release }
	ch.up.Store(true)
	done := make(chan struct{})
	go func() { ch.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close waited for transport cleanup")
	}
	if ch.Connected() {
		t.Fatal("closed channel remains connected")
	}
}

func TestRemotePollTransportCloseCancelsDial(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	ch := newRemoteChannel("test", "test", func(ctx context.Context) (io.WriteCloser, io.Reader, func(), error) {
		close(entered)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-release:
		}
		return nil, nil, nil, context.Canceled
	})
	go ch.ensureConnected()
	<-entered
	ch.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel dial")
	}
}

func TestRemotePollTransportBlockedWriteHonorsDeadline(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	ch := newRemoteChannel("test", "test", nil)
	ch.stdin = writer
	ch.closeFn = func() { writer.Close() }
	ch.up.Store(true)
	defer ch.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := ch.Request(ctx, []string{"list"}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write ignored deadline")
	}
}

func TestRemotePollTransportLatencyDeadline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho 'Permission denied (publickey)' >&2\nsleep 2 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (&SSHRunner{Host: "example.invalid"}).MeasureLatency(ctx)
	if time.Since(started) > time.Second {
		t.Errorf("latency probe exceeded bound: %v", time.Since(started))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wanted deadline error, got %v", err)
	}
}

func TestRemotePollTransportDoesNotPrintDiagnostics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho unexpected-output\necho 'Permission denied (publickey)' >&2\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	stdout, err := os.CreateTemp(dir, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(dir, "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	oldOut, oldErr, oldLog := os.Stdout, os.Stderr, sessionLog
	var logs bytes.Buffer
	sessionLog = slog.New(slog.NewTextHandler(&logs, nil))
	os.Stdout, os.Stderr = stdout, stderr
	defer func() { os.Stdout, os.Stderr, sessionLog = oldOut, oldErr, oldLog }()
	r := &SSHRunner{Host: "example.invalid"}
	for _, run := range []func() error{
		func() error { _, err := r.Run(context.Background(), "list"); return err },
		func() error { _, err := r.MeasureLatency(context.Background()); return err },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "Permission denied (publickey)") {
			t.Fatalf("missing captured diagnostic: %v", err)
		}
	}

	// Persistent channel diagnostics follow the same logging-only path.
	_, output, closeChannel, err := r.dialRemoteAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(output)
	closeChannel()
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, f := range []*os.File{stdout, stderr} {
		data, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != 0 {
			t.Errorf("terminal %s received %q", f.Name(), data)
		}
	}
	if strings.Count(logs.String(), "Permission denied (publickey)") != 3 {
		t.Fatalf("default-level logs lost diagnostics: %s", logs.String())
	}
}
