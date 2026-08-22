package main

import (
	"os"
	"os/exec"
	"os/user"
	"strings"
	"testing"
)

// Issue #2012: package init() used to call initUpdateSettings(), which loads
// the user config and therefore resolves an agent-deck path BEFORE TestMain
// can call testutil.IsolateHome(). The agentpaths unsandboxed-test guard then
// fired on every run of this package, which made the guard worthless: a real
// isolation escape printed exactly what a clean run printed.
//
// This test re-executes the test binary with HOME pointed at the OS user's
// real home (the un-isolated state every `go test` starts in) and no tests
// selected, so only package init and TestMain run. The guard must stay
// silent: nothing may resolve an agent-deck path before TestMain isolates.
func TestIssue2012_PackageInitDoesNotResolveRealHome(t *testing.T) {
	if os.Getenv("AGENT_DECK_ISSUE2012_CHILD") != "" {
		t.Skip("child process")
	}
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skip("cannot determine real home directory from OS user database")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	env := []string{"AGENT_DECK_ISSUE2012_CHILD=1"}
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
			"XDG_STATE_HOME", "AGENTDECK_PROFILE", "AGENT_DECK_TEST_HOME_ISOLATED":
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "HOME="+u.HomeDir)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("re-exec of test binary failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "resolved agent-deck path under the real home") {
		t.Fatalf("package init resolved an agent-deck path under the real home before TestMain isolated HOME (issue #2012):\n%s", out)
	}
}
