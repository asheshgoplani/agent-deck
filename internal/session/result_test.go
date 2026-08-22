package session

import (
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
