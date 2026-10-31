package session

import "testing"

func TestKimiToolBinary(t *testing.T) {
	if got := toolBinaries["kimi"]; got != "kimi" {
		t.Fatalf("toolBinaries[kimi] = %q, want kimi", got)
	}
}
