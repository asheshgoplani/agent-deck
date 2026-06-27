package session

// BudgetLevel classifies a session's context-window occupancy against the
// configured absolute-token thresholds. Lower bounds are inclusive.
type BudgetLevel int

const (
	// BudgetNormal: tokens < WarnTokens.
	BudgetNormal BudgetLevel = iota
	// BudgetWarn: WarnTokens <= tokens < HighTokens.
	BudgetWarn
	// BudgetHigh: HighTokens <= tokens < CeilingTokens.
	BudgetHigh
	// BudgetOver: tokens >= CeilingTokens.
	BudgetOver
)

func (l BudgetLevel) String() string {
	switch l {
	case BudgetWarn:
		return "warn"
	case BudgetHigh:
		return "high"
	case BudgetOver:
		return "over"
	default:
		return "normal"
	}
}

// BudgetLevelForTokens maps an absolute context-token count to a BudgetLevel
// using inclusive lower bounds (exactly WarnTokens => warn).
func BudgetLevelForTokens(tokens int, cfg ContextBudgetSettings) BudgetLevel {
	switch {
	case tokens >= cfg.CeilingTokens:
		return BudgetOver
	case tokens >= cfg.HighTokens:
		return BudgetHigh
	case tokens >= cfg.WarnTokens:
		return BudgetWarn
	default:
		return BudgetNormal
	}
}

// BudgetLevel returns the budget level for this session's current context-window
// occupancy. Callers must first confirm a usable token signal exists (Claude-
// compatible tool + non-nil analytics); a zero CurrentContextTokens maps to
// BudgetNormal.
func (a *SessionAnalytics) BudgetLevel(cfg ContextBudgetSettings) BudgetLevel {
	return BudgetLevelForTokens(a.CurrentContextTokens, cfg)
}

// GetContextBudgetSettings loads the user config (cached) and returns the
// context-budget settings with defaults applied. Convenience for UI callers.
func GetContextBudgetSettings() ContextBudgetSettings {
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return (&UserConfig{}).GetContextBudget()
	}
	return cfg.GetContextBudget()
}
