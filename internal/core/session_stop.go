package core

import (
	"context"
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
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
	loaded := session.BeforeStopKill(d.storage, inst.ID)
	if err := inst.Kill(); err != nil {
		return SessionStopOut{}, &Error{Code: CodeInvalid, Message: fmt.Sprintf("failed to stop session: %v", err), Cause: err}
	}
	// Defect 317: write stopped now, so another process's error sample of
	// this kill cannot refuse the save below.
	session.WriteStoppedAfterKill(d.storage, inst, loaded)
	// Saved before the drain: a drained start waits for its agent to be ready.
	if err := d.saveOr("failed to save session state"); err != nil {
		return SessionStopOut{}, err
	}

	drained := drainGroupQueue(ctx, d.storage, inst.GroupPath, d.instances, d.groups)

	out := SessionStopOut{ID: inst.ID, Title: inst.Title}
	if drained != nil {
		out.Drained = drained.ID
		out.DrainedTitle = drained.Title
	}
	// Journaled after the verdict: RecordSessionEvent writes synchronously
	// and a slow health volume must not delay the answer. session.end is
	// recorded here so the default CLI and daemon stops are counted like
	// the TUI, web and legacy CLI stops.
	AfterFunc(ctx, func() {
		session.RecordSessionEvent(in.Profile, inst.ID, health.KindStop, nil)
		inst.RecordTelemetryEnd(telemetry.EndStop)
		session.RecallNotifyInstance(inst, health.KindStop)
	})
	return out, nil
}

// drainGroupQueue starts the oldest queued instance in groupPath when a slot
// is free and returns it. It persists only that instance's row
// (session.Storage.StartQueued), which also puts its queued message on the
// send queue. Best-effort: a failed start is reported through
// EventQueueDrainFailed and marks that instance errored. A session another
// drainer claimed first is left to it, and the next queued one is tried
// while the group is below its cap.
func drainGroupQueue(ctx context.Context, storage *session.Storage, groupPath string, instances []*session.Instance, groups []*session.GroupData) *session.Instance {
	tree := session.NewGroupTreeWithGroups(instances, groups)
	max := session.GroupMaxConcurrent(tree, groupPath)
	// The cap is checked on every pass, counting starting sessions too: a
	// claim lost to another drainer (or to a direct start) means that session
	// now holds a slot, and its stale copy here is marked starting.
	for !session.IsAtCap(session.CountOccupiedInGroup(instances, groupPath), max) {
		next := session.FindNextQueued(instances, groupPath)
		if next == nil {
			return nil
		}
		started, err := storage.StartQueued(next)
		if started.Warning != "" {
			Warn(ctx, started.Warning)
		}
		if err != nil {
			Emit(ctx, Event{Kind: EventQueueDrainFailed, ID: next.ID, Title: next.Title, Err: err})
			return nil
		}
		if started.Started {
			return next
		}
		// Claimed by another drainer: this copy is stale.
		next.Status = session.StatusStarting
	}
	return nil
}

func init() { session.SetQueuedMessageSink(EnqueueQueuedStartMessage, sendqueue.DefaultRetryBudget) }

// spawnSendWorker starts a target's send worker; tests replace it.
var spawnSendWorker = sendqueue.SpawnWorker

// SetSendWorkerSpawnForTest replaces the send-worker spawn of
// EnqueueQueuedStartMessage and returns a restore func.
func SetSendWorkerSpawnForTest(fn func(profile, sessionID string) error) func() {
	prev := spawnSendWorker
	spawnSendWorker = fn
	return func() { spawnSendWorker = prev }
}

// EnqueueQueuedStartMessage is the session queued-message sink: it puts a
// queued -m message on the durable send queue next to the profile's state.db
// (profileDir) and starts the target's send worker for that profile, which
// types it once the agent is ready. It returns at once: a drain runs under the profile mutation lock,
// and waiting there for readiness (up to send.DefaultAgentReadyTimeout) would
// block every other mutation.
func EnqueueQueuedStartMessage(profile, profileDir string, inst *session.Instance, message string) error {
	now := time.Now()
	// It is queued before the spawn: the worker waits for the pane
	// (WaitWhileStopped) and for the agent to be idle before typing.
	s := sendqueue.Send{
		SessionID: inst.ID, SessionTitle: inst.Title, Tool: inst.Tool, Running: true,
		Message: message, Sender: "cli", Deadline: now.Add(sendqueue.DefaultRetryBudget),
		WaitWhileStopped: true,
	}
	rec, err := sendqueue.Enqueue(profile, sendqueue.Dir(profileDir), s, now)
	if err != nil {
		return fmt.Errorf("queueing the queued message of %s: %w", inst.Title, err)
	}
	if err := spawnSendWorker(profile, rec.SessionID); err != nil {
		return fmt.Errorf("starting the send worker for %s (send %s stays queued): %w", inst.Title, rec.SendID, err)
	}
	return nil
}
