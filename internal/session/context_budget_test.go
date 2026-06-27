package session

import "testing"

func boundaryCfg() ContextBudgetSettings {
	// Use the defaults via GetContextBudget on an empty config.
	return (&UserConfig{}).GetContextBudget()
}

func TestBudgetLevelForTokens_Boundaries(t *testing.T) {
	cfg := boundaryCfg()
	cases := []struct {
		tokens int
		want   BudgetLevel
	}{
		{0, BudgetNormal},
		{149999, BudgetNormal},
		{150000, BudgetWarn},
		{199999, BudgetWarn},
		{200000, BudgetHigh},
		{249999, BudgetHigh},
		{250000, BudgetOver},
		{500000, BudgetOver},
	}
	for _, tc := range cases {
		if got := BudgetLevelForTokens(tc.tokens, cfg); got != tc.want {
			t.Errorf("BudgetLevelForTokens(%d) = %v, want %v", tc.tokens, got, tc.want)
		}
	}
}

func TestSessionAnalytics_BudgetLevel(t *testing.T) {
	cfg := boundaryCfg()
	a := &SessionAnalytics{CurrentContextTokens: 200000}
	if got := a.BudgetLevel(cfg); got != BudgetHigh {
		t.Errorf("BudgetLevel = %v, want BudgetHigh", got)
	}
}
