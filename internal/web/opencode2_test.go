package web

import (
	"slices"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestOpenCode2SettingsModelCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	if !slices.Contains(session.PickerToolNames(), "opencode2") || !slices.Contains(session.VisibleToolNames(), "opencode2") {
		t.Fatal("web settings omit the v2 launcher")
	}
	catalog := modelCatalogForPicker(session.PickerToolNames())
	if !slices.Equal(catalog["opencode2"].Models, catalog["opencode"].Models) || len(catalog["opencode2"].Models) == 0 {
		t.Fatal("web picker lacks matching OpenCode 2 model suggestions")
	}
}
