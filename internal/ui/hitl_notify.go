package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/notify"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// waitingNotifyText builds the bell/desktop message for a session that just
// entered the waiting state.
func waitingNotifyText(inst *session.Instance) (title, body string) {
	name := inst.Title
	if name == "" {
		name = inst.ID
	}
	return "agent-hopdeck", name + " is waiting for input"
}

// notifyNewlyWaiting rings the bell + raises a desktop notification for each
// session id that just transitioned into waiting (the `added` set returned by
// NotificationManager.SyncFromInstances). Edge-triggered: SyncFromInstances
// only reports a given id in `added` once per transition, so this fires once.
func (h *Home) notifyNewlyWaiting(added []string, instances []*session.Instance) {
	if !h.hitlNotifyEnabled || len(added) == 0 {
		return
	}
	byID := make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
	}
	rang := false
	for _, id := range added {
		inst, ok := byID[id]
		if !ok || inst.GetStatusThreadSafe() != session.StatusWaiting {
			continue // showAll mode adds non-waiting ids too; skip them
		}
		if !rang {
			notify.Bell()
			rang = true
		}
		title, body := waitingNotifyText(inst)
		notify.Desktop(title, body)
	}
}
