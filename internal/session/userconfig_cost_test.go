package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestLoadUserConfig_CostsSection(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(configPath, []byte(`
[costs]
currency = "usd"
timezone = "America/New_York"
retention_days = 60

[costs.budgets]
daily_limit = 50.0
weekly_limit = 200.0
monthly_limit = 500.0

[costs.budgets.groups]
backend = { daily_limit = 25.0 }

[costs.budgets.sessions]
"my-session" = { total_limit = 100.0 }

[costs.pricing.overrides]
"claude-sonnet-4-6" = { input_per_mtok = 3.0, output_per_mtok = 15.0 }
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}

	if cfg.Costs.Currency != "usd" {
		t.Errorf("currency = %q, want %q", cfg.Costs.Currency, "usd")
	}
	if cfg.Costs.Timezone != "America/New_York" {
		t.Errorf("timezone = %q, want %q", cfg.Costs.Timezone, "America/New_York")
	}
	if cfg.Costs.RetentionDays != 60 {
		t.Errorf("retention_days = %d, want 60", cfg.Costs.RetentionDays)
	}
	if cfg.Costs.GetRetentionDays() != 60 {
		t.Errorf("GetRetentionDays = %d, want 60", cfg.Costs.GetRetentionDays())
	}
	if cfg.Costs.Budgets.DailyLimit != 50.0 {
		t.Errorf("daily_limit = %f, want 50.0", cfg.Costs.Budgets.DailyLimit)
	}
	if cfg.Costs.Budgets.WeeklyLimit != 200.0 {
		t.Errorf("weekly_limit = %f, want 200.0", cfg.Costs.Budgets.WeeklyLimit)
	}
	if cfg.Costs.Budgets.Groups["backend"].DailyLimit != 25.0 {
		t.Errorf("group backend daily_limit = %f, want 25.0", cfg.Costs.Budgets.Groups["backend"].DailyLimit)
	}
	if cfg.Costs.Budgets.Sessions["my-session"].TotalLimit != 100.0 {
		t.Errorf("session total_limit = %f, want 100.0", cfg.Costs.Budgets.Sessions["my-session"].TotalLimit)
	}
	override, ok := cfg.Costs.Pricing.Overrides["claude-sonnet-4-6"]
	if !ok {
		t.Fatal("missing pricing override for claude-sonnet-4-6")
	}
	if override.InputPerMtok != 3.0 {
		t.Errorf("input_per_mtok = %f, want 3.0", override.InputPerMtok)
	}
	if override.OutputPerMtok != 15.0 {
		t.Errorf("output_per_mtok = %f, want 15.0", override.OutputPerMtok)
	}
}

func TestCostsSettings_Defaults(t *testing.T) {
	var cfg CostsSettings
	if cfg.GetRetentionDays() != 90 {
		t.Errorf("default retention = %d, want 90", cfg.GetRetentionDays())
	}
	if cfg.GetTimezone() != "Local" {
		t.Errorf("default timezone = %q, want Local", cfg.GetTimezone())
	}
}
