package session

import "github.com/asheshgoplani/agent-deck/internal/tmux"

// Defect 317: a stop kills the pane and then saves stopped. Another process
// (the notify daemon, a TUI) that samples the pane in between sees it gone
// and commits error; the stop's full save then refused stopped (stale
// concurrent Status conflict), the stop failed, the row stayed error, and
// the idle-death watch (#2806) told the parent its child had died.
//
// So a stop writes its own verdict right after the kill, before the full
// save. The generic snapshot merge is left alone: a stopped in a TUI save
// can be an observation (classifyTerminatedPane), not a decision, and must
// not beat a concurrent error there.

// StopAfterKillHook runs between a stop's kill and its stopped write (tests
// only: it lets a test commit another process's status in that window).
var StopAfterKillHook func(id string)

// SavedRowStatus is the row's saved status, or "" when it cannot be read. A
// stop reads it before its kill: the in-memory status can differ from the
// row (waiting in memory, idle saved).
func SavedRowStatus(storage *Storage, id string) string {
	if storage == nil || storage.GetDB() == nil {
		return ""
	}
	row, err := storage.GetDB().LoadInstanceByID(id)
	if err != nil || row == nil {
		return ""
	}
	return row.Status
}

// StopPreKill is what a stop reads before its kill: the row's saved status
// and the instance's spawn generation (instance_spawn_guard.go).
type StopPreKill struct {
	Status   string
	spawnGen uint64
}

// BeforeStopKill reads StopPreKill. Call it right before the kill.
func BeforeStopKill(storage *Storage, id string) StopPreKill {
	return StopPreKill{Status: SavedRowStatus(storage, id), spawnGen: spawnGenerationSnapshot(id)}
}

// WriteStoppedAfterKill writes stopped to the row after a stop's kill. The
// status read before the kill (pre.Status), or an error sample of the kill,
// is replaced. A concurrent running, waiting or idle is replaced too when no
// pane for this session can be alive after the kill: the field case is a
// child that woke (or a dying process's own hooks) committing running inside
// the kill window, then the daemon sampling the dead pane as error.
//
// A restart is told apart by its live pane only once it has saved its tmux
// name to the row: a recreate gets a new name, which reaches the row only at
// that process's later full save. Until then the spawn guard covers it: a
// held spawn lock, or a spawn generation that moved since pre (both are files,
// so they cover a restart in another process). In any of those cases, or when
// a probe is indeterminate, only pre.Status and error are replaced and the
// full save refuses as before. starting and other deliberate statuses (queued)
// are never replaced. Best-effort: a failed write leaves the full save to
// decide.
func WriteStoppedAfterKill(storage *Storage, inst *Instance, pre StopPreKill) {
	if storage == nil || inst == nil {
		return
	}
	if StopAfterKillHook != nil {
		StopAfterKillHook(inst.ID)
	}
	db := storage.GetDB()
	if db == nil {
		return
	}
	stopped := string(StatusStopped)
	replace := func(prior string) bool {
		if prior == "" || prior == stopped {
			return false
		}
		applied, err := db.WriteStatusIfCurrent(inst.ID, prior, stopped, inst.Tool)
		return err == nil && applied
	}
	if replace(pre.Status) || replace(string(StatusError)) {
		return
	}
	if !stopPaneGone(storage, inst, pre) {
		return
	}
	for _, prior := range []string{string(StatusRunning), string(StatusWaiting), string(StatusIdle), string(StatusError)} {
		if replace(prior) {
			return
		}
	}
}

// stopPaneGone reports whether no tmux session can be alive for inst after
// its kill. A spawn in flight (lock held) or finished since pre (generation
// moved) may have a pane under a name the row does not hold yet: false.
// Otherwise a fresh, uncached probe of the instance's own tmux name and of
// the one the row holds now, each on its own socket, must prove both absent.
// Any live or indeterminate answer is false.
func stopPaneGone(storage *Storage, inst *Instance, pre StopPreKill) bool {
	if instanceSpawnLockHeld(inst.ID) || spawnedSince(inst.ID, pre.spawnGen) {
		return false
	}
	type target struct{ socket, name string }
	var targets []target
	if ts := inst.GetTmuxSession(); ts != nil && ts.TmuxName() != "" {
		targets = append(targets, target{ts.SocketName, ts.TmuxName()})
	}
	if row, err := storage.GetDB().LoadInstanceByID(inst.ID); err == nil && row != nil && row.TmuxSession != "" {
		targets = append(targets, target{row.TmuxSocketName, row.TmuxSession})
	}
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if alive, err := tmux.ProbeSessionOnSocket(t.socket, t.name); err != nil || alive {
			return false
		}
	}
	return true
}
