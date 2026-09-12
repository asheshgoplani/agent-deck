package tmux

import "testing"

func TestResetFailedReportsCollectedUnit(t *testing.T) {
	const unit = "agentdeck-tmux-accept-scope-1234.scope"

	tests := []struct {
		name   string
		output string
		unit   string
		want   bool
	}{
		{
			name:   "collected exact unit",
			output: "Failed to reset failed state of unit agentdeck-tmux-accept-scope-1234.scope: Unit agentdeck-tmux-accept-scope-1234.scope not loaded.\n",
			unit:   unit,
			want:   true,
		},
		{
			name:   "different unit is not accepted",
			output: "Failed to reset failed state of unit agentdeck-tmux-accept-other.scope: Unit agentdeck-tmux-accept-other.scope not loaded.",
			unit:   unit,
			want:   false,
		},
		{
			name:   "authorization failure containing absent text is fatal",
			output: "Authorization failed while checking whether Unit agentdeck-tmux-accept-scope-1234.scope not loaded.",
			unit:   unit,
			want:   false,
		},
		{
			name:   "manager failure is fatal",
			output: "Failed to reset failed state of unit agentdeck-tmux-accept-scope-1234.scope: Access denied.",
			unit:   unit,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resetFailedReportsCollectedUnit([]byte(tt.output), tt.unit); got != tt.want {
				t.Errorf("resetFailedReportsCollectedUnit(%q, %q) = %t, want %t", tt.output, tt.unit, got, tt.want)
			}
		})
	}
}
