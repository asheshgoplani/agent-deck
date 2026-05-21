package statedb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMarshalUnmarshalToolData_MultiRepo(t *testing.T) {
	worktrees := []MultiRepoWorktreeData{
		{OriginalPath: "/a/frontend", WorktreePath: "/a/.worktrees/feature/frontend", RepoRoot: "/a/frontend", Branch: "feature"},
		{OriginalPath: "/b/backend", WorktreePath: "/b/.worktrees/feature/backend", RepoRoot: "/b/backend", Branch: "feature"},
	}

	data := MarshalToolData(
		"", time.Time{}, // claude
		"", time.Time{}, nil, "", // gemini
		"", time.Time{}, // opencode
		"", time.Time{}, // codex
		"", "", nil, nil, // prompt, notes, mcps, toolopts
		nil, "", // sandbox
		"", "", // ssh
		true, []string{"/path/additional1", "/path/additional2"}, // multi-repo
		"/tmp/agent-deck-sessions/abc", worktrees,
		nil,   // channels
		nil,   // extra_args
		nil,   // plugins (RFC docs/rfc/PLUGIN_ATTACH.md)
		false, // plugin_channel_link_disabled
		nil,   // auto_linked_channels (RFC §4.7 G4/C2 fix)
		"",    // color (issue #391)
	)

	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _,
		mrEnabled, addPaths, mrTempDir, mrWorktrees, _, _, _, _, _, _ := UnmarshalToolData(data)

	assert.True(t, mrEnabled)
	assert.Equal(t, []string{"/path/additional1", "/path/additional2"}, addPaths)
	assert.Equal(t, "/tmp/agent-deck-sessions/abc", mrTempDir)
	assert.Len(t, mrWorktrees, 2)
	assert.Equal(t, "/a/frontend", mrWorktrees[0].OriginalPath)
	assert.Equal(t, "/a/.worktrees/feature/frontend", mrWorktrees[0].WorktreePath)
	assert.Equal(t, "feature", mrWorktrees[1].Branch)
}

func TestMarshalUnmarshalToolData_NoMultiRepo(t *testing.T) {
	// Backward compat: no multi-repo fields
	data := MarshalToolData(
		"claude-123", time.Now(),
		"", time.Time{}, nil, "",
		"", time.Time{},
		"", time.Time{},
		"prompt", "notes", []string{"mcp1"}, nil,
		nil, "",
		"", "",
		false, nil, "", nil,
		nil,   // channels
		nil,   // extra_args
		nil,   // plugins (RFC docs/rfc/PLUGIN_ATTACH.md)
		false, // plugin_channel_link_disabled
		nil,   // auto_linked_channels (RFC §4.7 G4/C2 fix)
		"",    // color (issue #391)
	)

	claudeSID, _, _, _, _, _, _, _, _, _, prompt, notes, mcps, _, _, _, _, _,
		mrEnabled, addPaths, mrTempDir, mrWorktrees, _, _, _, _, _, _ := UnmarshalToolData(data)

	assert.Equal(t, "claude-123", claudeSID)
	assert.Equal(t, "prompt", prompt)
	assert.Equal(t, "notes", notes)
	assert.Equal(t, []string{"mcp1"}, mcps)
	assert.False(t, mrEnabled)
	assert.Nil(t, addPaths)
	assert.Empty(t, mrTempDir)
	assert.Nil(t, mrWorktrees)
}

// TestMarshalUnmarshalToolData_Plugins is the Phase 1 persistence guard for
// the new Instance.Plugins field (RFC docs/rfc/PLUGIN_ATTACH.md). The list
// MUST round-trip through MarshalToolData/UnmarshalToolData unchanged so a
// session restart re-applies enabledPlugins[<id>] = true on the next spawn
// of EnsureWorkerScratchConfigDir.
func TestMarshalUnmarshalToolData_Plugins(t *testing.T) {
	data := MarshalToolData(
		"claude-id", time.Now(),
		"", time.Time{}, nil, "",
		"", time.Time{},
		"", time.Time{},
		"", "", nil, nil,
		nil, "",
		"", "",
		false, nil, "", nil,
		nil,                            // channels
		nil,                            // extra_args
		[]string{"octopus", "discord"}, // plugins
		false,                          // plugin_channel_link_disabled
		nil,                            // auto_linked_channels
		"",                             // color
	)

	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _,
		_, _, _, _, _, _, plugins, _, _, _ := UnmarshalToolData(data)

	assert.Equal(t, []string{"octopus", "discord"}, plugins,
		"Plugins must round-trip through MarshalToolData/UnmarshalToolData unchanged — Restart relies on this for enabledPlugins re-application")
}
