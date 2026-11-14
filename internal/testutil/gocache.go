package testutil

import (
	"os"
	"os/exec"
	"strings"
)

// PinGoCaches exports GOCACHE and GOMODCACHE with the values the go command
// resolves for the current (real) HOME, unless the caller already set them.
//
// Call it from TestMain BEFORE IsolateHome. Neither variable is set on a
// GitHub runner (setup-go reads `go env GOCACHE`, it does not export it), so
// both default to paths under $HOME. Once IsolateHome moves HOME to a temp
// dir, every `go build` a test runs (the shared agent-deck binary of the
// session, tuitest and releasetests packages) sees an empty build cache and
// an empty module cache: it downloads every module and recompiles the whole
// dependency graph, about a minute of CPU per build on a CI runner. Pinned,
// those builds reuse the runner's restored caches and each other's work.
//
// The caches hold only Go build output and downloaded modules, never
// agent-deck state, so pinning them does not weaken the HOME isolation.
func PinGoCaches() {
	names := []string{"GOCACHE", "GOMODCACHE"}
	out, err := exec.Command("go", append([]string{"env"}, names...)...).Output()
	if err != nil {
		return
	}
	values := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i, name := range names {
		if i < len(values) && os.Getenv(name) == "" && strings.TrimSpace(values[i]) != "" {
			_ = os.Setenv(name, strings.TrimSpace(values[i]))
		}
	}
}
