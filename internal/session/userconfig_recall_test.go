package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// [recall] defaults to off and parses when present; SaveUserConfig omits the
// zero-value section (covered by the omit-zero-sections test's list).
func TestRecallConfig(t *testing.T) {
	var zero UserConfig
	if zero.Recall.GetEnabled() {
		t.Fatal("recall must default to disabled")
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[recall]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var config UserConfig
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !config.Recall.GetEnabled() {
		t.Fatal("[recall] enabled = true did not parse")
	}
	if config.Recall.GetMaxLoadAvg() != DefaultRecallMaxLoadAvg || config.Recall.GetTextTier() != "clipped" ||
		config.Recall.GetKeepMissingDays() != DefaultRecallKeepMissingDays || config.Recall.GetPerSourceMB() != DefaultRecallPerSourceMB {
		t.Fatalf("defaults: %+v", config.Recall)
	}
	if err := os.WriteFile(configPath, []byte("[recall]\nenabled = true\nmax_loadavg = 0\ntext_tier = \"FULL\"\nkeep_missing_days = 7\nper_source_mb = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config = UserConfig{}
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if config.Recall.GetMaxLoadAvg() != 0 || config.Recall.GetTextTier() != "full" || config.Recall.GetKeepMissingDays() != 7 || config.Recall.GetPerSourceMB() != 0 {
		t.Fatalf("overrides: %+v", config.Recall)
	}
}
