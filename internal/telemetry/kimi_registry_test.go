package telemetry

import "testing"

func TestKimiToolRegistryAppended(t *testing.T) {
	if got := NormalizeTool("kimi"); got != "kimi" {
		t.Fatalf("NormalizeTool(kimi) = %q, want kimi", got)
	}
	if got := ToolBit("kimi"); got != 1<<12 {
		t.Fatalf("ToolBit(kimi) = %d, want %d", got, 1<<12)
	}
	if got := ToolBit("shell"); got != 1<<11 {
		t.Fatalf("ToolBit(shell) = %d, want existing bit %d", got, 1<<11)
	}
}
