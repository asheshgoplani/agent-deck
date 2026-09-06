package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeAgent answers requests like the remote agent would, over pipes.
func fakeAgent(t *testing.T, ready bool) (dial func(context.Context) (io.WriteCloser, io.Reader, func(), error), push func(string)) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	write := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = outW.Write(append(b, '\n'))
	}
	go func() {
		if ready {
			write(remoteChannelReply{Event: "ready"})
		} else {
			_, _ = outW.Write([]byte("Unknown command: remote-agent\n"))
			return
		}
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			var req remoteChannelRequest
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			switch req.Args[0] {
			case "fail":
				write(remoteChannelReply{ID: req.ID, Code: 1, Stderr: "no such session"})
			default:
				write(remoteChannelReply{ID: req.ID, Stdout: "out:" + strings.Join(req.Args, " ")})
			}
		}
	}()
	dial = func(context.Context) (io.WriteCloser, io.Reader, func(), error) {
		return inW, outR, func() { _ = inW.Close(); _ = outW.Close() }, nil
	}
	push = func(event string) { write(remoteChannelReply{Event: event}) }
	return dial, push
}

func newTestChannel(dial func(context.Context) (io.WriteCloser, io.Reader, func(), error)) (*RemoteChannel, chan string) {
	events := make(chan string, 8)
	ch := &RemoteChannel{name: "box", dial: dial, events: events, pending: map[int64]chan remoteChannelReply{}, backoff: time.Millisecond}
	return ch, events
}

func TestRemoteChannel_RequestReplyAndEvents(t *testing.T) {
	dial, push := fakeAgent(t, true)
	ch, events := newTestChannel(dial)
	ch.ensureConnected()
	if !ch.Connected() {
		t.Fatal("channel must be up after the agent said ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := ch.Request(ctx, []string{"list", "--json"})
	if err != nil || string(out) != "out:list --json" {
		t.Fatalf("Request = %q, %v", out, err)
	}
	_, err = ch.Request(ctx, []string{"fail"})
	if err == nil || !strings.Contains(err.Error(), "exit status 1: no such session") {
		t.Fatalf("a failing command must surface stderr like ssh exec does, got %v", err)
	}
	if errors.Is(err, errChannelDown) {
		t.Fatal("a command failure is not a transport failure")
	}

	push("changed")
	select {
	case name := <-events:
		if name != "box" {
			t.Fatalf("event for %q, want box", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pushed change did not reach the events channel")
	}
}

func TestRemoteChannel_OldRemoteIsUnsupportedAndFallsBack(t *testing.T) {
	dial, _ := fakeAgent(t, false)
	ch, _ := newTestChannel(dial)
	ch.ensureConnected()
	if ch.Connected() {
		t.Fatal("a remote that does not know remote-agent must not count as connected")
	}
	if time.Until(ch.unsupportedUntil) < time.Minute {
		t.Fatal("an unsupported remote must not be redialled immediately")
	}
	_, err := ch.Request(context.Background(), []string{"list"})
	if !errors.Is(err, errChannelDown) {
		t.Fatalf("Request on a down channel must report errChannelDown so the caller execs instead, got %v", err)
	}
}
