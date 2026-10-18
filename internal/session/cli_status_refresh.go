package session

import "github.com/asheshgoplani/agent-deck/internal/tmux"

// RefreshInstancesForCLIStatus is the CLI analogue of
// SessionDataService.refreshStatuses (internal/web) and
// Home.backgroundStatusUpdate (internal/ui). CLI JSON emitters must call
// it before iterating inst.UpdateStatus() so the tmux pane-title cache is
// warm and on-disk hook statuses are loaded — without this step the title
// fast-path in tmux.GetStatus cannot fire and long-running Claude sessions
// get reported as idle/waiting instead of running (issue #610).
//
// Symmetry with the other two surfaces:
//   - internal/ui/home.go:backgroundStatusUpdate
//   - internal/web/session_data_service.go:refreshStatuses
func RefreshInstancesForCLIStatus(instances []*Instance) {
	if len(instances) == 0 {
		return
	}
	// Warm the shared tmux caches so the title fast-path in GetStatus can
	// read pane titles (braille spinner while Claude is working) without
	// hitting a subprocess per instance.
	tmux.RefreshExistingSessions()
	tmux.RefreshPaneInfoCache()
	// Capture every live pane with one tmux client per socket instead of one
	// `capture-pane` process per session: GetStatus and Substate then read
	// the primed frame. This is the O(sessions) subprocess cost of every
	// `list --json` a remote poller runs.
	primeCLIStatusCaptures(instances)

	// Cold-load hook status files for every tool that emits lifecycle
	// events. The CLI is a fresh OS process with no StatusFileWatcher
	// running; without this the "running" event written by Claude's
	// UserPromptSubmit hook never reaches UpdateStatus's fast-path window.
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if !HookStatusTool(inst.Tool) {
			continue
		}
		if hs := readHookStatusFile(inst.ID); hs != nil {
			inst.UpdateHookStatus(hs)
		}
	}
}

// ReloadHookStatus re-reads inst's hook status file from disk and applies it.
// It is the cold-load half of RefreshInstancesForCLIStatus without the tmux
// cache warm-up, for callers that poll only the hook signal, such as
// `session send`'s busy probe (issues #1978, #2033), and it goes through the
// same UpdateHookStatus ownership checks. A missing or unreadable file leaves
// the instance untouched.
func ReloadHookStatus(inst *Instance) {
	if inst == nil {
		return
	}
	if hs := readHookStatusFile(inst.ID); hs != nil {
		inst.UpdateHookStatus(hs)
	}
}

// primeCLIStatusCaptures batches the pane captures of the instances a status
// pass is about to read (tmux.PrimePaneCaptures).
func primeCLIStatusCaptures(instances []*Instance) {
	sessions := make([]*tmux.Session, 0, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if sess := inst.GetTmuxSession(); sess != nil {
			sessions = append(sessions, sess)
		}
	}
	tmux.PrimePaneCaptures(sessions)
}
