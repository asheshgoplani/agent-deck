package session

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

// WriteStoppedAfterKill writes stopped to the row after a stop's kill. Only
// the status read before the kill (loaded), or an error sample of the kill,
// is replaced: a concurrent starting or running is a restart, newer than the
// stop, and is left for the full save to refuse. Best-effort: a failed write
// leaves the full save to decide, as before.
func WriteStoppedAfterKill(storage *Storage, inst *Instance, loaded string) {
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
	for _, expected := range []string{loaded, string(StatusError)} {
		if expected == "" || expected == stopped {
			continue
		}
		if applied, err := db.WriteStatusIfCurrent(inst.ID, expected, stopped, inst.Tool); err == nil && applied {
			return
		}
	}
}
