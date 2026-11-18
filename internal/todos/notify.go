package todos

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// KindChanged is the bus kind published on every change of the list.
const KindChanged = "todo.changed"

// Actions of a Change.
const (
	ActionAdd    = "add"
	ActionDone   = "done"
	ActionUndo   = "undo"
	ActionRemove = "remove"
)

// Change is the data of a todo.changed frame: the action, the item's id,
// the number of open items after the change and who made it (cli, phone,
// voice, ...). It never carries the item's text: the bus log is kept on
// disk in every profile, and no heard or typed text belongs in a log.
// Screens refetch the list (`todo list --json`, GET /api/todos).
type Change struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Open   int    `json:"open"`
	By     string `json:"by"`

	// sessionID is the frame's session_id (the item's session, if any).
	sessionID string
}

// NewChange is the change event for item it.
func NewChange(action string, it Item, open int, by string) Change {
	return Change{Action: action, ID: it.ID, Open: open, By: by, sessionID: it.SessionID}
}

// flushWait bounds how long Publish waits for each bus to commit.
const flushWait = 2 * time.Second

// Publish writes c as a todo.changed frame on the bus of every profile on
// this host plus extra (normally the caller's resolved profile), so a
// screen following any profile sees it. The frame's session_id is the
// item's session id. Publishing never fails the change: a disabled or
// unwritable bus is skipped. It returns the profiles it published to.
func Publish(c Change, extra ...string) []string {
	seen := map[string]bool{}
	var profiles []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			profiles = append(profiles, p)
		}
	}
	for _, p := range extra {
		add(p)
	}
	if list, err := session.ListProfiles(); err == nil {
		for _, p := range list {
			add(p)
		}
	}
	var done []string
	for _, p := range profiles {
		bus := events.OpenProfile(p)
		if !bus.Stats().Enabled {
			_ = bus.Close()
			continue
		}
		if bus.PublishWithID(KindChanged, c.sessionID, c) != "" && bus.Flush(flushWait) {
			done = append(done, p)
		}
		_ = bus.Close()
	}
	return done
}
