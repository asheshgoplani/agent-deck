package session

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// cliStatusRewarmAfter is how long the batched capture may take before the
// session and pane-info caches warmed ahead of it are warmed again. It is half
// the 2s session-cache TTL: a prime slower than this risks leaving the status
// pass with expired caches (#3089).
const cliStatusRewarmAfter = time.Second

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
	primeStarted := time.Now()
	primeCLIStatusCaptures(instances)
	// On a loaded host the prime can outlast the caches warmed above (the
	// session cache lives 2s; a timed-out batch waits 3s and more). Every row
	// would then ask tmux on its own (has-session, list-panes,
	// display-message: 58 live rows cost 164 tmux calls in #3089), so warm
	// them once more instead.
	if time.Since(primeStarted) > cliStatusRewarmAfter {
		tmux.RefreshExistingSessions()
		tmux.RefreshPaneInfoCache()
	}

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
