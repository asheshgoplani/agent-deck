package main

import (
	"encoding/json"
	"testing"
)

// Top-level help promises `harness ... [--json]`; the bare form used to exit 2 with usage.
func TestHarness_BareJSONListsHarnesses(t *testing.T) {
	cliInboxTestHome(t)
	old := cliExitHook
	cliExitHook = func(code int) { panic(sendExit{code}) }
	t.Cleanup(func() { cliExitHook = old })
	out := captureStdout(t, func() { handleHarness("", []string{"--json"}) })
	if !json.Valid([]byte(out)) {
		t.Fatalf("bare harness --json did not print JSON: %q", out)
	}
}
