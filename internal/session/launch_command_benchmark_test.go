package session

import "testing"

func BenchmarkBuildClaudeExtraFlagsDefault(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("XDG_CONFIG_HOME", "")
	ClearUserConfigCache()
	b.Cleanup(ClearUserConfigCache)
	inst := NewInstanceWithTool("bench-claude", home, "claude")
	opts := &ClaudeOptions{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = inst.buildClaudeExtraFlags(opts)
	}
}

func BenchmarkBuildCodexCommandDefault(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("XDG_CONFIG_HOME", "")
	ClearUserConfigCache()
	b.Cleanup(ClearUserConfigCache)
	inst := NewInstanceWithTool("bench-codex", home, "codex")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = inst.buildCodexCommand("codex")
	}
}
