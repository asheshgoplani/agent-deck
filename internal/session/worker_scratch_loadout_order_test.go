package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A persisted session may predate a declarative group plugin loadout (or be
// loaded before the loadout is reconciled). The spawn wrapper must materialize
// that loadout before it computes the scratch allow-list; otherwise the
// catalog-wide deny pass writes false for every plugin and Claude starts
// without the plugins the group declares.
func TestPrepareWorkerScratchReconcilesGroupPluginsBeforeAllowList(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[plugins.agent-deck]
name = "agent-deck"
source = "agent-deck"

[groups.doozyx.claude]
plugins = ["agent-deck"]
`)
	source := filepath.Join(home, ".claude")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(source, "settings.json"),
		[]byte(`{"enabledPlugins":{"agent-deck@agent-deck":true}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	inst := NewInstanceWithGroupAndTool(
		"loadout-before-scratch",
		t.TempDir(),
		"doozyx/doozyx-apps",
		"claude",
	)
	inst.ID = "loadout-before-scratch"
	inst.Plugins = nil // Simulate a stale persisted session.

	inst.prepareWorkerScratchConfigDirForSpawn()

	if len(inst.Plugins) != 1 || inst.Plugins[0] != "agent-deck" {
		t.Fatalf("group loadout was not reconciled before scratch: %v", inst.Plugins)
	}
	data, err := os.ReadFile(filepath.Join(inst.WorkerScratchConfigDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.EnabledPlugins["agent-deck@agent-deck"] {
		t.Fatalf("group plugin was pinned false in scratch settings: %s", data)
	}
}
