package sessionhost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestBuildRequestRejectsNoInstance(t *testing.T) {
	_, _, err := BuildRequest(nil, nil, RequestOptions{})
	if !errors.Is(err, ErrNoInstance) {
		t.Fatalf("BuildRequest(nil) error = %v, want ErrNoInstance", err)
	}
}

func TestBuildRequestCarriesTheSessionIdentity(t *testing.T) {
	inst := &session.Instance{
		ID:          "abc123def456",
		Title:       "my-project",
		Tool:        "shell",
		ProjectPath: t.TempDir(),
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	req, warnings, err := BuildRequest(inst, nil, RequestOptions{SessionRef: "my-project", Now: now})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("a tool with no transcript convention must not warn, got %v", warnings)
	}
	if req.Tool != "shell" {
		t.Fatalf("Tool = %q", req.Tool)
	}
	if req.ProjectPath != inst.ProjectPath {
		t.Fatalf("ProjectPath = %q, want %q", req.ProjectPath, inst.ProjectPath)
	}
	if req.SessionRef != "my-project" {
		t.Fatalf("SessionRef = %q", req.SessionRef)
	}
	if req.Host == nil {
		t.Fatal("Host must be wired: an unwired request silently degrades every agent-deck-attributed item")
	}
	if !req.Timestamp().Equal(now) {
		t.Fatalf("Timestamp = %v, want %v", req.Timestamp(), now)
	}
}

func TestBuildRequestSessionRefFallsBackThroughTitleToID(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		title    string
		id       string
		want     string
	}{
		{"explicit wins", "typed-ref", "the-title", "the-id", "typed-ref"},
		{"title when nothing was typed", "", "the-title", "the-id", "the-title"},
		{"id when there is no title", "", "", "the-id", "the-id"},
		{"blank when there is nothing", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst := &session.Instance{ID: tc.id, Title: tc.title, Tool: "shell", ProjectPath: t.TempDir()}
			req, _, err := BuildRequest(inst, nil, RequestOptions{SessionRef: tc.explicit})
			if err != nil {
				t.Fatalf("BuildRequest: %v", err)
			}
			if req.SessionRef != tc.want {
				t.Fatalf("SessionRef = %q, want %q", req.SessionRef, tc.want)
			}
		})
	}
}

// A missing transcript is not an error: it is the difference between an
// observed and a projected report, and the warning has to say so in terms the
// user can act on.
func TestBuildRequestWarnsWithoutFailingWhenNoClaudeTranscriptExists(t *testing.T) {
	inst := &session.Instance{
		ID:              "abc123def456",
		Title:           "my-project",
		Tool:            "claude",
		ProjectPath:     t.TempDir(),
		ClaudeSessionID: "11111111-2222-3333-4444-555555555555",
	}

	req, warnings, err := BuildRequest(inst, nil, RequestOptions{})
	if err != nil {
		t.Fatalf("BuildRequest must not fail on a missing transcript: %v", err)
	}
	if req.TranscriptPath != "" {
		t.Fatalf("TranscriptPath = %q, want empty", req.TranscriptPath)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], "projected") {
		t.Fatalf("warning does not explain the consequence: %q", warnings[0])
	}
	if !strings.Contains(warnings[0], inst.ClaudeSessionID) {
		t.Fatalf("warning does not name the id it looked for: %q", warnings[0])
	}
	if req.ConfigDir == "" {
		t.Fatal("ConfigDir must be resolved for a claude session even when no transcript exists")
	}
}

// The per-instance config dir is authoritative. Resolving only against the
// process-wide one misses every session running under an account, conductor,
// group or scratch-home override.
func TestBuildRequestResolvesTheTranscriptUnderThePerInstanceConfigDir(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".claude-alt")
	projectPath := t.TempDir()

	sessionID := "11111111-2222-3333-4444-555555555555"
	dir := filepath.Join(configDir, "projects", session.ConvertToClaudeDirName(projectPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating the transcript dir: %v", err)
	}
	transcript := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("writing the transcript: %v", err)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	inst := &session.Instance{
		ID:              "abc123def456",
		Title:           "my-project",
		Tool:            "claude",
		ProjectPath:     projectPath,
		ClaudeSessionID: sessionID,
	}

	req, warnings, err := BuildRequest(inst, nil, RequestOptions{})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.TranscriptPath == "" {
		t.Fatalf("no transcript resolved; warnings: %v", warnings)
	}
	if filepath.Base(req.TranscriptPath) != sessionID+".jsonl" {
		t.Fatalf("TranscriptPath = %q, want the session's own transcript", req.TranscriptPath)
	}
	if len(warnings) != 0 {
		t.Fatalf("a resolved transcript must not warn, got %v", warnings)
	}
}

