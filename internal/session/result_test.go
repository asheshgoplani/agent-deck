package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSessionResultPrefersJSONAndNeverTranscript(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "RESULTS.md"), []byte("# fallback\nSTATUS: PARTIAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte(strings.Repeat("large", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "RESULT.json"), []byte(`{"verdict":"DONE","next":"ship"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSessionResult(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "DONE" || !strings.Contains(string(got.Result), `"next":"ship"`) || !strings.HasSuffix(got.Source, "RESULT.json") {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestReadSessionResultMarkdownFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "RESULTS.md"), []byte("# fix complete\nnotes\nSTATUS: PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSessionResult(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "PASS" || !strings.Contains(string(got.Result), "fix complete") {
		t.Fatalf("unexpected fallback: %+v", got)
	}
}

func TestReadSessionResultVerdictHeaderFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "RESULTS.md"), []byte("# VERDICT FAIL\n\nfindings\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSessionResult(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "FAIL" {
		t.Fatalf("verdict = %q, want FAIL", got.Verdict)
	}
}

func TestReadSessionResultNotFound(t *testing.T) {
	_, err := ReadSessionResult(t.TempDir())
	if !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("got %v, want ErrResultNotFound", err)
	}
}

func TestResultFileSignalChangesOnWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "RESULT.json")
	if err := os.WriteFile(path, []byte(`{"verdict":"PASS"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	first, ok := resultFileSignal(dir)
	if !ok {
		t.Fatal("valid result was not detected")
	}
	if err := os.WriteFile(path, []byte(`{"verdict":"FAIL"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	second, ok := resultFileSignal(dir)
	if !ok || first == second {
		t.Fatalf("rewrite did not produce a new event signal: %q %q", first, second)
	}
}

func TestFormatCompletionsIncludesResultArtifact(t *testing.T) {
	got := FormatCompletionsForInjection([]TransitionNotificationEvent{{
		Kind: transitionKindResult, ChildSessionID: "child", ChildTitle: "worker",
		DoneStatus: "DONE", DoneSummary: "/work/RESULT.json",
	}})
	if !strings.Contains(got, "DONE") || !strings.Contains(got, "/work/RESULT.json") {
		t.Fatalf("result event did not wake parent with artifact location: %q", got)
	}
}

func TestSessionResultIdentityAdversarialInterleaving(t *testing.T) {
	t.Setenv("AGENT_DECK_HOME", t.TempDir())
	shared := t.TempDir()
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(shared, "RESULT.json"), []byte(`{"verdict":"PASS","value":"`+value+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	capture := func(sessionID, turnID, value string) {
		t.Helper()
		if err := ClaimSessionResultSource(sessionID, shared); err != nil {
			t.Fatal(err)
		}
		write(value)
		if _, err := CaptureSessionResult(ResultIdentity{SessionID: sessionID, TurnID: turnID}, shared); err != nil {
			t.Fatal(err)
		}
	}
	assert := func(sessionID, turnID, value string) {
		t.Helper()
		got := ResolveSessionResult(ResultIdentity{SessionID: sessionID, TurnID: turnID})
		if got.State != ResultStateKnown || !strings.Contains(string(got.Result), `"value":"`+value+`"`) {
			t.Fatalf("%s/%s got %+v, want only %q", sessionID, turnID, got, value)
		}
	}

	capture("session-a", "turn-a1", "a1")
	capture("session-b", "turn-b1", "b1")
	assert("session-a", "turn-a1", "a1")
	assert("session-b", "turn-b1", "b1")
	capture("session-b", "turn-b2", "b2")
	capture("session-a", "turn-a2", "a2")
	assert("session-a", "turn-a2", "a2")
	assert("session-b", "turn-b2", "b2")
	if _, err := CaptureSessionResult(ResultIdentity{SessionID: "session-a", TurnID: "turn-a3"}, shared); !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("unchanged prior-turn source was accepted: %v", err)
	}
	if stale := ResolveSessionResult(ResultIdentity{SessionID: "session-a", TurnID: "turn-a3"}); stale.State != ResultStateUnknown {
		t.Fatalf("stale prior turn surfaced as current: %+v", stale)
	}
	if cross := ResolveSessionResult(ResultIdentity{SessionID: "session-b", TurnID: "turn-a2"}); cross.State != ResultStateUnknown {
		t.Fatalf("cross-session result surfaced: %+v", cross)
	}
}

func TestSessionResultSourceOwnershipMatrix(t *testing.T) {
	t.Setenv("AGENT_DECK_HOME", t.TempDir())
	shared := t.TempDir()
	path := filepath.Join(shared, "RESULT.json")
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	capture := func(sessionID, turnID string) error {
		t.Helper()
		_, err := CaptureSessionResult(ResultIdentity{SessionID: sessionID, TurnID: turnID}, shared)
		return err
	}

	// A1 -> B1 -> A2 with B1 unchanged: A cannot steal B's artifact.
	if err := ClaimSessionResultSource("a", shared); err != nil {
		t.Fatal(err)
	}
	write(`{"value":"a1"}`)
	if err := capture("a", "a1"); err != nil {
		t.Fatal(err)
	}
	if err := ClaimSessionResultSource("b", shared); err != nil {
		t.Fatal(err)
	}
	write(`{"value":"b1"}`)
	if err := capture("b", "b1"); err != nil {
		t.Fatal(err)
	}
	if err := capture("a", "a2"); !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("A2 stole B1: %v", err)
	}

	// A persisted source claim survives a process restart because no in-memory
	// state is required, and a simultaneous contender cannot steal it.
	if err := ClaimSessionResultSource("a", shared); err != nil {
		t.Fatal(err)
	}
	if err := ClaimSessionResultSource("b", shared); !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("contender stole claim: %v", err)
	}
	write(`{"value":"same"}`)
	if err := capture("a", "a3"); !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("conflicted simultaneous production was accepted: %v", err)
	}
	if err := ClaimSessionResultSource("a", shared); err != nil {
		t.Fatal(err)
	}
	write(`{"value":"same"}`)
	if err := capture("a", "a3"); err != nil {
		t.Fatal(err)
	}
	if err := ClaimSessionResultSource("a", shared); err != nil {
		t.Fatal(err)
	}
	write(`{"value":"same"}`)
	if err := capture("a", "a4"); err != nil {
		t.Fatalf("later owned identical bytes rejected: %v", err)
	}

	for name, body := range map[string]string{
		"mismatch":     `{"session_id":"b","turn_id":"b9","value":"foreign"}`,
		"missing-turn": `{"session_id":"a","value":"partial"}`,
	} {
		t.Run(name, func(t *testing.T) {
			write(body)
			if err := capture("a", "a5"); !errors.Is(err, ErrResultNotFound) {
				t.Fatalf("conflicting identity accepted: %v", err)
			}
		})
	}
	write(`{"session_id":"a","turn_id":"a5","value":"labelled"}`)
	if err := capture("a", "a5"); err != nil {
		t.Fatalf("matching embedded identity rejected: %v", err)
	}
}

