package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type resultCLIFixture struct {
	home, shared string
}

func newResultCLIFixture(t *testing.T) *resultCLIFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "ch_support_test")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return &resultCLIFixture{home: home, shared: filepath.Join(home, "shared-project")}
}

func (f *resultCLIFixture) addTurn(t *testing.T, title, conversationID, content string) (string, string) {
	t.Helper()
	id := addTestSession(t, f.home, f.shared, title)
	stdout, stderr, code := runAgentDeck(t, f.home, "session", "set", id, "claude-session-id", conversationID, "--json")
	if code != 0 {
		t.Fatalf("set conversation id (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	dir := seedClaudeProjectDir(t, f.home, f.shared, content)
	seed := filepath.Join(dir, "abc-123.jsonl")
	path := filepath.Join(dir, conversationID+".jsonl")
	if err := os.Rename(seed, path); err != nil {
		t.Fatal(err)
	}
	return id, fmt.Sprintf("jsonl:%d", len(content))
}

func (f *resultCLIFixture) capture(t *testing.T, sessionID, turnID, value, verdict string) {
	t.Helper()
	if err := session.ClaimSessionResultSource(sessionID, f.shared); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"verdict":%q,"value":%q}`, verdict, value)
	if err := os.WriteFile(filepath.Join(f.shared, "RESULT.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CaptureSessionResult(session.ResultIdentity{SessionID: sessionID, TurnID: turnID}, f.shared); err != nil {
		t.Fatal(err)
	}
}

func parseResultCLIJSON(t *testing.T, stdout string) session.SessionResult {
	t.Helper()
	var got session.SessionResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("session result emitted invalid JSON: %v\nstdout: %s", err, stdout)
	}
	return got
}

func TestSessionResultCLIContract(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	f := newResultCLIFixture(t)

	t.Run("known text and JSON have the same verdict", func(t *testing.T) {
		id, turn := f.addTurn(t, "result-known", "known-turn", "known transcript")
		f.capture(t, id, turn, "known-only", "PASS")

		textOut, textErr, textCode := runAgentDeck(t, f.home, "session", "result", id)
		jsonOut, jsonErr, jsonCode := runAgentDeck(t, f.home, "session", "result", id, "--json")
		if textCode != 0 || jsonCode != 0 {
			t.Fatalf("known result codes text=%d json=%d\ntext stderr: %s\njson stderr: %s", textCode, jsonCode, textErr, jsonErr)
		}
		got := parseResultCLIJSON(t, jsonOut)
		if got.State != session.ResultStateKnown || got.Verdict != "PASS" || !strings.Contains(textOut, "VERDICT: "+got.Verdict) {
			t.Fatalf("text/JSON semantic mismatch\ntext: %s\njson: %s", textOut, jsonOut)
		}
	})

	t.Run("unknown JSON preserves current identity", func(t *testing.T) {
		id, turn := f.addTurn(t, "result-unknown", "unknown-turn", "unknown transcript")
		stdout, stderr, code := runAgentDeck(t, f.home, "session", "result", id, "--json")
		if code != 3 {
			t.Fatalf("unknown exit=%d, want 3\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
		got := parseResultCLIJSON(t, stdout)
		if got.State != session.ResultStateUnknown || got.SessionID != id || got.TurnID != turn || len(got.Result) != 0 {
			t.Fatalf("dishonest unknown result: %+v", got)
		}
	})

	t.Run("shared working directory cannot cross-return", func(t *testing.T) {
		aID, aTurn := f.addTurn(t, "result-shared-a", "shared-turn-a", "transcript a")
		bID, bTurn := f.addTurn(t, "result-shared-b", "shared-turn-b", "transcript b has another size")
		f.capture(t, aID, aTurn, "only-a", "PASS")
		f.capture(t, bID, bTurn, "only-b", "FAIL")
		for _, tc := range []struct{ id, want, reject string }{{aID, "only-a", "only-b"}, {bID, "only-b", "only-a"}} {
			stdout, stderr, code := runAgentDeck(t, f.home, "session", "result", tc.id, "--json")
			if code != 0 {
				t.Fatalf("session %s exit=%d stderr=%s", tc.id, code, stderr)
			}
			got := parseResultCLIJSON(t, stdout)
			if !strings.Contains(string(got.Result), tc.want) || strings.Contains(string(got.Result), tc.reject) {
				t.Fatalf("session %s cross-returned result: %s", tc.id, stdout)
			}
		}
	})

	t.Run("stale prior-turn artifact is unknown in text and JSON", func(t *testing.T) {
		id, firstTurn := f.addTurn(t, "result-stale", "stale-turn", "first turn")
		f.capture(t, id, firstTurn, "prior-turn", "PASS")
		transcript := filepath.Join(seedClaudeProjectDir(t, f.home, f.shared, "unused"), "stale-turn.jsonl")
		if err := os.WriteFile(transcript, []byte("second turn is longer"), 0o644); err != nil {
			t.Fatal(err)
		}
		secondTurn := fmt.Sprintf("jsonl:%d", len("second turn is longer"))
		if _, err := session.CaptureSessionResult(session.ResultIdentity{SessionID: id, TurnID: secondTurn}, f.shared); !errors.Is(err, session.ErrResultNotFound) {
			t.Fatalf("unchanged result accepted for new turn: %v", err)
		}

		textOut, textErr, textCode := runAgentDeck(t, f.home, "session", "result", id)
		jsonOut, jsonErr, jsonCode := runAgentDeck(t, f.home, "session", "result", id, "--json")
		if textCode != 3 || jsonCode != 3 || strings.TrimSpace(textOut) != "no result for current turn" {
			t.Fatalf("stale result was not unknown: text=%d %q (%s), json=%d %q (%s)", textCode, textOut, textErr, jsonCode, jsonOut, jsonErr)
		}
		got := parseResultCLIJSON(t, jsonOut)
		if got.State != session.ResultStateUnknown || got.SessionID != id || got.TurnID != secondTurn || len(got.Result) != 0 {
			t.Fatalf("stale JSON invented a result: %+v", got)
		}
	})
}
