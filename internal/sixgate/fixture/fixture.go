// Package fixture materializes the state a SIXGATE driver drives against.
//
// A gate is only as honest as the world it runs in. This package builds that
// world from the repository's own recorded, sanitized corpus
// (internal/ctxinspect/ctxfixture) rather than from whatever happens to be on
// the machine, so a frame captured here means the same thing on a maintainer's
// laptop, in a container and in review three months from now.
//
// Every fixture is materialized into a caller-owned temporary directory and
// every path on the resulting session points inside it. Nothing here reads or
// writes a real home directory: this repository has three times had a run
// resolve agent-deck's live profile out of the real home and destroy it, and
// "the driver does not do that yet" is not a property a later driver preserves.
package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxfixture"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Setup is a materialized world: the sessions a driver will see, where the
// cursor starts, and the environment the product code must resolve paths from.
type Setup struct {
	// Name identifies the fixture in artifacts and in the matrix.
	Name string
	// Description is one line explaining what this world is, reproduced into
	// the transcript so a reader knows what they are looking at.
	Description string
	// Root is the temporary directory everything was materialized into.
	Root string
	// Instances populate the session list, in list order.
	Instances []*session.Instance
	// Cursor is the row the journey starts on.
	Cursor int
	// Env are the variables that must be set for the product's own path
	// resolution to find the materialized tree. A driver applies them to its
	// own process and records them in run.json.
	Env map[string]string
	// Notes record anything a reader needs in order to trust the numbers, for
	// example that a transcript was relocated into Claude's canonical layout.
	Notes []string
}

// CorpusNames returns every recorded case name available as a fixture.
func CorpusNames() ([]string, error) { return ctxfixture.Names() }

// FromCorpus materializes one recorded case into dir and returns the world a
// driver should drive.
//
// The case's transcript is copied into the canonical location the product
// resolves from (<config>/projects/<encoded project path>/<session id>.jsonl)
// rather than being handed to the inspector directly. That is deliberate: the
// point of G1 is to exercise the resolution the user's session actually goes
// through — instance → sessionhost.BuildRequest → adapter — not to inject a
// path past it. A fixture that shortcut the resolver could not have caught a
// bug in the resolver.
func FromCorpus(name, dir string) (*Setup, error) {
	c, err := ctxfixture.Load(name)
	if err != nil {
		return nil, err
	}
	req, err := c.Materialize(dir)
	if err != nil {
		return nil, err
	}

	setup := &Setup{
		Name:        name,
		Description: c.Description,
		Root:        dir,
		Env:         map[string]string{},
	}

	inst := &session.Instance{
		ID:          fixtureInstanceID(name),
		Title:       c.Title,
		Tool:        c.Tool,
		Status:      session.StatusRunning,
		ProjectPath: req.ProjectPath,
		GroupPath:   "fixtures",
		CreatedAt:   ctxfixture.FixedNow,
	}

	switch {
	case session.IsClaudeCompatible(c.Tool):
		inst.ClaudeSessionID = c.SessionID
		setup.Env["CLAUDE_CONFIG_DIR"] = req.ConfigDir
		placed, err := placeClaudeTranscript(req.ConfigDir, req.ProjectPath, c.SessionID, req.TranscriptPath)
		if err != nil {
			return nil, err
		}
		setup.Notes = append(setup.Notes, fmt.Sprintf(
			"transcript copied from the case layout into Claude's canonical path so the product's own resolver finds it: %s", placed))
	case session.IsCodexCompatible(c.Tool):
		inst.CodexSessionID = c.SessionID
		setup.Env["CODEX_HOME"] = req.ConfigDir
		placed, err := placeCodexRollout(req.ConfigDir, c.SessionID, req.TranscriptPath)
		if err != nil {
			return nil, err
		}
		setup.Notes = append(setup.Notes, fmt.Sprintf(
			"rollout copied from the case layout into Codex's canonical path so the product's own resolver finds it: %s", placed))
	default:
		return nil, fmt.Errorf("fixture %q: tool %q has no transcript convention this fixture can materialize", name, c.Tool)
	}

	setup.Instances = []*session.Instance{inst}
	setup.Cursor = 0
	return setup, nil
}

