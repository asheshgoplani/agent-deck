//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// update --unattended never scans PATH: the timer and a controller's nudge
// run with their own PATH (systemd's or launchd's default, an ssh
// command's), so a scan would warn about the wrong PATH. Even with an
// older agent-deck first on this process's PATH, a successful install
// prints no shadow warning and logs none.
func TestRunUnattendedUpdate_NoPathShadowWarning(t *testing.T) {
	dir := t.TempDir()
	writeFakeAgentDeck(t, dir, "1.16.3")
	t.Setenv("PATH", dir)
	h := newUnattendedHarness(t, availableInfo())
	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.Equal(t, []string{"check", "homebrew", "preflight", "install 1.17.0", "bridge", "hygiene", "remotes 1.17.0"}, h.calls)
	out := h.out.String()
	assert.Contains(t, out, "✓ Updated to v1.17.0")
	assert.NotContains(t, out, "Warning:")
	assert.NotContains(t, out, "on PATH")
}

// update --check --json always carries shadowed_by, an empty list when
// PATH is clean.
func TestUpdateCheckJSON_ShadowedByDefaultsEmpty(t *testing.T) {
	doc := buildUpdateCheckJSON(&update.UpdateInfo{}, session.UpdateSettings{}, update.TimerStatus{}, "", nil, nil)
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"shadowed_by":[]`)
}

func writeFakeAgentDeck(t *testing.T, dir, version string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agent-deck"), []byte("#!/bin/sh\necho 'Agent Deck v"+version+"'\n"), 0o755))
}
