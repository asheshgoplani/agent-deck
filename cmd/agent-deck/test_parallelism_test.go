package main

import (
	"flag"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestPackageRunsParallelTestsTwoPerCPU pins the TestMain default from #2797:
// without an explicit -parallel, this package's parallel tests get two slots
// per CPU. They wait on child processes, daemons and timers, so one slot per
// CPU (go test's default) left the parallel phase queued behind those waits
// and the package near go test's 10-minute default timeout.
func TestPackageRunsParallelTestsTwoPerCPU(t *testing.T) {
	// The command line, not flag.Visit: TestMain's own flag.Set marks the
	// flag as set too.
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(strings.TrimLeft(arg, "-"), "test.parallel") {
			t.Skip("-parallel was given explicitly; it overrides the package default")
		}
	}
	f := flag.Lookup("test.parallel")
	if f == nil {
		t.Fatal("test.parallel flag not registered")
	}
	got, err := strconv.Atoi(f.Value.String())
	if err != nil {
		t.Fatalf("test.parallel = %q: %v", f.Value.String(), err)
	}
	if want := 2 * runtime.GOMAXPROCS(0); got < want {
		t.Fatalf("test.parallel = %d, want at least %d (two per CPU): the parallel tests here are wait-bound", got, want)
	}
}
