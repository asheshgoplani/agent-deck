package session

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Regression for #2043: all sends begin while another turn is still active.
// Each waiter must bind to its own durable user UUID and return only the nonce
// emitted after that record, never the in-flight turn's tail.
func TestTurnIdentity_InterleavedSendsReturnOwnNonce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	write := func(lines ...string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, line := range lines {
			fmt.Fprintln(f, line)
		}
	}

	write(`{"type":"user","uuid":"inflight-user","message":{"role":"user","content":"old turn"}}`)
	cursor, err := TranscriptCursor(path)
	if err != nil {
		t.Fatal(err)
	}

	prompts := []string{"prompt nonce-A", "prompt nonce-B", "prompt nonce-C"}
	want := []string{"reply nonce-A", "reply nonce-B", "reply nonce-C"}
	type result struct {
		i    int
		text string
		err  error
	}
	results := make(chan result, len(prompts))
	var ready sync.WaitGroup
	ready.Add(len(prompts))
	for i := range prompts {
		go func(i int) {
			ready.Done()
			id, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: prompts[i], Cursor: cursor}, 3*time.Second, time.Millisecond)
			if err != nil {
				results <- result{i: i, err: err}
				return
			}
			resp, err := AwaitTurnResponse(id, 3*time.Second, time.Millisecond)
			if err != nil {
				results <- result{i: i, err: err}
				return
			}
			results <- result{i: i, text: resp.Content}
		}(i)
	}
	ready.Wait()

	// Finish the already-live turn after all waiters have started. This is the
	// output the sentAt implementation incorrectly returned for every send.
	write(`{"type":"assistant","uuid":"inflight-reply","message":{"role":"assistant","content":[{"type":"text","text":"WRONG OLD TAIL"}],"stop_reason":"end_turn"}}`)
	for i := range prompts {
		write(
			fmt.Sprintf(`{"type":"user","uuid":"user-%d","message":{"role":"user","content":%q}}`, i, prompts[i]),
			fmt.Sprintf(`{"type":"assistant","uuid":"assistant-%d","message":{"role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":"end_turn"}}`, i, want[i]),
		)
	}

	for range prompts {
		got := <-results
		if got.err != nil {
			t.Fatalf("send %d: %v", got.i, got.err)
		}
		if got.text != want[got.i] {
			t.Errorf("send %d returned %q, want %q", got.i, got.text, want[got.i])
		}
	}
}

func TestAwaitTurnIdentity_RejectsMissingUUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"mine"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "mine"}, time.Second, time.Millisecond); err == nil {
		t.Fatal("missing UUID was accepted as turn identity")
	}
}

func TestAwaitTurnIdentity_RetainsPartialTrailingRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := `{"type":"user","uuid":"mine","message":{"role":"user","content":"mine"}`
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		id  TurnIdentity
		err error
	}, 1)
	go func() {
		id, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "mine"}, time.Second, time.Millisecond)
		done <- struct {
			id  TurnIdentity
			err error
		}{id, err}
	}()
	time.Sleep(10 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(f, `}`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.id.UUID != "mine" || got.id.StartOffset != int64(len(prefix)+2) {
		t.Fatalf("identity = %+v, want UUID mine at offset %d", got.id, len(prefix)+2)
	}
}

func TestAwaitTurnIdentity_BindsClaudePeerEnvelope(t *testing.T) {
	prompt := "[agent-deck from:slack] ok resume"
	body := peerPromptPrefix + prompt + peerPromptSuffix + " — not typed by your user."
	path := filepath.Join(t.TempDir(), "session.jsonl")
	rows := fmt.Sprintf(
		"%s\n%s\n%s\n",
		fxUser("imitator", body, map[string]any{"turnOrigin": "human", "origin": map[string]any{"kind": "human"}}),
		fxUser("peer-turn", body, map[string]any{"isMeta": true, "turnOrigin": "peer", "origin": map[string]any{"kind": "peer"}, "sessionId": "peer-session"}),
		`{"type":"assistant","uuid":"peer-reply","message":{"role":"assistant","content":[{"type":"text","text":"RIGHT PEER REPLY"}],"stop_reason":"end_turn"}}`,
	)
	if err := os.WriteFile(path, []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}

	id, _, found, err := scanTurnIdentity(TurnQuery{Path: path, Prompt: prompt}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !found || id.UUID != "peer-turn" || id.SessionID != "peer-session" {
		t.Fatalf("identity = %+v, found %v; want the metadata-marked peer turn", id, found)
	}
	response, err := AwaitTurnResponse(id, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "RIGHT PEER REPLY" {
		t.Fatalf("response = %q; want the peer turn's own reply", response.Content)
	}
}

func TestAwaitTurnIdentity_RejectsUntrustedOrDifferentPeerEnvelope(t *testing.T) {
	prompt := "[agent-deck from:slack] ok resume"
	peer := func(inner string, metadata map[string]any) string {
		body := peerPromptPrefix + inner + peerPromptSuffix + " — not typed by your user."
		return fxUser("candidate", body, metadata) + "\n"
	}
	cases := map[string]string{
		"ordinary user imitation": peer(prompt, map[string]any{"turnOrigin": "human", "origin": map[string]any{"kind": "human"}}),
		"missing meta marker":     peer(prompt, map[string]any{"turnOrigin": "peer", "origin": map[string]any{"kind": "peer"}}),
		"different inner prompt":  peer("[agent-deck from:slack] another request", map[string]any{"isMeta": true, "turnOrigin": "peer", "origin": map[string]any{"kind": "peer"}}),
	}
	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(row), 0o600); err != nil {
				t.Fatal(err)
			}
			if TurnAdvanced(TurnQuery{Path: path, Prompt: prompt}) {
				t.Fatal("untrusted or different peer envelope acquired the turn")
			}
		})
	}
}

func TestStreamTranscriptForTurn_SkipsPreviousTurnTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := `{"type":"assistant","uuid":"old","message":{"role":"assistant","content":"WRONG","stop_reason":"end_turn"}}` + "\n" +
		`{"type":"user","uuid":"mine","message":{"role":"user","content":"mine"}}` + "\n"
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	id := TurnIdentity{UUID: "mine", Path: path, StartOffset: int64(len(prefix))}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- StreamTranscriptForTurn(context.Background(), id, "sid", &out, StreamConfig{
			PollInterval: time.Millisecond,
			IdleTimeout:  time.Second,
			CharBudget:   1024,
			ToolBudget:   10,
		})
	}()
	time.Sleep(10 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"type":"assistant","uuid":"own","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"RIGHT"}],"stop_reason":"end_turn"}}`)
	_ = f.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !bytes.Contains([]byte(got), []byte("RIGHT")) || bytes.Contains([]byte(got), []byte("WRONG")) {
		t.Fatalf("turn stream = %s", got)
	}
}
