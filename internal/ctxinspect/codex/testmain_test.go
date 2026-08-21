package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain isolates the package from the developer's real home directory.
//
// This is mandatory in this repository: several packages resolve agent-deck's
// live profile and config out of $HOME / $XDG_*, and an un-sandboxed test run
// has three times wiped a maintainer's live session index. Nothing here writes
// outside t.TempDir, but the isolation is unconditional so a test added later
// cannot reintroduce the hazard.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ctxinspect-codex-home-")
	if err != nil {
		panic("codex: cannot create isolated HOME for tests: " + err.Error())
	}
	for k, v := range map[string]string{
		"HOME":              dir,
		"XDG_CONFIG_HOME":   filepath.Join(dir, ".config"),
		"XDG_DATA_HOME":     filepath.Join(dir, ".local", "share"),
		"XDG_CACHE_HOME":    filepath.Join(dir, ".cache"),
		"XDG_STATE_HOME":    filepath.Join(dir, ".local", "state"),
		"CODEX_HOME":        filepath.Join(dir, ".codex"),
		"AGENTDECK_PROFILE": "_test",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic("codex: cannot isolate " + k + ": " + err.Error())
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// rolloutBuilder assembles a synthetic rollout file one record at a time, so a
// test states exactly the records it depends on and nothing else.
type rolloutBuilder struct {
	t     *testing.T
	lines []string
}

// newRollout starts a builder.
func newRollout(t *testing.T) *rolloutBuilder {
	t.Helper()
	return &rolloutBuilder{t: t}
}

// add appends one record.
func (b *rolloutBuilder) add(recordType string, payload any) *rolloutBuilder {
	b.t.Helper()
	raw, err := json.Marshal(map[string]any{
		"timestamp": "2026-07-28T11:22:58.805Z",
		"type":      recordType,
		"payload":   payload,
	})
	if err != nil {
		b.t.Fatalf("marshalling a %s record: %v", recordType, err)
	}
	b.lines = append(b.lines, string(raw))
	return b
}

// addRaw appends a verbatim line, for malformed-input tests.
func (b *rolloutBuilder) addRaw(line string) *rolloutBuilder {
	b.lines = append(b.lines, line)
	return b
}

// sessionMeta appends a session_meta record with a base prompt.
func (b *rolloutBuilder) sessionMeta(sessionID, cwd, baseInstructions string) *rolloutBuilder {
	return b.add(recordSessionMeta, map[string]any{
		"session_id":        sessionID,
		"cwd":               cwd,
		"cli_version":       "0.145.0",
		"originator":        "codex-tui",
		"thread_source":     "user",
		"base_instructions": map[string]any{"text": baseInstructions},
	})
}

// message appends a response_item message with one content element per text.
func (b *rolloutBuilder) message(role string, texts ...string) *rolloutBuilder {
	content := make([]map[string]any, 0, len(texts))
	for _, t := range texts {
		content = append(content, map[string]any{"type": "input_text", "text": t})
	}
	return b.add(recordResponseItem, map[string]any{
		"type":    payloadMessage,
		"role":    role,
		"content": content,
	})
}

// userMessageEvent appends the harness's own record of the typed message.
func (b *rolloutBuilder) userMessageEvent(text string) *rolloutBuilder {
	return b.add(recordEventMsg, map[string]any{"type": payloadUserMessage, "message": text})
}

// worldState appends a world_state record.
func (b *rolloutBuilder) worldState(state map[string]any) *rolloutBuilder {
	return b.add(recordWorldState, map[string]any{"full": true, "state": state})
}

// turnContext appends a turn_context record.
func (b *rolloutBuilder) turnContext(model, mode string) *rolloutBuilder {
	return b.add(recordTurnContext, map[string]any{
		"model": model,
		"collaboration_mode": map[string]any{
			"mode":     mode,
			"settings": map[string]any{"developer_instructions": "mode text"},
		},
	})
}

// assistant appends the record that ends the prefix.
func (b *rolloutBuilder) assistant() *rolloutBuilder {
	return b.add(recordResponseItem, map[string]any{"type": payloadReasoning})
}

// tokenCount appends an accounting record. Passing a different total from last
// is how a test states that the rollout's first turn is not the session's.
func (b *rolloutBuilder) tokenCount(last, total, window int) *rolloutBuilder {
	return b.add(recordEventMsg, map[string]any{
		"type": payloadTokenCount,
		"info": map[string]any{
			"total_token_usage":    map[string]any{"input_tokens": total},
			"last_token_usage":     map[string]any{"input_tokens": last},
			"model_context_window": window,
		},
	})
}

// nullTokenCount appends the accounting record older releases write with no
// usage at all.
func (b *rolloutBuilder) nullTokenCount() *rolloutBuilder {
	return b.add(recordEventMsg, map[string]any{"type": payloadTokenCount, "info": nil})
}

// write materialises the rollout and returns its path.
func (b *rolloutBuilder) write() string {
	b.t.Helper()
	path := filepath.Join(b.t.TempDir(), "rollout-test.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(b.lines, "\n")+"\n"), 0o600); err != nil {
		b.t.Fatalf("writing the rollout: %v", err)
	}
	return path
}

// writeFile creates a file under dir, creating parents, and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}
