//go:build !windows

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// The post-flip path: an unattended install that succeeds warns about an
// older copy earlier on PATH, compared against the version it installed,
// after the success line and before the remote follow-up.
func TestRunUnattendedUpdate_WarnsAboutShadowingCopy(t *testing.T) {
	h := newUnattendedHarness(t, availableInfo())
	var askedFor string
	h.deps.shadowCheck = func(installed string) []update.ShadowCopy {
		askedFor = installed
		h.calls = append(h.calls, "shadow")
		return []update.ShadowCopy{{Path: "/usr/local/bin/agent-deck", Target: "/opt/dev/agent-deck", Version: "1.16.3", Installed: installed, InstalledPath: "/home/u/.local/bin/agent-deck", InstalledOnPath: true, NeedsSudo: true, Remove: "sudo rm /usr/local/bin/agent-deck", Reorder: `export PATH=/home/u/.local/bin:"$PATH"`}}
	}
	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.Equal(t, "1.17.0", askedFor)
	assert.Equal(t, []string{"check", "homebrew", "preflight", "install 1.17.0", "bridge", "hygiene", "shadow", "remotes 1.17.0"}, h.calls)
	out := h.out.String()
	assert.Contains(t, out, "Warning: /usr/local/bin/agent-deck (v1.16.3, symlink to /opt/dev/agent-deck, not removable without sudo) comes before /home/u/.local/bin/agent-deck (v1.17.0) on PATH")
	assert.Contains(t, out, "Fix: sudo rm /usr/local/bin/agent-deck")
	assert.Less(t, strings.Index(out, "✓ Updated to v1.17.0"), strings.Index(out, "Warning: /usr/local/bin/agent-deck"))

	// Nothing installed: no scan.
	h = newUnattendedHarness(t, &update.UpdateInfo{CurrentVersion: "1.16.5", LatestVersion: "1.16.5"})
	h.deps.shadowCheck = func(string) []update.ShadowCopy { h.calls = append(h.calls, "shadow"); return nil }
	runUnattendedUpdate(h.deps)
	assert.NotContains(t, h.calls, "shadow")
}

// update --check --json always carries shadowed_by, an empty list when
// PATH is clean.
func TestUpdateCheckJSON_ShadowedByDefaultsEmpty(t *testing.T) {
	doc := buildUpdateCheckJSON(&update.UpdateInfo{}, session.UpdateSettings{}, update.TimerStatus{}, "", nil, nil)
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"shadowed_by":[]`)
}
