package main

import (
	"os"
	"os/exec"
	"os/user"
	"strings"
	"testing"
)

// initPathProbeEnv marks the re-executed child below. The child does no work:
// the thing under test is its package init(), which runs before TestMain and
// therefore before testutil.IsolateHome() can redirect HOME.
const initPathProbeEnv = "AGENT_DECK_INIT_PATH_PROBE"

// Package init() must not resolve HOME-derived agent-deck paths.
//
// TestMain isolates HOME+XDG, but Go runs package init first, so anything an
// init() resolves lands on the developer's real ~/.config/agent-deck — the
// exact escape the agentpaths S4 guard (2026-06-04 data-loss incident) exists
// to catch. The guard fails closed, so nothing is read; the damage is to the
// alarm itself. It warns through a sync.Once, so an init-time escape burns the
// one warning every test in this binary shares: the line is then printed on
// every run whether or not a test escaped, and a test that genuinely escapes
// later prints nothing new. An alarm that fires identically in both cases
// cannot report anything.
//
// Re-exec ourselves with the real HOME to put init() back in the situation a
// developer's machine puts it in, and assert it stays quiet.
func TestInitDoesNotResolveRealHomePaths(t *testing.T) {
	if os.Getenv(initPathProbeEnv) != "" {
		return // Child: init() has already run, which is all we needed.
	}

	realHome := ""
	if u, err := user.Current(); err == nil {
		realHome = u.HomeDir
	}
	if realHome == "" {
		t.Skip("cannot determine the real home dir")
	}

	cmd := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$")
	// Hand the child the real HOME with XDG cleared — the resolution a
	// developer running `go test ./cmd/agent-deck/...` actually gets. The
	// child's init must not turn this into an agent-deck path.
	cmd.Env = append(os.Environ(),
		initPathProbeEnv+"=1",
		"HOME="+realHome,
		"XDG_CONFIG_HOME=",
		"XDG_DATA_HOME=",
		"XDG_CACHE_HOME=",
		"XDG_STATE_HOME=",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe child failed: %v\n%s", err, out)
	}

	if strings.Contains(string(out), "under the real home") {
		t.Errorf("package init() resolved an agent-deck path under the real home %q.\n"+
			"init() runs before TestMain's IsolateHome(), so it must not resolve "+
			"HOME-derived paths; move the work into main(), which test binaries never call.\n"+
			"child output:\n%s", realHome, out)
	}
}
