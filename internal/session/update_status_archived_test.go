package session

import (
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
