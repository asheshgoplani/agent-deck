package session

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestUpdateStatus_ArchivedStaysStopped(t *testing.T) {
	tests := []struct {
		name            string
		archived        bool
		tmuxSession     *tmux.Session
		status          Status
		added           bool
		fresh           bool
		recentlyChecked bool
		exitCode        int
		wantExitCode    bool
		wantStatus      Status
		wantAuthCheck   bool
	}{
		{
			name:       "archived with no tmux object",
			archived:   true,
			status:     StatusRunning,
			wantStatus: StatusStopped,
		},
		{
			name:         "archived with nonexistent tmux pane",
			archived:     true,
			tmuxSession:  &tmux.Session{Name: "archived-status-missing-pane"},
			status:       StatusRunning,
			exitCode:     1,
			wantExitCode: true,
			wantStatus:   StatusStopped,
		},
		{
			name:       "fresh archived with no tmux object",
			archived:   true,
			fresh:      true,
			status:     StatusError,
			wantStatus: StatusStopped,
		},
		{
			name:         "fresh archived with nonexistent tmux pane",
			archived:     true,
			fresh:        true,
			tmuxSession:  &tmux.Session{Name: "fresh-archived-status-missing-pane"},
			status:       StatusError,
			exitCode:     1,
			wantExitCode: true,
			wantStatus:   StatusStopped,
		},
		{
			name:       "fresh stopped archive with no tmux object",
			archived:   true,
			fresh:      true,
			status:     StatusStopped,
			wantStatus: StatusStopped,
		},
		{
			name:         "fresh stopped archive with nonexistent tmux pane",
			archived:     true,
			fresh:        true,
			tmuxSession:  &tmux.Session{Name: "fresh-stopped-archive-missing-pane"},
			status:       StatusStopped,
			exitCode:     1,
			wantExitCode: true,
			wantStatus:   StatusStopped,
		},
		{
			name:            "archived error with recently checked missing tmux pane",
			archived:        true,
			recentlyChecked: true,
			tmuxSession:     &tmux.Session{Name: "recently-checked-archive-missing-pane"},
			status:          StatusError,
			exitCode:        1,
			wantExitCode:    true,
			wantStatus:      StatusStopped,
		},
		{
			name:       "active never started",
			status:     StatusIdle,
			added:      true,
			wantStatus: StatusIdle,
		},
		{
			name:          "active clean exit",
			tmuxSession:   &tmux.Session{Name: "active-clean-exit-missing-pane"},
			status:        StatusRunning,
			exitCode:      0,
			wantExitCode:  true,
			wantStatus:    StatusStopped,
			wantAuthCheck: true,
		},
		{
			name:          "active error exit",
			tmuxSession:   &tmux.Session{Name: "active-error-exit-missing-pane"},
			status:        StatusRunning,
			exitCode:      1,
			wantExitCode:  true,
			wantStatus:    StatusError,
			wantAuthCheck: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			createdAt := time.Now().Add(-time.Minute)
			if tt.fresh {
				createdAt = time.Now()
			}
			instance := &Instance{
				ID:               "update-status-archived-test",
				Tool:             "claude",
				Status:           tt.status,
				CreatedAt:        createdAt,
				tmuxSession:      tt.tmuxSession,
				addedThisProcess: tt.added,
			}
			if tt.archived {
				instance.ArchivedAt = time.Now().Add(-time.Hour)
			}
			if tt.recentlyChecked {
				instance.lastErrorCheck = time.Now()
			}

			probeCalled := false
			instance.paneDeadExitStatusForTest = func() (int, bool) {
				probeCalled = true
				return tt.exitCode, tt.wantExitCode
			}

			if err := instance.UpdateStatus(); err != nil {
				t.Fatalf("UpdateStatus() error = %v", err)
			}
			if instance.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", instance.Status, tt.wantStatus)
			}
			if tt.archived {
				if probeCalled {
					t.Error("archived instance queried terminated-pane exit status")
				}
				if !instance.authHoldCheckedAt.IsZero() {
					t.Errorf("archived instance refreshed auth-hold death state at %v", instance.authHoldCheckedAt)
				}
			} else if tt.wantAuthCheck && instance.authHoldCheckedAt.IsZero() {
				t.Error("active terminated instance did not refresh auth-hold death state")
			}
		})
	}
}

func TestUpdateStatus_ArchivedLivePaneBypassesErrorCache(t *testing.T) {
	skipIfNoTmuxBinary(t)

	tests := []struct {
		name           string
		lastErrorCheck time.Time
	}{
		{
			name:           "recent error check",
			lastErrorCheck: time.Now(),
		},
		{
			name:           "stale error check",
			lastErrorCheck: time.Now().Add(-errorRecheckInterval - time.Second),
		},
	}

	for n, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmuxSession := tmux.NewSession(fmt.Sprintf("archived-live-cache-%d-%d", n, time.Now().UnixNano()), t.TempDir())
			if err := tmuxSession.Start("sleep 3600"); err != nil {
				t.Fatalf("start live tmux session: %v", err)
			}
			t.Cleanup(func() { _ = tmuxSession.Kill() })

			instance := &Instance{
				ID:             fmt.Sprintf("archived-live-cache-%d", n),
				Tool:           "shell",
				Status:         StatusError,
				CreatedAt:      time.Now().Add(-time.Minute),
				ArchivedAt:     time.Now().Add(-time.Hour),
				tmuxSession:    tmuxSession,
				lastErrorCheck: tt.lastErrorCheck,
			}

			if err := instance.UpdateStatus(); err != nil {
				t.Fatalf("UpdateStatus() error = %v", err)
			}
			if instance.Status == StatusStopped {
				t.Errorf("live archived pane Status = %q, must be classified from the live pane", instance.Status)
			}
			if !instance.lastErrorCheck.IsZero() {
				t.Errorf("lastErrorCheck = %v, want cleared after live-pane confirmation", instance.lastErrorCheck)
			}
		})
	}
}