func TestSessionResultCrashBeforeIdentityCopyKeepsExactTurnBinding(t *testing.T) {
	t.Setenv("AGENT_DECK_HOME", t.TempDir())
	shared := t.TempDir()
	if err := ClaimSessionResultSource("a", shared); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "RESULT.json"), []byte(`{"value":"owned"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	first := ResultIdentity{SessionID: "a", TurnID: "turn-1"}
	target, err := resultArtifactPath(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil { // obstruct the durable file rename
		t.Fatal(err)
	}
	if _, err := CaptureSessionResult(first, shared); err == nil {
		t.Fatal("obstructed identity copy unexpectedly succeeded")
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureSessionResult(ResultIdentity{SessionID: "a", TurnID: "turn-2"}, shared); !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("later turn inherited crash-held source: %v", err)
	}
	if _, err := CaptureSessionResult(first, shared); err != nil {
		t.Fatalf("exact crashed turn could not retry: %v", err)
	}
}

func TestSessionResultMutationDuringCaptureFailsClosed(t *testing.T) {
	t.Setenv("AGENT_DECK_HOME", t.TempDir())
	shared := t.TempDir()
	path := filepath.Join(shared, "RESULT.json")
	if err := ClaimSessionResultSource("a", shared); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"value":"before"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resultSourceCaptureHook = func() { _ = os.WriteFile(path, []byte(`{"value":"between-stat-and-copy"}`), 0o644) }
	t.Cleanup(func() { resultSourceCaptureHook = nil })
	if _, err := CaptureSessionResult(ResultIdentity{SessionID: "a", TurnID: "t1"}, shared); !errors.Is(err, ErrResultNotFound) {
		t.Fatalf("mutated source was promoted: %v", err)
	}
	if got := ResolveSessionResult(ResultIdentity{SessionID: "a", TurnID: "t1"}); got.State != ResultStateUnknown {
		t.Fatalf("mutated source surfaced: %+v", got)
	}
}

func TestUnknownSessionResultJSONIsHonestAndParseable(t *testing.T) {
	got := UnknownSessionResult(ResultIdentity{SessionID: "s", TurnID: "t"})
	data, err := json.Marshal(got)
	if err != nil || !json.Valid(data) {
		t.Fatalf("unknown JSON invalid: %s (%v)", data, err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["state"] != ResultStateUnknown || fields["session_id"] != "s" || fields["turn_id"] != "t" {
		t.Fatalf("dishonest unknown shape: %s", data)
	}
	if _, exists := fields["result"]; exists {
		t.Fatalf("unknown must not invent result: %s", data)
	}
}

func TestResultFormatterParityKnownAndUnknown(t *testing.T) {
	known := SessionResult{State: ResultStateKnown, Result: json.RawMessage(`{"ok":true}`), Verdict: "PASS"}
	if got := FormatSessionResult(known); got != "{\"ok\":true}\nVERDICT: PASS" {
		t.Fatalf("known = %q", got)
	}
	if got := FormatSessionResult(UnknownSessionResult(ResultIdentity{})); got != "no result for current turn" {
		t.Fatalf("unknown = %q", got)
	}
}
