package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Exercise the acceptance test itself with an empty PATH. Missing SSH must fail,
// rather than turn a focused acceptance invocation into a successful skip.
func TestHealthRemoteExecRequiresSSH(t *testing.T) {
	if os.Getenv("AGENT_DECK_HEALTH_MISSING_SSH") == "1" {
		t.Setenv("PATH", t.TempDir())
		TestHealthRemoteExecJSONParity(t)
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestHealthRemoteExecRequiresSSH$", "-test.v")
	cmd.Env = append(sandboxedCLIEnv(t.TempDir()), "AGENT_DECK_HEALTH_MISSING_SSH=1", "AGENTDECK_TEST_CLI_BIN="+channelsCLIBinary(t))
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "health parity requires OpenSSH client") {
		t.Fatalf("missing SSH must fail acceptance explicitly; error=%v\n%s", err, output)
	}
	t.Logf("missing-dependency acceptance failed as required:\n%s", output)
}