// Two live instances sharing one claude_session_id must not both resolve a
// transcript: showing another session's context is worse than showing none
// (#1349/#1400).
func TestBuildRequestRefusesACollidingTranscript(t *testing.T) {
	projectPath := t.TempDir()
	sessionID := "11111111-2222-3333-4444-555555555555"

	mine := &session.Instance{
		ID:              "aaaa1111",
		Title:           "mine",
		Tool:            "claude",
		ProjectPath:     projectPath,
		ClaudeSessionID: sessionID,
		Status:          session.StatusRunning,
	}
	peer := &session.Instance{
		ID:              "bbbb2222",
		Title:           "peer",
		Tool:            "claude",
		ProjectPath:     projectPath,
		ClaudeSessionID: sessionID,
		Status:          session.StatusRunning,
	}

	req, warnings, err := BuildRequest(mine, []*session.Instance{mine, peer}, RequestOptions{})
	if err != nil {
		t.Fatalf("a collision must degrade the report, not fail the call: %v", err)
	}
	if req.TranscriptPath != "" {
		t.Fatalf("a colliding session must not resolve a transcript, got %q", req.TranscriptPath)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "projected") {
		t.Fatalf("the collision was not explained: %v", warnings)
	}
}

func TestBuildRequestResolvesTheCodexRollout(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	sessionID := "01998888-7777-6666-5555-444444444444"
	dir := filepath.Join(codexHome, "sessions", "2026", "07", "28")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating the rollout dir: %v", err)
	}
	rollout := filepath.Join(dir, "rollout-2026-07-28T12-00-00-"+sessionID+".jsonl")
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("writing the rollout: %v", err)
	}

	inst := &session.Instance{
		ID:             "cccc3333",
		Title:          "codex-session",
		Tool:           "codex",
		ProjectPath:    t.TempDir(),
		CodexSessionID: sessionID,
	}

	req, warnings, err := BuildRequest(inst, nil, RequestOptions{})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.TranscriptPath != rollout {
		t.Fatalf("TranscriptPath = %q, want %q (warnings: %v)", req.TranscriptPath, rollout, warnings)
	}
	if req.SessionID != sessionID {
		t.Fatalf("SessionID = %q, want %q", req.SessionID, sessionID)
	}
	if req.ConfigDir == "" {
		t.Fatal("ConfigDir must be the resolved CODEX_HOME")
	}
}

func TestBuildRequestEstimatorDefaultsWhenUnset(t *testing.T) {
	inst := &session.Instance{ID: "abc123def456", Title: "t", Tool: "shell", ProjectPath: t.TempDir()}
	req, _, err := BuildRequest(inst, nil, RequestOptions{})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if len(req.EstimatorOrDefault().Divisors) == 0 {
		t.Fatal("an unset estimator must fall back to the package default")
	}
}

func TestHostImplementsTheEngineInterface(t *testing.T) {
	var _ ctxinspect.Host = New()
	if New().Name() != "session" {
		t.Fatalf("host name = %q, want session", New().Name())
	}
}

// TestBuildRequestPointsAtTheScratchHomeTheSessionActuallyReads is the
// request-level half of the config-dir fix.
//
// agent-deck hands most claude sessions a per-session worker-scratch config
// dir. Its CLAUDE.md, settings.json and skills are what the session loads; the
// account profile's are not. Resolving the profile dir here made a live report
// list two memory files that were not in the window and miss the one that was.
func TestBuildRequestPointsAtTheScratchHomeTheSessionActuallyReads(t *testing.T) {
	projectPath := t.TempDir()
	profile := filepath.Join(t.TempDir(), ".claude-profile")
	t.Setenv("CLAUDE_CONFIG_DIR", profile)

	inst := &session.Instance{
		ID:          "b81d755d-1785346375",
		Title:       "worker",
		Tool:        "claude",
		ProjectPath: projectPath,
	}
	scratch := session.WorkerScratchConfigDirFor(inst.ID)
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatalf("creating the scratch home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })

	req, _, err := BuildRequest(inst, nil, RequestOptions{})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.ConfigDir != scratch {
		t.Fatalf("ConfigDir = %q, want the scratch home %q the process was handed", req.ConfigDir, scratch)
	}
}

// TestBuildRequestWarnsWhenTheConfigDirIsOnlyInferred keeps an inference from
// being presented as an observation. With no explicit config_dir anywhere,
// agent-deck exports nothing and the session inherits its shell's — so the
// inventory below is read from a directory nobody confirmed it opened.
func TestBuildRequestWarnsWhenTheConfigDirIsOnlyInferred(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	inst := &session.Instance{
		ID:          "cccc3333",
		Title:       "ambient",
		Tool:        "claude",
		ProjectPath: t.TempDir(),
	}

	req, warnings, err := BuildRequest(inst, nil, RequestOptions{})
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.ConfigDir == "" {
		t.Fatal("an inferred config dir is still a config dir; the report needs somewhere to read from")
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, "exports no CLAUDE_CONFIG_DIR") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings %v do not say the config dir was inferred rather than observed", warnings)
	}
}
