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

// Change is the data of a todo.changed frame. Todo is the item after the
// change (for remove: the item as it was). Open is the number of open
// items after the change. By is the source of the change (cli, phone, …).
type Change struct {
	Action string `json:"action"`
	Todo   Item   `json:"todo"`
	Open   int    `json:"open"`
	By     string `json:"by"`
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
		if bus.PublishWithID(KindChanged, c.Todo.SessionID, c) != "" && bus.Flush(flushWait) {
			done = append(done, p)
		}
		_ = bus.Close()
	}
	return done
}
