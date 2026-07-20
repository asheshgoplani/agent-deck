package session

import "testing"

// The chain cap is the only brake on an autonomous fork loop, so an unset
// config must still produce a finite bound rather than zero (which would read
// as "no forks allowed") or unbounded.
func TestGetContextBudget_MaxHandoffChainDefault(t *testing.T) {
	cfg := (&UserConfig{}).GetContextBudget()
	if cfg.MaxHandoffChain != 3 {
		t.Errorf("MaxHandoffChain = %d, want 3", cfg.MaxHandoffChain)
	}
}

func TestGetContextBudget_MaxHandoffChainRespectsExplicit(t *testing.T) {
	c := &UserConfig{}
	c.ContextBudget.MaxHandoffChain = 7
	if got := c.GetContextBudget().MaxHandoffChain; got != 7 {
		t.Errorf("MaxHandoffChain = %d, want 7", got)
	}
}

// With the default cap of 3, a human-started session (generation 0) may produce
// successors at generations 1, 2 and 3 — and the generation-3 session may not
// fork again. Off-by-one here either strangles legitimate work or leaves the
// runaway chain unbounded.
func TestHandoffChainAllows(t *testing.T) {
	cfg := (&UserConfig{}).GetContextBudget() // MaxHandoffChain = 3
	for _, tc := range []struct {
		sourceGeneration int
		want             bool
	}{
		{sourceGeneration: 0, want: true},  // -> generation 1
		{sourceGeneration: 1, want: true},  // -> generation 2
		{sourceGeneration: 2, want: true},  // -> generation 3
		{sourceGeneration: 3, want: false}, // -> generation 4 exceeds the cap
		{sourceGeneration: 9, want: false},
	} {
		if got := HandoffChainAllows(tc.sourceGeneration, cfg); got != tc.want {
			t.Errorf("HandoffChainAllows(gen=%d) = %v, want %v", tc.sourceGeneration, got, tc.want)
		}
	}
}

// An unrecognized tool name would silently map to "shell" at spawn time
// (createSessionTool's default branch), turning a handoff into a dead shell
// pane. Reject it at config load instead.
func TestValidateHandoffTargetTool(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tool    string
		wantErr bool
	}{
		{name: "empty means same tool", tool: "", wantErr: false},
		{name: "builtin codex", tool: "codex", wantErr: false},
		{name: "builtin cursor", tool: "cursor", wantErr: false},
		{name: "builtin claude", tool: "claude", wantErr: false},
		{name: "command with flags degrades to shell", tool: "codex --yolo", wantErr: true},
		{name: "unknown tool", tool: "totally-not-a-tool", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateHandoffTargetTool(tc.tool)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateHandoffTargetTool(%q) = nil, want error", tc.tool)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateHandoffTargetTool(%q) = %v, want nil", tc.tool, err)
			}
		})
	}
}
