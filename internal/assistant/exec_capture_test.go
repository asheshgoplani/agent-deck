package assistant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDeck writes a stand-in agent-deck that prints n bytes (a JSON array
// opener followed by spaces) to stdout and exits 0.
func fakeDeck(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-deck")
	script := fmt.Sprintf("#!/bin/sh\nprintf '['\nhead -c %d /dev/zero | tr '\\000' ' '\n", n-1)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Output over the capture limit is an error that says so, never a cut-off
// prefix reported as success.
func TestBinaryExecutorFailsWhenStdoutOverflows(t *testing.T) {
	out, err := (BinaryExecutor{Path: fakeDeck(t, maxCapture+1)}).Run(context.Background(), "", []string{"list", "--json"}, nil)
	if err == nil || !strings.Contains(err.Error(), "output was cut off") {
		t.Fatalf("Run with %d bytes of stdout: %d bytes, err %v; want an error saying the output was cut off", maxCapture+1, len(out), err)
	}
	// Exactly at the limit is fine.
	out, err = (BinaryExecutor{Path: fakeDeck(t, maxCapture)}).Run(context.Background(), "", []string{"list", "--json"}, nil)
	if err != nil || len(out) != maxCapture {
		t.Fatalf("Run with exactly %d bytes: %d bytes, err %v", maxCapture, len(out), err)
	}
}

// list_sessions tells the assistant the list was too large, instead of a
// JSON parse error on a cut-off array.
func TestListSessionsSaysWhenOutputWasCutOff(t *testing.T) {
	svc := NewServiceWithLimits(NewStore(t.TempDir()), BinaryExecutor{Path: fakeDeck(t, maxCapture+4096)}, "test", DefaultLimits)
	rec, err := svc.Authenticate(issue(t, svc, "bot"))
	if err != nil {
		t.Fatal(err)
	}
	r := svc.Call(context.Background(), rec, ToolListSessions, nil, TransportAPI)
	if r.Err == nil || r.Err.Code != CodeFailed || !strings.Contains(r.Err.Message, "output was cut off") {
		t.Fatalf("list_sessions over the capture limit: %+v; want command_failed saying the output was cut off", r)
	}
}
