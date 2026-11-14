package sendqueue

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// childStates maps each child process of this process to its state letter.
func childStates(t *testing.T) map[int]string {
	t.Helper()
	paths, _ := filepath.Glob("/proc/[0-9]*/stat")
	out := map[int]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...; comm may hold spaces and parens.
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		f := strings.Fields(s[i+1:])
		if len(f) < 2 {
			continue
		}
		if ppid, _ := strconv.Atoi(f[1]); ppid == os.Getpid() {
			pid, _ := strconv.Atoi(strings.Fields(s)[0])
			out[pid] = f[0]
		}
	}
	return out
}

// A long-lived caller (the daemon draining a group queue) must not collect a
// zombie per send worker: SpawnWorker reaps the worker when it exits.
func TestSpawnWorkerReapsTheWorker(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	// The worker is this test binary: it rejects -p and exits at once.
	if err := SpawnWorker("reap-test", "reap-target"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		children := childStates(t)
		if len(children) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("send worker not reaped after it exited: children %v", children)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
