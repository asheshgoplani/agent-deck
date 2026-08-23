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
