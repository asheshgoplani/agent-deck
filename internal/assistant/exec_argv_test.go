package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestBuildArgvBoundsTheAllocation(t *testing.T) {
	at := make([]string, maxExecArgs)
	for i := range at {
		at[i] = "a"
	}
	got, err := buildArgv("work", at)
	if err != nil {
		t.Fatalf("buildArgv with exactly %d args: %v", maxExecArgs, err)
	}
	if len(got) != maxExecArgs+2 || got[0] != "-p" || got[1] != "work" {
		t.Fatalf("argv = %d items starting %q, want -p work + %d args", len(got), got[:2], maxExecArgs)
	}
	if got, err := buildArgv("", []string{"list", "--json"}); err != nil || strings.Join(got, " ") != "list --json" {
		t.Fatalf("argv without profile = %q, %v", got, err)
	}

	over := append(at, "one-too-many")
	if _, err := buildArgv("work", over); err == nil || !strings.Contains(err.Error(), "too many arguments") {
		t.Fatalf("buildArgv with %d args: err = %v, want too many arguments", len(over), err)
	}
	// Run refuses before starting anything.
	if _, err := (BinaryExecutor{Path: "/nonexistent/agent-deck"}).Run(context.Background(), "", over, nil); err == nil || !strings.Contains(err.Error(), "too many arguments") {
		t.Fatalf("Run with %d args: err = %v, want too many arguments", len(over), err)
	}
}
