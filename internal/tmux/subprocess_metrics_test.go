package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSubprocessMetricsActualExecution(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	before := SubprocessStarts()
	cmd := exec.Command(bin)
	if SubprocessStarts() != before {
		t.Fatal("constructing command counted as subprocess")
	}
	if err := commandRun(cmd); err != nil {
		t.Fatal(err)
	}
	if SubprocessStarts() != before+1 {
		t.Fatal("successful start was not counted")
	}
	if err := commandRun(exec.Command(filepath.Join(t.TempDir(), "tmux"))); err == nil {
		t.Fatal("missing executable started")
	}
	if SubprocessStarts() != before+1 {
		t.Fatal("failed launch counted as subprocess")
	}
}
