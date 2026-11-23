package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeToastPane builds a widthRun whose tmux prints a frame carrying the
// "restarted into" footer toast for the first n captures and a clean frame
// afterwards, the way the real footer does once its 5s auto-dismiss fires.
func fakeToastPane(t *testing.T, n int) (*widthRun, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(root, "count")
	script := "#!/bin/sh\n" +
		"c=$(cat '" + count + "' 2>/dev/null || echo 0)\n" +
		"c=$((c+1)); echo $c > '" + count + "'\n" +
		"if [ $c -le " + strconv.Itoa(n) + " ]; then\n" +
		"  printf 'SESSIONS\\n✓ restarted into v99.0.0 (was v1.0.0) (auto-dismiss in 4s)\\n'\n" +
		"else\n  printf 'SESSIONS\\n'\nfi\n"
	if err := os.WriteFile(filepath.Join(root, "bin", "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &suite{root: root, project: root, ctx: context.Background(), env: []string{"PATH=/usr/bin:/bin"}}
	return &widthRun{s: s, tmuxName: "fixture"}, count
}

// TestWaitRestartToastGoneBlocksUntilDismissed: a frame captured while the
// restart toast is still up differs from a golden captured after it expired,
// so the step must wait for the toast to clear instead of racing its timer.
func TestWaitRestartToastGoneBlocksUntilDismissed(t *testing.T) {
	w, _ := fakeToastPane(t, 3)
	if err := waitRestartToastGone(w, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	pane, err := w.pane()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pane, "restarted into") {
		t.Fatalf("returned while the toast was still on screen:\n%s", pane)
	}
}

func TestWaitRestartToastGoneTimesOutWhenStuck(t *testing.T) {
	w, _ := fakeToastPane(t, 9)
	if err := waitRestartToastGone(w, 300*time.Millisecond); err == nil {
		t.Fatal("a toast that never clears must fail the step, not pass it")
	}
}
