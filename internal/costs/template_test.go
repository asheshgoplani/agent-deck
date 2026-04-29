package costs_test

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestRenderCostLine(t *testing.T) {
	vars := map[string]int64{
		"cost_today":      50_000,    // $0.05
		"cost_yesterday":  1_000_000, // $1.00
		"cost_this_week":  0,         // $0.00
		"cost_last_week":  2_500_000, // $2.50
		"cost_this_month": 0,         // $0.00
	}

	tests := []struct {
		name         string
		template     string
		hideWhenZero bool
		want         string
	}{
		{
			name:     "empty template",
			template: "",
			want:     "",
		},
		{
			name:     "single known var",
			template: "{cost_today}",
			want:     "$0.05",
		},
		{
			name:     "known var with literal suffix",
			template: "{cost_today} today",
			want:     "$0.05 today",
		},
		{
			name:     "multiple known vars with separator",
			template: "{cost_today} today | {cost_last_week} last wk",
			want:     "$0.05 today | $2.50 last wk",
		},
		{
			name:     "unknown var passes through literally",
			template: "{cost_galaxy}",
			want:     "{cost_galaxy}",
		},
		{
			name:     "known plus unknown",
			template: "{cost_today} today | {cost_galaxy}",
			want:     "$0.05 today | {cost_galaxy}",
		},
		{
			name:     "adjacent known vars",
			template: "{cost_today}{cost_yesterday}",
			want:     "$0.05$1.00",
		},
		{
			name:     "unclosed brace at end is literal",
			template: "{cost_today",
			want:     "{cost_today",
		},
		{
			name:     "literal with no placeholders is preserved",
			template: "static text",
			want:     "static text",
		},

		// hide_when_zero == true cases
		{
			name:         "hide-when-zero hides when single var is zero",
			template:     "{cost_this_week} wk",
			hideWhenZero: true,
			want:         "",
		},
		{
			name:         "hide-when-zero hides when all known vars are zero",
			template:     "{cost_this_week} wk | {cost_this_month} mo",
			hideWhenZero: true,
			want:         "",
		},
		{
			name:         "hide-when-zero shows when at least one var is non-zero",
			template:     "{cost_this_week} wk | {cost_today} today",
			hideWhenZero: true,
			want:         "$0.00 wk | $0.05 today",
		},
		{
			name:         "hide-when-zero hides template with only literal text (no recognized vars)",
			template:     "static text",
			hideWhenZero: true,
			want:         "",
		},
		{
			name:         "hide-when-zero hides template with only unknown vars",
			template:     "{cost_galaxy}",
			hideWhenZero: true,
			want:         "",
		},
		{
			name:         "hide-when-zero hides empty template",
			template:     "",
			hideWhenZero: true,
			want:         "",
		},

		// hide_when_zero == false cases
		{
			name:         "show-zero shows literal-only template",
			template:     "static text",
			hideWhenZero: false,
			want:         "static text",
		},
		{
			name:         "show-zero shows zero-valued var as $0.00",
			template:     "{cost_this_week} wk",
			hideWhenZero: false,
			want:         "$0.00 wk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := costs.RenderCostLine(tt.template, vars, tt.hideWhenZero)
			if got != tt.want {
				t.Errorf("RenderCostLine(%q) = %q, want %q", tt.template, got, tt.want)
			}
		})
	}
}

func TestRenderCostLine_NilVars(t *testing.T) {
	// Nil vars map: every placeholder is unknown.
	got := costs.RenderCostLine("{cost_today} today", nil, false)
	if got != "{cost_today} today" {
		t.Errorf("nil vars: got %q, want %q", got, "{cost_today} today")
	}
}

func TestRenderCostLine_HideWhenZero_NilVars(t *testing.T) {
	// Nil vars + hideWhenZero: zero recognized vars renders, hide.
	got := costs.RenderCostLine("{cost_today} today", nil, true)
	if got != "" {
		t.Errorf("nil vars hideWhenZero: got %q, want empty", got)
	}
}
