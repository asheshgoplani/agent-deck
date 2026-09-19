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
}
