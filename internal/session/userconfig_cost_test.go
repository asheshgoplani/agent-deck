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

func TestLoadUserConfig_CostLineTemplate_Global(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(configPath, []byte(`
[costs]
cost_line_template = "{cost_today} today"
cost_line_hide_when_zero = false
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	if cfg.Costs.CostLineTemplate == nil {
		t.Fatal("CostLineTemplate is nil, want non-nil")
	}
	if *cfg.Costs.CostLineTemplate != "{cost_today} today" {
		t.Errorf("CostLineTemplate = %q, want %q", *cfg.Costs.CostLineTemplate, "{cost_today} today")
	}
	if cfg.Costs.CostLineHideWhenZero == nil {
		t.Fatal("CostLineHideWhenZero is nil, want non-nil")
	}
	if *cfg.Costs.CostLineHideWhenZero != false {
		t.Errorf("CostLineHideWhenZero = %v, want false", *cfg.Costs.CostLineHideWhenZero)
	}
}

func TestLoadUserConfig_CostLineTemplate_AbsentLeavesNil(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(configPath, []byte(`
[costs]
currency = "usd"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	if cfg.Costs.CostLineTemplate != nil {
		t.Errorf("CostLineTemplate = %v, want nil (unset)", cfg.Costs.CostLineTemplate)
	}
	if cfg.Costs.CostLineHideWhenZero != nil {
		t.Errorf("CostLineHideWhenZero = %v, want nil (unset)", cfg.Costs.CostLineHideWhenZero)
	}
}

func TestLoadUserConfig_CostLineTemplate_ProfileOverride(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(configPath, []byte(`
[costs]
cost_line_template = "{cost_today} today"

[profiles.work.costs]
cost_line_template = "{cost_today} today | {cost_this_week} wk"
cost_line_hide_when_zero = true
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	work, ok := cfg.Profiles["work"]
	if !ok {
		t.Fatal("Profiles[work] missing")
	}
	if work.Costs == nil {
		t.Fatal("Profiles[work].Costs is nil, want non-nil block")
	}
	if work.Costs.CostLineTemplate == nil {
		t.Fatal("Profiles[work].Costs.CostLineTemplate is nil, want set")
	}
	if got, want := *work.Costs.CostLineTemplate, "{cost_today} today | {cost_this_week} wk"; got != want {
		t.Errorf("profile template = %q, want %q", got, want)
	}
	if work.Costs.CostLineHideWhenZero == nil || *work.Costs.CostLineHideWhenZero != true {
		t.Errorf("profile hide_when_zero = %v, want true", work.Costs.CostLineHideWhenZero)
	}
}

func TestLoadUserConfig_CostLineTemplate_ProfileBlockAbsent(t *testing.T) {
	// A profile that has [profiles.X.claude] but no [profiles.X.costs] should
	// have a nil Costs pointer so the resolver can fall through to global.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(configPath, []byte(`
[profiles.work.claude]
config_dir = "~/.claude-work"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	var cfg UserConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	work, ok := cfg.Profiles["work"]
	if !ok {
		t.Fatal("Profiles[work] missing")
	}
	if work.Costs != nil {
		t.Errorf("Profiles[work].Costs = %+v, want nil (no [costs] block)", work.Costs)
	}
}

func TestResolveCostLineTemplate_NilConfig(t *testing.T) {
	tmpl, hide := ResolveCostLineTemplate(nil, "any")
	if tmpl != "{cost_today} today" {
		t.Errorf("nil cfg: template = %q, want hardcoded default", tmpl)
	}
	if !hide {
		t.Errorf("nil cfg: hide_when_zero = %v, want true", hide)
	}
}

func TestResolveCostLineTemplate_EmptyConfig(t *testing.T) {
	cfg := &UserConfig{}
	tmpl, hide := ResolveCostLineTemplate(cfg, "default")
	if tmpl != "{cost_today} today" {
		t.Errorf("empty cfg: template = %q, want hardcoded default", tmpl)
	}
	if !hide {
		t.Errorf("empty cfg: hide_when_zero = %v, want true", hide)
	}
}

func TestResolveCostLineTemplate_GlobalTemplate(t *testing.T) {
	g := "{cost_yesterday} yda"
	cfg := &UserConfig{Costs: CostsSettings{CostLineTemplate: &g}}
	tmpl, _ := ResolveCostLineTemplate(cfg, "default")
	if tmpl != g {
		t.Errorf("template = %q, want %q", tmpl, g)
	}
}

func TestResolveCostLineTemplate_GlobalHideWhenZeroFalse(t *testing.T) {
	f := false
	cfg := &UserConfig{Costs: CostsSettings{CostLineHideWhenZero: &f}}
	_, hide := ResolveCostLineTemplate(cfg, "default")
	if hide {
		t.Errorf("hide_when_zero = %v, want false", hide)
	}
}

func TestResolveCostLineTemplate_ProfileOverridesGlobal(t *testing.T) {
	g := "G"
	p := "P"
	cfg := &UserConfig{
		Costs: CostsSettings{CostLineTemplate: &g},
		Profiles: map[string]ProfileSettings{
			"work": {Costs: &ProfileCosts{CostLineTemplate: &p}},
		},
	}
	tmpl, _ := ResolveCostLineTemplate(cfg, "work")
	if tmpl != p {
		t.Errorf("work profile: template = %q, want %q", tmpl, p)
	}
}

func TestResolveCostLineTemplate_ProfileHideOverridesGlobal(t *testing.T) {
	gTrue := true
	pFalse := false
	cfg := &UserConfig{
		Costs: CostsSettings{CostLineHideWhenZero: &gTrue},
		Profiles: map[string]ProfileSettings{
			"work": {Costs: &ProfileCosts{CostLineHideWhenZero: &pFalse}},
		},
	}
	_, hide := ResolveCostLineTemplate(cfg, "work")
	if hide {
		t.Errorf("work profile: hide_when_zero = %v, want false", hide)
	}
}

func TestResolveCostLineTemplate_ProfileEmptyTemplateDisables(t *testing.T) {
	g := "global"
	empty := ""
	cfg := &UserConfig{
		Costs: CostsSettings{CostLineTemplate: &g},
		Profiles: map[string]ProfileSettings{
			"work": {Costs: &ProfileCosts{CostLineTemplate: &empty}},
		},
	}
	tmpl, _ := ResolveCostLineTemplate(cfg, "work")
	if tmpl != "" {
		t.Errorf("work profile explicit empty: template = %q, want empty (disabled)", tmpl)
	}
}

func TestResolveCostLineTemplate_GlobalEmptyTemplateDisables(t *testing.T) {
	empty := ""
	cfg := &UserConfig{Costs: CostsSettings{CostLineTemplate: &empty}}
	tmpl, _ := ResolveCostLineTemplate(cfg, "default")
	if tmpl != "" {
		t.Errorf("global explicit empty: template = %q, want empty (disabled)", tmpl)
	}
}

func TestResolveCostLineTemplate_ProfileNilCostsFallsThrough(t *testing.T) {
	g := "global"
	cfg := &UserConfig{
		Costs: CostsSettings{CostLineTemplate: &g},
		Profiles: map[string]ProfileSettings{
			"work": {}, // Costs is nil
		},
	}
	tmpl, _ := ResolveCostLineTemplate(cfg, "work")
	if tmpl != g {
		t.Errorf("nil profile.Costs: template = %q, want %q (global)", tmpl, g)
	}
}

func TestResolveCostLineTemplate_ProfileNilTemplateFallsThrough(t *testing.T) {
	// Profile has a Costs block but only sets hide_when_zero, not template.
	g := "global"
	pHide := false
	cfg := &UserConfig{
		Costs: CostsSettings{CostLineTemplate: &g},
		Profiles: map[string]ProfileSettings{
			"work": {Costs: &ProfileCosts{CostLineHideWhenZero: &pHide}},
		},
	}
	tmpl, hide := ResolveCostLineTemplate(cfg, "work")
	if tmpl != g {
		t.Errorf("template = %q, want %q (fall through to global)", tmpl, g)
	}
	if hide {
		t.Errorf("hide_when_zero = %v, want false (profile)", hide)
	}
}

func TestResolveCostLineTemplate_UnknownProfile(t *testing.T) {
	g := "global"
	cfg := &UserConfig{
		Costs:    CostsSettings{CostLineTemplate: &g},
		Profiles: map[string]ProfileSettings{},
	}
	tmpl, _ := ResolveCostLineTemplate(cfg, "nonexistent")
	if tmpl != g {
		t.Errorf("unknown profile: template = %q, want %q (global)", tmpl, g)
	}
}

func TestUserConfig_CostLineTemplate_RoundTrip(t *testing.T) {
	tmpl := "{cost_today} today | {cost_this_week} wk"
	hide := true

	src := UserConfig{
		Costs: CostsSettings{
			CostLineTemplate:     &tmpl,
			CostLineHideWhenZero: &hide,
		},
		Profiles: map[string]ProfileSettings{
			"work": {Costs: &ProfileCosts{
				CostLineTemplate: &tmpl,
			}},
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "out.toml")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.NewEncoder(f).Encode(&src); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	var dst UserConfig
	if _, err := toml.DecodeFile(path, &dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dst.Costs.CostLineTemplate == nil || *dst.Costs.CostLineTemplate != tmpl {
		t.Errorf("global template round-trip lost: got %v, want %q", dst.Costs.CostLineTemplate, tmpl)
	}
	if dst.Costs.CostLineHideWhenZero == nil || *dst.Costs.CostLineHideWhenZero != hide {
		t.Errorf("global hide_when_zero round-trip lost: got %v, want true", dst.Costs.CostLineHideWhenZero)
	}
	work, ok := dst.Profiles["work"]
	if !ok {
		t.Fatal("Profiles[work] lost on round-trip")
	}
	if work.Costs == nil || work.Costs.CostLineTemplate == nil || *work.Costs.CostLineTemplate != tmpl {
		t.Errorf("profile template round-trip lost: got %+v, want %q", work.Costs, tmpl)
	}
}
