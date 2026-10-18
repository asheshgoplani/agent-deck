//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shadowCopyJSON is the part of a shadowed_by entry this test reads; kept
// local so the test also builds (and fails) on a tree without the scan.
type shadowCopyJSON struct {
	Path          string `json:"path"`
	Version       string `json:"version"`
	Installed     string `json:"installed"`
	InstalledPath string `json:"installed_path"`
	Remove        string `json:"remove"`
}

// End to end through the real binary: doctor (text and JSON) reports an
// older agent-deck earlier on a temporary PATH, says nothing in the other
// order, treats a symlink to itself as itself, and never touches the copy.
func TestDoctorReportsShadowingCopyOnPath(t *testing.T) {
	bin := channelsCLIBinary(t)
	root := t.TempDir()
	newDir := filepath.Join(root, "local", "bin")
	oldDir := filepath.Join(root, "usr", "local", "bin")
	for _, d := range []string{newDir, oldDir} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	self := filepath.Join(newDir, "agent-deck")
	require.NoError(t, os.Symlink(bin, self))
	old := filepath.Join(oldDir, "agent-deck")
	require.NoError(t, os.WriteFile(old, []byte("#!/bin/sh\necho 'Agent Deck v1.16.3'\n"), 0o755))

	sysPath := "/usr/bin:/bin"
	home := t.TempDir()
	doctorJSON := func(path string) []shadowCopyJSON {
		t.Helper()
		stdout, stderr, code := runAgentDeckEnv(t, home, "", []string{"PATH=" + path}, "doctor", "--json")
		require.Equal(t, 0, code, "doctor --json: %s %s", stdout, stderr)
		var report map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(stdout), &report), stdout)
		raw, ok := report["shadowed_by"]
		require.True(t, ok, "doctor --json has no shadowed_by list: %s", stdout)
		var copies []shadowCopyJSON
		require.NoError(t, json.Unmarshal(raw, &copies))
		require.NotNil(t, copies, "shadowed_by is a list, never null: %s", stdout)
		return copies
	}

	stdout, _, code := runAgentDeckEnv(t, home, "", []string{"PATH=" + oldDir + ":" + newDir + ":" + sysPath}, "doctor")
	require.Equal(t, 0, code)
	t.Logf("measure: old copy first on PATH: doctor warning lines = %d", strings.Count(stdout, "Warning: "+old))
	assert.Contains(t, stdout, "Warning: "+old+" (v1.16.3) comes before "+self+" (v"+Version+") on PATH")
	assert.Contains(t, stdout, "put "+newDir+" first on PATH")

	got := doctorJSON(oldDir + ":" + newDir + ":" + sysPath)
	t.Logf("measure: old copy first on PATH: shadowed_by entries = %d", len(got))
	require.Len(t, got, 1)
	assert.Equal(t, old, got[0].Path)
	assert.Equal(t, "1.16.3", got[0].Version)
	assert.Equal(t, Version, got[0].Installed)
	assert.Equal(t, self, got[0].InstalledPath)
	assert.Equal(t, "rm "+old, got[0].Remove)

	assert.Empty(t, doctorJSON(newDir+":"+oldDir+":"+sysPath), "updated binary first on PATH")
	stdout, _, _ = runAgentDeckEnv(t, home, "", []string{"PATH=" + newDir + ":" + oldDir + ":" + sysPath}, "doctor")
	assert.Contains(t, stdout, "agent-deck on PATH: OK")

	_, err := os.Stat(old)
	assert.NoError(t, err, "doctor must never remove the shadowing copy")
}
