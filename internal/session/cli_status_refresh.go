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

	// Cold-load hook status files for every tool that emits lifecycle
	// events. The CLI is a fresh OS process with no StatusFileWatcher
	// running; without this the "running" event written by Claude's
	// UserPromptSubmit hook never reaches UpdateStatus's fast-path window.
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if !IsClaudeCompatible(inst.Tool) && inst.Tool != "codex" && inst.Tool != "gemini" && inst.Tool != "antigravity" {
			continue
		}
		if hs := readHookStatusFile(inst.ID); hs != nil {
			inst.UpdateHookStatus(hs)
		}
	}
}
