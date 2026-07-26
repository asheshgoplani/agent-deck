package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handoffFixture is a sandboxed `session handoff` scenario: an isolated HOME +
// CLAUDE_CONFIG_DIR, one seeded instance in its own profile, and a Claude
// transcript on disk for that instance.
type handoffFixture struct {
	profile        string
	instance       *session.Instance
	transcriptPath string
}

func newHandoffFixture(t *testing.T, profile, marker string) *handoffFixture {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", profile)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}

	sessionID := "5c5c5c5c-1234-4567-89ab-cdefcdefcdef"
	transcriptDir := filepath.Join(claudeDir, "projects", session.ConvertToClaudeDirName(project))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(transcriptDir, sessionID+".jsonl")
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"` + marker + `"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"acknowledged"}}`,
	}, "\n")
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &session.Instance{
		ID:              "handoff-inst-1",
		Title:           "handoff-src",
		Tool:            "claude",
		ProjectPath:     project,
		Command:         "claude",
		ClaudeSessionID: sessionID,
	}
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	return &handoffFixture{profile: profile, instance: inst, transcriptPath: transcriptPath}
}

func (f *handoffFixture) run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	err = runSessionHandoff(&out, &errBuf, f.profile, args)
	return out.String(), errBuf.String(), err
}

func requireHandoffError(t *testing.T, err error, wantCode string) *handoffError {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	he, ok := err.(*handoffError)
	if !ok {
		t.Fatalf("error type = %T (%v), want *handoffError", err, err)
	}
	if he.code != wantCode {
		t.Fatalf("error code = %q, want %q (msg %q)", he.code, wantCode, he.msg)
	}
	return he
}

// Issue #1670: the default (no-flag) path must print the prompt on stdout and
// the message accounting on stderr, so the prompt stays pipe-clean.
func TestRunSessionHandoff_PrintsPromptToStdoutAndStatsToStderr(t *testing.T) {
	f := newHandoffFixture(t, "handoff_stdout", "AMBER KEYSTONE")

	stdout, stderr, err := f.run(t, "handoff-src")
	if err != nil {
		t.Fatalf("runSessionHandoff: %v", err)
	}
	if !strings.Contains(stdout, "AMBER KEYSTONE") {
		t.Fatalf("stdout missing transcript content:\n%s", stdout)
	}
	if !strings.Contains(stdout, "--- BEGIN TRANSFERRED TRANSCRIPT ---") {
		t.Fatalf("stdout missing prompt scaffolding:\n%s", stdout)
	}
	if !strings.Contains(stderr, "handoff: 2/2 messages included") {
		t.Fatalf("stderr missing accounting line:\n%s", stderr)
	}
	if !strings.Contains(stderr, f.transcriptPath) {
		t.Fatalf("stderr missing source transcript path:\n%s", stderr)
	}
	// Accounting must not contaminate the prompt on stdout.
	if strings.Contains(stdout, "handoff: 2/2") {
		t.Fatalf("accounting leaked into stdout:\n%s", stdout)
	}
}

// --json must emit a single machine-parseable object carrying both the prompt
// and the HandoffInfo, with no human noise on stdout.
func TestRunSessionHandoff_JSONOutputCarriesPromptAndInfo(t *testing.T) {
	f := newHandoffFixture(t, "handoff_json", "COPPER LATTICE")

	stdout, _, err := f.run(t, "handoff-src", "--json")
	if err != nil {
		t.Fatalf("runSessionHandoff --json: %v", err)
	}

	var payload struct {
		Prompt string              `json:"prompt"`
		Info   session.HandoffInfo `json:"info"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not a single JSON object (%v):\n%s", err, stdout)
	}
	if !strings.Contains(payload.Prompt, "COPPER LATTICE") {
		t.Fatalf("json prompt missing transcript content:\n%s", payload.Prompt)
	}
	if payload.Info.TranscriptPath != f.transcriptPath {
		t.Fatalf("info.transcript_path = %q, want %q", payload.Info.TranscriptPath, f.transcriptPath)
	}
	if payload.Info.MessageCount != 2 || payload.Info.IncludedCount != 2 || payload.Info.Truncated {
		t.Fatalf("info = %+v, want 2/2 untruncated", payload.Info)
	}
	if payload.Info.MaxChars != session.DefaultHandoffMaxChars {
		t.Fatalf("info.max_chars = %d, want the default %d", payload.Info.MaxChars, session.DefaultHandoffMaxChars)
	}
}

// --out writes the prompt to the named file and keeps stdout empty; the
// accounting line still goes to stderr so scripts can log it.
func TestRunSessionHandoff_OutWritesFileAndKeepsStdoutEmpty(t *testing.T) {
	f := newHandoffFixture(t, "handoff_out", "NICKEL BEACON")
	dest := filepath.Join(t.TempDir(), "handoff.txt")

	stdout, stderr, err := f.run(t, "handoff-src", "--out", dest)
	if err != nil {
		t.Fatalf("runSessionHandoff --out: %v", err)
	}
	if stdout != "" {
		t.Fatalf("--out must not print the prompt to stdout, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "handoff: 2/2 messages included") {
		t.Fatalf("stderr missing accounting line:\n%s", stderr)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read --out file: %v", err)
	}
	if !strings.Contains(string(data), "NICKEL BEACON") {
		t.Fatalf("--out file missing transcript content:\n%s", data)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("--out file mode = %v, want 0600 (transcripts are private)", perm)
	}
}

// The --out guard is the data-safety contract: handing off must never clobber
// the source transcript it is reading from, even via a symlink alias.
func TestRunSessionHandoff_OutRefusesToOverwriteSourceTranscript(t *testing.T) {
	f := newHandoffFixture(t, "handoff_out_guard", "TITANIUM LEDGER")

	original, err := os.ReadFile(f.transcriptPath)
	if err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(t.TempDir(), "alias.jsonl")
	if err := os.Symlink(f.transcriptPath, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	for name, target := range map[string]string{
		"direct path": f.transcriptPath,
		"symlink":     alias,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := f.run(t, "handoff-src", "--out", target)
			he := requireHandoffError(t, err, ErrCodeInvalidOperation)
			if !strings.Contains(he.msg, "refuses to overwrite the source transcript") {
				t.Fatalf("error message = %q, want the overwrite refusal", he.msg)
			}
			after, readErr := os.ReadFile(f.transcriptPath)
			if readErr != nil {
				t.Fatalf("source transcript unreadable after refusal: %v", readErr)
			}
			if !bytes.Equal(original, after) {
				t.Fatal("source transcript was modified despite the refusal")
			}
		})
	}
}

