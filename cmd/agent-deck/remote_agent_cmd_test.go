package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The agent answers each request with its id and the command's output,
// refuses verbs that must not run over the channel, and pushes "changed"
// when the watched state file is written.
func TestRemoteAgent_RequestsEventsAndDenyList(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	if err := os.WriteFile(db, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	// The listing probe reflects the state file's content, so a write that
	// changes what the TUI would see produces exactly one "changed".
	run := func(ctx context.Context, args []string) (string, string, int) {
		for _, a := range args {
			if a == "--fail" {
				return "", "boom", 3
			}
		}
		if args[0] == "list" {
			content, _ := os.ReadFile(db)
			return "ran:" + strings.Join(args, " ") + ":" + string(content), "", 0
		}
		return "ran:" + strings.Join(args, " "), "", 0
	}
	// The probe is what the change feed compares; here it is the same
	// listing the request path returns, so the two stay in step.
	probe := func() (string, string, error) {
		l, _, _ := run(context.Background(), []string{"list", "--json"})
		g, _, _ := run(context.Background(), []string{"group", "list", "--json"})
		return l, g, nil
	}
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, outW, run, probe, db, 20*time.Millisecond)
		_ = outW.Close()
		close(done)
	}()
	sc := bufio.NewScanner(outR)
	next := func() remoteAgentReply {
		if !sc.Scan() {
			t.Fatalf("agent closed early: %v", sc.Err())
		}
		var r remoteAgentReply
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad reply %q: %v", sc.Text(), err)
		}
		return r
	}
	if r := next(); r.Event != "ready" {
		t.Fatalf("first line must announce ready, got %+v", r)
	}

	send := func(id int64, args ...string) {
		b, _ := json.Marshal(remoteAgentRequest{ID: id, Args: args})
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	send(7, "list", "--json")
	if r := next(); r.ID != 7 || r.Code != 0 || r.Stdout != "ran:list --json:v1" {
		t.Fatalf("list reply = %+v", r)
	}
	send(8, "status", "--fail")
	if r := next(); r.ID != 8 || r.Code != 3 || r.Stderr != "boom" {
		t.Fatalf("failing command reply = %+v", r)
	}
	send(9, "web")
	if r := next(); r.ID != 9 || r.Code != 2 || !strings.Contains(r.Error, "not allowed") {
		t.Fatalf("denied verb reply = %+v", r)
	}

	// A write that does not change the listing (same content, new mtime)
	// produces no event; a write that does produces one.
	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(db, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	send(10, "status", "--noop")
	if r := next(); r.ID != 10 || r.Event != "" {
		t.Fatalf("a content-preserving write must not push an event; got %+v before the noop reply", r)
	}
	if err := os.WriteFile(db, []byte("v2-longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	got := make(chan remoteAgentReply, 1)
	go func() { got <- next() }()
	select {
	case r := <-got:
		if r.Event != "changed" {
			t.Fatalf("expected a changed event, got %+v", r)
		}
	case <-deadline:
		t.Fatal("no changed event after the state file was written")
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit when stdin closed")
	}
}

// The "changed" event reports how long the probe took, and a probe that
// fails still tells the TUI to refetch: an event without listings.
func TestRemoteAgent_ProbeTimingAndFailureFallback(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	if err := os.WriteFile(db, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	run := func(context.Context, []string) (string, string, int) { return "", "", 0 }
	probe := func() (string, string, error) {
		content, _ := os.ReadFile(db)
		if string(content) == "broken" {
			return "", "", errors.New("storage gone")
		}
		time.Sleep(5 * time.Millisecond)
		return "[" + string(content) + "]", "{}", nil
	}
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, outW, run, probe, db, 20*time.Millisecond)
		_ = outW.Close()
		close(done)
	}()
	sc := bufio.NewScanner(outR)
	next := func() remoteAgentReply {
		t.Helper()
		got := make(chan remoteAgentReply, 1)
		go func() {
			if !sc.Scan() {
				t.Errorf("agent closed early: %v", sc.Err())
				got <- remoteAgentReply{}
				return
			}
			var r remoteAgentReply
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				t.Errorf("bad reply %q: %v", sc.Text(), err)
			}
			got <- r
		}()
		select {
		case r := <-got:
			return r
		case <-time.After(2 * time.Second):
			t.Fatal("no line from the agent within 2s")
			return remoteAgentReply{}
		}
	}
	if r := next(); r.Event != "ready" {
		t.Fatalf("first line must announce ready, got %+v", r)
	}

	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(db, []byte("v2-longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := next()
	if r.Event != "changed" || r.Sessions != "[v2-longer]" || r.Groups != "{}" {
		t.Fatalf("expected a changed event with listings, got %+v", r)
	}
	if r.ProbeMS < 5 {
		t.Fatalf("probe_ms = %d, want the probe's duration (>= 5ms)", r.ProbeMS)
	}

	// The feed re-reads the stamp after a probe to absorb the probe's own
	// writes; give it that moment so this write is not taken as seen.
	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(db, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = next()
	if r.Event != "changed" || r.Sessions != "" || r.Groups != "" {
		t.Fatalf("a failing probe must push a bare changed event, got %+v", r)
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit when stdin closed")
	}
}
