package tmux

import "testing"

func BenchmarkMonitorWatchingParse(b *testing.B) {
	const pane = "✻ Baked for 13s · 1 monitor still running\n────────────────────\n❯\n────────────────────\n  ⏵⏵ bypass permissions on · 1 monitor · ← for agents"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ParseClaudeBackgroundWork(pane)
	}
}