// --max-chars must reach the builder (and normalizeArgs must tolerate the flag
// appearing after the positional identifier, which is how people actually type
// it).
func TestRunSessionHandoff_MaxCharsFlagAppliesAfterPositional(t *testing.T) {
	f := newHandoffFixture(t, "handoff_maxchars", "ZINC WATERMARK")

	stdout, _, err := f.run(t, "handoff-src", "--max-chars", "40", "--json")
	if err != nil {
		t.Fatalf("runSessionHandoff: %v", err)
	}
	var payload struct {
		Info session.HandoffInfo `json:"info"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON (%v):\n%s", err, stdout)
	}
	if payload.Info.MaxChars != 40 {
		t.Fatalf("info.max_chars = %d, want 40", payload.Info.MaxChars)
	}
	if !payload.Info.Truncated || payload.Info.IncludedCount != 1 {
		t.Fatalf("info = %+v, want a truncated tail of one message", payload.Info)
	}
}

// An unknown flag must fail with a nonzero-exit error instead of tearing the
// process down inside flag.FlagSet (the reason the seam uses ContinueOnError).
func TestRunSessionHandoff_UnknownFlagReturnsError(t *testing.T) {
	f := newHandoffFixture(t, "handoff_badflag", "LEAD MARKER")

	_, stderr, err := f.run(t, "handoff-src", "--nope")
	requireHandoffError(t, err, ErrCodeInvalidOperation)
	if !strings.Contains(stderr, "Usage: agent-deck session handoff") {
		t.Fatalf("stderr missing usage block:\n%s", stderr)
	}
}

// -h prints usage and succeeds; it must not be reported as a command failure.
func TestRunSessionHandoff_HelpFlagIsNotAnError(t *testing.T) {
	f := newHandoffFixture(t, "handoff_help", "IRON SIGIL")

	_, stderr, err := f.run(t, "-h")
	if err != nil {
		t.Fatalf("-h returned an error: %v", err)
	}
	for _, want := range []string{"Usage: agent-deck session handoff", "-max-chars", "-out", "-json"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("usage output missing %q:\n%s", want, stderr)
		}
	}
}

// An unresolvable identifier must surface NOT_FOUND rather than panicking on a
// nil instance, and must honour --json so wrappers can parse the failure.
func TestRunSessionHandoff_UnknownSessionIsNotFound(t *testing.T) {
	f := newHandoffFixture(t, "handoff_missing", "CHROME TALLY")

	_, _, err := f.run(t, "no-such-session")
	requireHandoffError(t, err, ErrCodeNotFound)

	_, _, err = f.run(t, "no-such-session", "--json")
	he := requireHandoffError(t, err, ErrCodeNotFound)
	if !he.jsonMode {
		t.Fatal("--json failure did not record jsonMode, so the wrapper would print human text")
	}
}

// A session without a Claude transcript must fail with a clear build error that
// names the path searched, not an empty prompt.
func TestRunSessionHandoff_MissingTranscriptReportsBuildFailure(t *testing.T) {
	f := newHandoffFixture(t, "handoff_no_transcript", "STEEL NOTE")
	if err := os.Remove(f.transcriptPath); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := f.run(t, "handoff-src")
	he := requireHandoffError(t, err, ErrCodeInvalidOperation)
	if !strings.Contains(he.msg, "build handoff prompt") {
		t.Fatalf("error message = %q, want the build-failure prefix", he.msg)
	}
	if stdout != "" {
		t.Fatalf("failed handoff still wrote to stdout:\n%s", stdout)
	}
}

// handleSessionHandoff renders failures via errors.As + CLIOutput before
// exiting, so the returned error must survive wrapping and expose its message.
func TestHandoffError_UnwrapsForTheExitWrapper(t *testing.T) {
	var err error = &handoffError{msg: "boom", code: ErrCodeInvalidOperation, jsonMode: true}
	if err.Error() != "boom" {
		t.Fatalf("Error() = %q, want %q", err.Error(), "boom")
	}

	var he *handoffError
	if !errors.As(fmt.Errorf("wrapped: %w", err), &he) {
		t.Fatal("errors.As failed to recover *handoffError through a wrap")
	}
	if he.code != ErrCodeInvalidOperation || !he.jsonMode {
		t.Fatalf("recovered = %+v, want the original code and jsonMode", he)
	}
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical existing paths", real, real, true},
		{"symlink to the same file", link, real, true},
		{"different existing files", real, filepath.Join(dir, "other.txt"), false},
		{"nonexistent but equal after Clean", dir + "/./ghost", dir + "/ghost", true},
		{"nonexistent and different", dir + "/ghost-a", dir + "/ghost-b", false},
		{"empty first operand", "", real, false},
		{"empty second operand", real, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := samePath(tc.a, tc.b); got != tc.want {
				t.Fatalf("samePath(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
