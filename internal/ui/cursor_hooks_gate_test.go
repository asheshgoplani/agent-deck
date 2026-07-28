// cursor_hooks_gate_test.go covers shouldAutoInstallCursorHooks, the TUI
// startup gate for Cursor Agent CLI hook auto-install (issue #1675, follow-up
// to #1673's durable [cursor] hooks_enabled opt-out). The predicate decides
// whether NewHomeWithProfileAndMode should even attempt
// session.AutoInstallCursorHooks and start the shared hook watcher; getting
// it wrong either silently reinstalls hooks after an explicit uninstall
// (regression #1672) or skips auto-install for users who never opted out.
package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestShouldAutoInstallCursorHooks(t *testing.T) {
	disabled := false
	enabled := true

	tests := []struct {
		name           string
		workersEnabled bool
		userConfig     *session.UserConfig
		cursorCmd      string
		want           bool
	}{
		{
			name:           "nil config, workers on, cmd set -> installs",
			workersEnabled: true,
			userConfig:     nil,
			cursorCmd:      "cursor",
			want:           true,
		},
		{
			name:           "zero-value config, workers on, cmd set -> installs",
			workersEnabled: true,
			userConfig:     &session.UserConfig{},
			cursorCmd:      "cursor",
			want:           true,
		},
		{
			name:           "explicit opt-in config -> installs",
			workersEnabled: true,
			userConfig:     &session.UserConfig{Cursor: session.CursorSettings{HooksEnabled: &enabled}},
			cursorCmd:      "cursor",
			want:           true,
		},
		{
			name:           "durable opt-out ([cursor] hooks_enabled = false) -> skipped, even with workers on and a cursor command",
			workersEnabled: true,
			userConfig:     &session.UserConfig{Cursor: session.CursorSettings{HooksEnabled: &disabled}},
			cursorCmd:      "cursor",
			want:           false,
		},
		{
			name:           "background workers disabled (test mode) -> skipped regardless of config",
			workersEnabled: false,
			userConfig:     nil,
			cursorCmd:      "cursor",
			want:           false,
		},
		{
			name:           "no cursor command configured -> skipped",
			workersEnabled: true,
			userConfig:     nil,
			cursorCmd:      "",
			want:           false,
		},
		{
			name:           "opt-out AND no cursor command -> still skipped",
			workersEnabled: true,
			userConfig:     &session.UserConfig{Cursor: session.CursorSettings{HooksEnabled: &disabled}},
			cursorCmd:      "",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := homeBackgroundWorkersEnabled
			homeBackgroundWorkersEnabled = tt.workersEnabled
			t.Cleanup(func() { homeBackgroundWorkersEnabled = orig })

			got := shouldAutoInstallCursorHooks(tt.userConfig, tt.cursorCmd)
			if got != tt.want {
				t.Fatalf("shouldAutoInstallCursorHooks(cfg=%+v, cmd=%q) with workersEnabled=%v = %v, want %v",
					tt.userConfig, tt.cursorCmd, tt.workersEnabled, got, tt.want)
			}
		})
	}
}

// TestShouldAutoInstallCursorHooks_DefaultTestModeSkips is a narrow regression
// guard: package TestMain must leave homeBackgroundWorkersEnabled false so
// ordinary tests never trigger real hook installation as a side effect. If
// this ever flips, every other test in this package that constructs a Home
// silently starts installing Cursor hooks into whatever HOME is active.
func TestShouldAutoInstallCursorHooks_DefaultTestModeSkips(t *testing.T) {
	if homeBackgroundWorkersEnabled {
		t.Fatal("TestMain must disable Home background workers")
	}
	if shouldAutoInstallCursorHooks(nil, "cursor") {
		t.Fatal("shouldAutoInstallCursorHooks() = true with background workers disabled, want false")
	}
}
