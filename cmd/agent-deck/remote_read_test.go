package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestRemoteReadForwarding(t *testing.T) {
	for _, args := range [][]string{
		{"recall", "timeline", "id", "--json", "--tail", "100"},
		{"recall", "timeline", "id", "--json", "--before", "cursor", "--limit=50"},
		{"recall", "follow", "id", "--after", "cursor", "--jsonl", "--status"},
		{"events", "follow", "--jsonl", "--since", "100", "--kind", "session.status"},
		{"session", "send-status", "send-id", "--json"},
	} {
		got, err := remoteCommandArgs(args)
		if err != nil || !reflect.DeepEqual(got, args) {
			t.Fatalf("forward %q: %q %v", args, got, err)
		}
	}
	for _, args := range [][]string{
		{"recall", "follow", "id", "--transcript", "/controller/file"},
		{"recall", "timeline", "id", "--remote", "other"},
		{"events", "publish", "--json"},
		{"events", "follow", "--since"},
		{"events", "follow", "--jsonl=true"},
		{"events", "follow", "unexpected"},
	} {
		if _, err := remoteCommandArgs(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	if isRemoteFollow([]string{"recall", "follow", "--help"}) {
		t.Fatal("help would watch stdin")
	}
	if !isRemoteFollow([]string{"events", "follow", "--jsonl"}) {
		t.Fatal("events not streaming")
	}
}

type fakeRemoteRead struct {
	calls       int
	follow      bool
	stdout      io.Writer
	stderr      io.Writer
	unsupported []byte
}

func (r *fakeRemoteRead) RunReadIO(ctx context.Context, in io.Reader, out, diag io.Writer, unsupported []byte, follow bool, args ...string) error {
	r.calls++
	r.follow = follow
	r.stdout = out
	r.stderr = diag
	r.unsupported = unsupported
	if !follow {
		if _, ok := ctx.Deadline(); !ok {
			panic("unbounded snapshot")
		}
	}
	_, _ = io.WriteString(out, "payload\n")
	return nil
}
func TestRemoteReadSingleRoundTripUnbuffered(t *testing.T) {
	for _, args := range [][]string{{"recall", "follow", "id", "--after", "end", "--jsonl"}, {"events", "follow", "--jsonl", "--since", "123"}, {"session", "send-status", "send", "--json"}, {"recall", "timeline", "id", "--json", "--before", "cursor", "--limit", "10"}} {
		var out, diag bytes.Buffer
		runner := &fakeRemoteRead{}
		code, err := runRemoteReadIO(context.Background(), runner, "remote", nil, &out, &diag, args)
		var obj map[string]string
		if code != 0 || err != nil || runner.calls != 1 || runner.stdout != &out || runner.stderr != &diag || runner.follow != isRemoteFollow(args) {
			t.Fatalf("%q: code=%d err=%v runner=%+v", args, code, err, runner)
		}
		if json.Unmarshal(runner.unsupported, &obj) != nil || !strings.Contains(obj["error"], "unsupported remote command") {
			t.Fatalf("unsupported response %s", runner.unsupported)
		}
	}
}
