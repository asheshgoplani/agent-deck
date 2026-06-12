package session

// Bonus regression: [claude].default_model parsed fine (issue #1172) but was
// never APPLIED at spawn. NewClaudeOptions — the factory used when a session is
// launched without per-session options (CLI / programmatic / resume paths in
// instance.go) — read every other Claude default but silently dropped
// default_model, so `--model` was never passed and Claude fell back to Opus even
// with `[claude] default_model = "fable"` set. OpenCode and Copilot already
// wired their default_model in their factories; this pins Claude to parity.

import "testing"

func TestNewClaudeOptions_AppliesDefaultModel(t *testing.T) {
	cfg := &UserConfig{}
	cfg.Claude.DefaultModel = "fable"

	opts := NewClaudeOptions(cfg)
	if opts == nil {
		t.Fatal("NewClaudeOptions returned nil")
	}
	if opts.Model != "fable" {
		t.Fatalf("NewClaudeOptions did not apply [claude].default_model: opts.Model = %q, want %q", opts.Model, "fable")
	}
}

// Boundary: no configured default leaves Model empty, so Claude keeps using its
// own built-in default (no bogus --model flag).
func TestNewClaudeOptions_EmptyDefaultModelStaysEmpty(t *testing.T) {
	opts := NewClaudeOptions(&UserConfig{})
	if opts.Model != "" {
		t.Fatalf("opts.Model = %q, want empty when [claude].default_model is unset", opts.Model)
	}
}
