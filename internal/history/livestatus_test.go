package history

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain isolates this package's tests from the real ~/.agent-deck config.
//
// agent-hopdeck: this package imports internal/session, whose constructors
// (e.g. NewInstanceWithTool, used below) transitively resolve agent-deck's
// own user config under the real HOME unless sandboxed.
// testutil.IsolateHome() closes that gap (see internal/testutil/homeenv.go —
// 2026-06-04 data-loss incident); without it, this suite could read/write
// the developer's real ~/.agent-deck config. Mirrors the pattern already
// used in internal/history/source/claudecode_test.go. Deliberately NOT named
// *testmain_test.go: that filename suffix is audited by
// internal/testutil.TestAllTestMainsIsolateTmuxSocket, which additionally
// requires IsolateTmuxSocket() — irrelevant here since this package never
// starts a tmux session (no Instance.Start() call).
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body so the deferred cleanup below
// actually runs (os.Exit does not run defers).
func runTestMain(m *testing.M) int {
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	return m.Run()
}

func TestOverlayInstanceStatus_MapsRunningToBusy(t *testing.T) {
	projects := []model.Project{{
		Path: "/proj", Name: "proj",
		Sessions: []model.Session{
			{ID: "sess-a", Status: model.StatusRecent},
			{ID: "sess-b", Status: model.StatusClosed},
		},
	}}
	inst := session.NewInstanceWithTool("proj", "/proj", "claude")
	inst.ClaudeSessionID = "sess-a"
	inst.Status = session.StatusRunning

	OverlayInstanceStatus(projects, []*session.Instance{inst})

	if got := projects[0].Sessions[0].Status; got != model.StatusRunningBusy {
		t.Fatalf("sess-a status = %v, want StatusRunningBusy", got)
	}
	if got := projects[0].Sessions[1].Status; got != model.StatusClosed {
		t.Fatalf("sess-b status = %v, want StatusClosed (unchanged)", got)
	}
}

func TestOverlayInstanceStatus_AllStatusMappings(t *testing.T) {
	tests := []struct {
		name           string
		instanceStatus session.Status
		expectedStatus model.SessionStatus
	}{
		{
			name:           "StatusRunning maps to StatusRunningBusy",
			instanceStatus: session.StatusRunning,
			expectedStatus: model.StatusRunningBusy,
		},
		{
			name:           "StatusStarting maps to StatusRunningBusy",
			instanceStatus: session.StatusStarting,
			expectedStatus: model.StatusRunningBusy,
		},
		{
			name:           "StatusWaiting maps to StatusWaiting",
			instanceStatus: session.StatusWaiting,
			expectedStatus: model.StatusWaiting,
		},
		{
			name:           "StatusIdle maps to StatusRunningIdle",
			instanceStatus: session.StatusIdle,
			expectedStatus: model.StatusRunningIdle,
		},
		{
			name:           "StatusStopped leaves browsed status unchanged",
			instanceStatus: session.StatusStopped,
			expectedStatus: model.StatusRecent,
		},
		{
			name:           "StatusError leaves browsed status unchanged",
			instanceStatus: session.StatusError,
			expectedStatus: model.StatusRecent,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Start browsed session with a known status
			projects := []model.Project{{
				Path: "/proj", Name: "proj",
				Sessions: []model.Session{
					{ID: "sess-x", Status: model.StatusRecent},
				},
			}}

			// Create live instance and set its status
			inst := session.NewInstanceWithTool("proj", "/proj", "claude")
			inst.ClaudeSessionID = "sess-x"
			inst.Status = tc.instanceStatus

			OverlayInstanceStatus(projects, []*session.Instance{inst})

			if got := projects[0].Sessions[0].Status; got != tc.expectedStatus {
				t.Fatalf("instance status %v: got %v, want %v", tc.instanceStatus, got, tc.expectedStatus)
			}
		})
	}
}
