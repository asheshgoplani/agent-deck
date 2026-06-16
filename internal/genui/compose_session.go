package genui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// compose_session.go — the REAL composer: route the intent + catalog to an
// existing agent-deck-managed claude session and read back the emitted spec.
//
// THE CRUX (no billed LLM call): there is no API key on this host and
// `claude -p` is forbidden (it bills). So the spec-emitting "LLM" is NOT a new
// API client — it is a session the user already runs. The fleet IS the compute:
// we hand the prompt to a managed session via the agent-deck CLI and parse the
// JSON spec back out. This dogfoods the product (the command center generates
// its own UI using the very sessions it monitors) and respects the no-key rule.
//
// The CLI round-trip is injected (Runner) so the prompt construction and JSON
// extraction are unit-tested with ZERO live session. The default runner shells
// the agent-deck binary; it is NOT exercised in CI (no managed session exists
// there) — the StubComposer is what keeps CI green. Wiring this composer in is
// an operator choice (see the web command's --genui-composer-session flag).

// Runner executes a fully-rendered prompt against a managed session and returns
// the session's raw textual reply. Injected for testability.
type Runner func(ctx context.Context, session, prompt string) (string, error)

// SessionComposer emits specs by prompting a managed agent-deck session.
type SessionComposer struct {
	Session string // the managed session id/title to prompt
	Run     Runner // the round-trip; defaults to CLIRunner when nil
}

// NewSessionComposer builds a SessionComposer for a managed session, using the
// default agent-deck CLI runner.
func NewSessionComposer(session string) *SessionComposer {
	return &SessionComposer{Session: session, Run: CLIRunner}
}

// Name identifies the composer in the compose trace.
func (c *SessionComposer) Name() string { return "session:" + c.Session }

// Mutates reports that this composer has side effects (it sends a prompt to a
// managed session), so the compose endpoint gates it as a mutation (403 in
// --read-only / when web mutations are disabled).
func (c *SessionComposer) Mutates() bool { return true }

// Compose builds the prompt (system + user, with repair context on a retry),
// runs it against the session, and extracts the JSON spec from the reply. The
// returned bytes flow into the SAME ValidateBytes gate as any composer's — a
// malformed or code-bearing reply is rejected there, not trusted here.
func (c *SessionComposer) Compose(ctx context.Context, req ComposeRequest) ([]byte, error) {
	if c.Session == "" {
		return nil, fmt.Errorf("session composer: no session configured")
	}
	run := c.Run
	if run == nil {
		run = CLIRunner
	}
	prompt := buildSessionPrompt(req)
	reply, err := run(ctx, c.Session, prompt)
	if err != nil {
		return nil, fmt.Errorf("session %q: %w", c.Session, err)
	}
	raw, err := extractJSONObject(reply)
	if err != nil {
		return nil, fmt.Errorf("session %q reply: %w", c.Session, err)
	}
	return raw, nil
}

// buildSessionPrompt assembles the system prompt (the contract) and the user
// message (intent + data summary + any repair context) into one prompt string.
func buildSessionPrompt(req ComposeRequest) string {
	var b strings.Builder
	b.WriteString(req.Catalog.SystemPrompt())
	b.WriteString("\n---\n")
	b.WriteString(req.Catalog.UserMessage(req))
	return b.String()
}

// cliPollInterval is how often CLIRunner re-reads the session output while
// waiting for the managed session to answer.
var cliPollInterval = 750 * time.Millisecond

// CLIRunner is the default Runner: it sends the prompt to the session and reads
// the session's reply back, both via the agent-deck CLI. This is the dogfood
// path; it requires a live managed session and is therefore never run in CI.
//
// `agent-deck session send` returns when the prompt is DELIVERED, not when the
// session has answered — reading `session output` immediately would return the
// PRIOR reply. So we snapshot the last response before sending and poll until it
// changes (or the request context deadline hits). NOTE: this detects a changed
// reply, not a correlated one; per-session serialization + a response
// correlation marker is genui-2 — for now wire one composer session per server.
func CLIRunner(ctx context.Context, session, prompt string) (string, error) {
	before, _ := exec.CommandContext(ctx, "agent-deck", "session", "output", session).Output()

	// Deliver the prompt. `-q` keeps the CLI output minimal (exit-code driven).
	send := exec.CommandContext(ctx, "agent-deck", "session", "send", session, prompt, "-q")
	var sendErr bytes.Buffer
	send.Stderr = &sendErr
	if err := send.Run(); err != nil {
		return "", fmt.Errorf("session send failed: %w: %s", err, strings.TrimSpace(sendErr.String()))
	}

	ticker := time.NewTicker(cliPollInterval)
	defer ticker.Stop()
	for {
		out, err := exec.CommandContext(ctx, "agent-deck", "session", "output", session).Output()
		if err != nil {
			return "", fmt.Errorf("session output failed: %w", err)
		}
		if !bytes.Equal(out, before) {
			return string(out), nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("session %q did not respond before deadline: %w", session, ctx.Err())
		case <-ticker.C:
		}
	}
}

// extractJSONObject pulls the first balanced top-level JSON object out of a
// (possibly chatty) reply: it tolerates code fences and surrounding prose, then
// returns the {...} slice. It does NOT validate the spec — that is ValidateBytes'
// job — it only locates the JSON so a model that adds a sentence or a ```json
// fence still round-trips. Brace counting is string-aware so a `}` inside a
// string value does not close the object early.
func extractJSONObject(reply string) ([]byte, error) {
	s := stripCodeFences(reply)
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil, fmt.Errorf("no JSON object found in reply")
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case ch == '\\':
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("unbalanced JSON object in reply")
}

// stripCodeFences removes ```...``` fences (with or without a language tag) so a
// fenced reply still parses. If there is no fence the input is returned as-is.
func stripCodeFences(s string) string {
	idx := strings.Index(s, "```")
	if idx < 0 {
		return s
	}
	rest := s[idx+3:]
	// Drop an optional language tag up to the first newline.
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	if end := strings.Index(rest, "```"); end >= 0 {
		return rest[:end]
	}
	return rest
}
