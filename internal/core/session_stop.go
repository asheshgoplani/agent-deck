package core

import (
	"context"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// SessionStopIn is the input of session.stop.
type SessionStopIn struct {
	Profile string `json:"profile" doc:"Profile whose store holds the session"`
	Session string `json:"session" doc:"Session id, id prefix, title or path"`
}

// SessionStopOut is the output of session.stop.
type SessionStopOut struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Drained      string `json:"drained,omitempty" doc:"Id of the queued session started in the freed slot"`
	DrainedTitle string `json:"drained_title,omitempty"`
}

func (deps Deps) sessionStop(ctx context.Context, in SessionStopIn) (SessionStopOut, error) {
	d, err := loadSessionData(in.Profile)
	if err != nil {
		return SessionStopOut{}, err
	}
	inst, err := deps.resolve(in.Session, d.instances)
	if err != nil {
		return SessionStopOut{}, err
	}
	if !inst.Exists() {
		return SessionStopOut{}, Errorf(CodeNotRunning, "session '%s' is not running", inst.Title)
	}

	// Capture tool conversation ids before Kill: show-environment fails on
	// a dead tmux session.
	inst.SyncSessionIDsFromTmux()
	if err := inst.Kill(); err != nil {
		return SessionStopOut{}, &Error{Code: CodeInvalid, Message: fmt.Sprintf("failed to stop session: %v", err), Cause: err}
	}

	drained := drainGroupQueue(ctx, inst.GroupPath, d.instances, d.groups)
	if err := d.saveOr("failed to save session state"); err != nil {
		return SessionStopOut{}, err
	}

	out := SessionStopOut{ID: inst.ID, Title: inst.Title}
	if drained != nil {
		out.Drained = drained.ID
		out.DrainedTitle = drained.Title
	}
	// Journaled after the verdict: RecordSessionEvent writes synchronously
	// and a slow health volume must not delay the answer.
	AfterFunc(ctx, func() {
		session.RecordSessionEvent(in.Profile, inst.ID, health.KindStop, nil)
		session.RecallNotifyInstance(inst, health.KindStop)
	})
	return out, nil
}

// drainGroupQueue starts the oldest queued instance in groupPath when a slot
// is free and returns it. Best-effort: a failed start is reported through
// EventQueueDrainFailed and marks that instance errored.
func drainGroupQueue(ctx context.Context, groupPath string, instances []*session.Instance, groups []*session.GroupData) *session.Instance {
	tree := session.NewGroupTreeWithGroups(instances, groups)
	max := session.GroupMaxConcurrent(tree, groupPath)
	if session.IsAtCap(session.CountRunningInGroup(instances, groupPath), max) {
		return nil
	}
	next := session.FindNextQueued(instances, groupPath)
	if next == nil {
		return nil
	}
	if err := next.Start(); err != nil {
		next.Status = session.StatusError
		Emit(ctx, Event{Kind: EventQueueDrainFailed, ID: next.ID, Title: next.Title, Err: err})
		return nil
	}
	return next
}
