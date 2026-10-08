package tmux

import (
	"context"
	"os"
	"testing"
	"time"
)

// BenchmarkNestedAgentCachedDetection measures a real process-identity cache hit.
// Linux uses procfs; macOS uses ps, so Linux timings do not quantify macOS savings.
func BenchmarkNestedAgentCachedDetection(b *testing.B) {
	pid := os.Getpid()
	start, err := processIdentityOf(context.Background(), pid)
	if err != nil {
		b.Fatal(err)
	}
	sess := NewSession("identity-bench", b.TempDir())
	sess.rememberAgent(nestedAgentMatch{Tool: "opencode", PID: pid, StartID: start})
	// Measure identity checks, not detection-entry expiry, even for long runs.
	sess.toolDetectExpiry = time.Duration(1<<63 - 1)
	b.ResetTimer()
	for b.Loop() {
		if _, ok := sess.freshCachedTool(); !ok {
			b.Fatal("identity expired")
		}
	}
}