// placeClaudeTranscript copies src into the path
// session.ResolveClaudeTranscriptPath will find, returning that path.
func placeClaudeTranscript(configDir, projectPath, sessionID, src string) (string, error) {
	if strings.TrimSpace(src) == "" {
		return "", fmt.Errorf("fixture: the case declares no transcript, so the report would be projected rather than observed")
	}
	resolved := projectPath
	if r, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolved = r
	}
	destDir := filepath.Join(configDir, "projects", session.ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("fixture: creating the transcript directory: %w", err)
	}
	raw, err := os.ReadFile(src) //nolint:gosec // src is inside the caller-owned fixture root
	if err != nil {
		return "", fmt.Errorf("fixture: reading the case transcript: %w", err)
	}
	dest := filepath.Join(destDir, sessionID+".jsonl")
	if err := os.WriteFile(dest, raw, 0o644); err != nil { //nolint:gosec // fixture tree
		return "", fmt.Errorf("fixture: placing the transcript: %w", err)
	}
	return dest, nil
}

// placeCodexRollout copies src into the path session.CodexRolloutPathForInstance
// will find, returning that path.
//
// THE MATRIX FOUND THIS. Until G3 ran a Codex row, this function did not exist:
// the fixture left the rollout at the case's own layout and a comment asserted
// that the resolver would find it via CODEX_HOME. The first codex frame said
// otherwise, in the product's own words — "no codex rollout found for session id
// … under <CODEX_HOME>" — and every codex row was silently exercising the
// no-rollout projection path while claiming to cover a real Codex session. That
// is the difference between reading code and driving it: the comment was
// plausible, confident and wrong, and only a captured frame said so.
//
// Codex's layout is <CODEX_HOME>/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
// (internal/session/codex_subagent_gate.go: codexRolloutPathInHome). The date
// parts come from the corpus's own pinned timestamp so the path is reproducible.
func placeCodexRollout(configDir, sessionID, src string) (string, error) {
	if strings.TrimSpace(src) == "" {
		return "", fmt.Errorf("fixture: the case declares no rollout, so the report would be projected rather than observed")
	}
	ts := ctxfixture.FixedNow.UTC()
	destDir := filepath.Join(configDir, "sessions",
		ts.Format("2006"), ts.Format("01"), ts.Format("02"))
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("fixture: creating the rollout directory: %w", err)
	}
	raw, err := os.ReadFile(src) //nolint:gosec // src is inside the caller-owned fixture root
	if err != nil {
		return "", fmt.Errorf("fixture: reading the case rollout: %w", err)
	}
	dest := filepath.Join(destDir, fmt.Sprintf("rollout-%s-%s.jsonl", ts.Format("2006-01-02T15-04-05"), sessionID))
	if err := os.WriteFile(dest, raw, 0o644); err != nil { //nolint:gosec // fixture tree
		return "", fmt.Errorf("fixture: placing the rollout: %w", err)
	}
	return dest, nil
}

// fixtureInstanceID produces a stable, obviously-synthetic session id. It is
// stable so two runs of the same fixture produce comparable artifacts, and
// obviously synthetic so nobody mistakes an artifact for a real session.
func fixtureInstanceID(name string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, name)
	return "sixgate-" + safe
}

// Apply sets the fixture's environment on the current process and returns a
// function restoring what was there before.
//
// A driver is a single-purpose process, so mutating its own environment is
// safe; restoring anyway keeps a driver that runs several fixtures in one
// process from leaking one world into the next.
func (s *Setup) Apply() (restore func(), err error) {
	saved := make(map[string]*string, len(s.Env))
	for k := range s.Env {
		if v, ok := os.LookupEnv(k); ok {
			copied := v
			saved[k] = &copied
		} else {
			saved[k] = nil
		}
	}
	for k, v := range s.Env {
		if err := os.Setenv(k, v); err != nil {
			return func() {}, fmt.Errorf("fixture: setting %s: %w", k, err)
		}
	}
	return func() {
		for k, v := range saved {
			if v == nil {
				_ = os.Unsetenv(k)
				continue
			}
			_ = os.Setenv(k, *v)
		}
	}, nil
}

// FixedNow is the timestamp the corpus stamps its reports with, re-exported so
// a driver can record it without importing the corpus package.
var FixedNow = ctxfixture.FixedNow
