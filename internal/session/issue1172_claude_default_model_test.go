package session

// Regression tests for #1172 — configurable default model for Claude sessions.
//
// Before #1023 shipped per-session model selection the new-session dialog had
// no model field. After it, the dialog preselected the first catalog entry
// (claude-sonnet-4-6) for Claude with no way to override it, even though
// [gemini]/[opencode]/[copilot] already exposed a `default_model` key. This
// file pins the config-parse half of the fix: [claude].default_model must
// round-trip through TOML exactly like the other tools' default_model keys.
//
// Reported by @marekaf (agent-deck v1.9.29, confirmed absent in v1.9.31).

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// Happy path: [claude].default_model parses into ClaudeSettings.DefaultModel.
func TestIssue1172_ClaudeDefaultModel_ParsesFromTOML(t *testing.T) {
	const doc = `
[claude]
default_model = "claude-opus-4-7"
`
	var cfg UserConfig
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Claude.DefaultModel != "claude-opus-4-7" {
		t.Fatalf("Claude.DefaultModel = %q, want %q", cfg.Claude.DefaultModel, "claude-opus-4-7")
	}
}

// Regression/boundary: when [claude] omits default_model the field is empty,
// preserving the pre-#1172 behavior (tool falls back to its own default).
func TestIssue1172_ClaudeDefaultModel_EmptyByDefault(t *testing.T) {
	const doc = `
[claude]
dangerous_mode = true
`
	var cfg UserConfig
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Claude.DefaultModel != "" {
		t.Fatalf("Claude.DefaultModel = %q, want empty when unset", cfg.Claude.DefaultModel)
	}
}

// Parity: the Claude key uses the same toml tag spelling as the other tools so
// users get a consistent config surface across [claude]/[gemini]/[opencode]/[copilot].
func TestIssue1172_ClaudeDefaultModel_TagMatchesOtherTools(t *testing.T) {
	const doc = `
[claude]
default_model = "claude-haiku-4-5"
[gemini]
default_model = "gemini-2.5-flash"
[opencode]
default_model = "anthropic/claude-opus-4-7"
[copilot]
default_model = "gpt-5.2"
`
	var cfg UserConfig
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Claude.DefaultModel != "claude-haiku-4-5" {
		t.Fatalf("Claude.DefaultModel = %q, want claude-haiku-4-5", cfg.Claude.DefaultModel)
	}
	// Sanity: the other tools still parse unchanged (no accidental field shadowing).
	if cfg.Gemini.DefaultModel != "gemini-2.5-flash" {
		t.Fatalf("Gemini.DefaultModel = %q, want gemini-2.5-flash", cfg.Gemini.DefaultModel)
	}
	if cfg.OpenCode.DefaultModel != "anthropic/claude-opus-4-7" {
		t.Fatalf("OpenCode.DefaultModel = %q, want anthropic/claude-opus-4-7", cfg.OpenCode.DefaultModel)
	}
	if cfg.Copilot.DefaultModel != "gpt-5.2" {
		t.Fatalf("Copilot.DefaultModel = %q, want gpt-5.2", cfg.Copilot.DefaultModel)
	}
}
