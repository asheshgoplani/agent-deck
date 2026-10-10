package watcher

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Outbox takes routed events for durable delivery to their conductor
// (#2537). The engine's writer calls Enqueue for a new event routed to a
// conductor, before it stores the event; the health loop reads Pending.
type Outbox interface {
	Enqueue(watcherID, watcherName string, evt Event) error
	Pending() (map[string][]PendingDelivery, error)
}

// PendingDelivery is the routed events of one watcher still waiting for one
// conductor: how many, and when the oldest was queued.
type PendingDelivery struct {
	Conductor string    `json:"conductor"`
	Count     int       `json:"count"`
	Oldest    time.Time `json:"oldest_queued_at"`
}

// deliveryKeyPrefix starts the send queue key of every routed event.
const deliveryKeyPrefix = "watcher:"

// DeliveryKey is the send queue key of evt from the watcher watcherID: the
// same event routed again (a replay, or a re-insert after its row was
// pruned) is not queued twice.
func DeliveryKey(watcherID string, evt Event) string {
	return deliveryKeyPrefix + watcherID + ":" + evt.DedupKey()
}

// ConductorMessage is the single line a routed event is delivered as. It
// prefers the full message Body (so the conductor receives the complete
// text, not the first-line/200-byte Subject label) and falls back to Subject
// when Body is empty (e.g. v1 events). Newlines are collapsed to spaces, so
// the event reaches the conductor as one submitted line.
func ConductorMessage(evt Event) string {
	text := strings.TrimSpace(evt.Body)
	if text == "" {
		text = evt.Subject
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\n", " ")
	return fmt.Sprintf("[%s] %s: %s", evt.Source, evt.Sender, text)
}

// ConductorOutbox delivers routed events through the profile's send queue
// (internal/sendqueue, the outbox behind `session send --queue`): one durable
// record per event, delivered at most once, in order, by a detached worker
// per conductor that outlives the process that queued it. The worker waits
// for a stopped conductor instead of failing the delivery, up to Deadline
// when one is set.
type ConductorOutbox struct {
	profile  string
	dir      string
	db       *statedb.StateDB
	deadline time.Duration
	spawn    func(profile, sessionID string) error
	log      *slog.Logger
}

// OutboxConfig configures a ConductorOutbox.
type OutboxConfig struct {
	Profile string
	// Dir is the profile's send queue directory (sendqueue.Dir).
	Dir string
	// DB finds the conductor sessions.
	DB *statedb.StateDB
	// Deadline is how long an event may wait for its conductor; 0 waits
	// until the conductor takes it.
	Deadline time.Duration
	// SpawnWorker starts the conductor's send worker; nil uses
	// sendqueue.SpawnWorker (this binary's `session send-worker`).
	SpawnWorker func(profile, sessionID string) error
	Logger      *slog.Logger
}

// NewConductorOutbox returns an outbox writing to cfg.Dir.
func NewConductorOutbox(cfg OutboxConfig) *ConductorOutbox {
	o := &ConductorOutbox{profile: cfg.Profile, dir: cfg.Dir, db: cfg.DB, deadline: cfg.Deadline, spawn: cfg.SpawnWorker, log: cfg.Logger}
	if o.spawn == nil {
		o.spawn = sendqueue.SpawnWorker
	}
	if o.log == nil {
		o.log = slog.Default()
	}
	return o
}

// Enqueue queues evt for the conductor it is routed to (evt.RoutedTo) and
// starts that conductor's worker. An event queued before under the same
// key is not queued again. A conductor with no session cannot be queued
// for: the error says so, and the event stays in watcher_events.
func (o *ConductorOutbox) Enqueue(watcherID, watcherName string, evt Event) error {
	title := session.ConductorSessionTitle(evt.RoutedTo)
	rows, err := o.db.LoadInstances()
	if err != nil {
		return fmt.Errorf("look up conductor %q: %w", evt.RoutedTo, err)
	}
	var target *statedb.InstanceRow
	for _, row := range rows {
		if row.Title == title {
			target = row
			break
		}
	}
	if target == nil {
		return fmt.Errorf("conductor %q has no session titled %q", evt.RoutedTo, title)
	}
	now := time.Now()
	s := sendqueue.Send{
		SessionID: target.ID, SessionTitle: target.Title, Tool: target.Tool,
		Message: ConductorMessage(evt), Sender: deliveryKeyPrefix + watcherName,
		Key: DeliveryKey(watcherID, evt), WaitWhileStopped: true, Ledger: true,
	}
	if o.deadline > 0 {
		s.Deadline = now.Add(o.deadline)
	}
	rec, created, err := sendqueue.EnqueueOnce(o.profile, o.dir, s, now)
	if err != nil {
		return err
	}
	if !created {
		o.log.Info("watcher_delivery_already_queued",
			slog.String("watcher", watcherName), slog.String("conductor", evt.RoutedTo),
			slog.String("send_id", rec.SendID), slog.String("state", rec.State))
		return nil
	}
	o.log.Info("watcher_delivery_queued",
		slog.String("watcher", watcherName), slog.String("conductor", evt.RoutedTo),
		slog.String("send_id", rec.SendID))
	o.startWorker(rec.SessionID)
	return nil
}

// Resume starts the worker of every conductor that still has a routed event
// to deliver, e.g. after a reboot killed the workers. A live worker keeps its
// target lock, so the extra one exits at once.
func (o *ConductorOutbox) Resume() {
	recs, err := sendqueue.List(o.dir, "")
	if err != nil {
		o.log.Warn("watcher_delivery_resume_failed", slog.String("error", err.Error()))
		return
	}
	seen := map[string]bool{}
	for _, r := range recs {
		if r.Final() || !strings.HasPrefix(r.Key, deliveryKeyPrefix) || seen[r.SessionID] {
			continue
		}
		seen[r.SessionID] = true
		o.startWorker(r.SessionID)
	}
}

func (o *ConductorOutbox) startWorker(sessionID string) {
	if err := o.spawn(o.profile, sessionID); err != nil {
		// The record stays queued; the next event for this conductor, or the
		// next engine start, starts a worker again.
		o.log.Warn("watcher_delivery_worker_start_failed",
			slog.String("session_id", sessionID), slog.String("error", err.Error()))
	}
}

// Pending returns PendingDeliveries for the outbox's queue.
func (o *ConductorOutbox) Pending() (map[string][]PendingDelivery, error) {
	return PendingDeliveries(o.dir)
}

// PendingDeliveries reads the send queue in dir and returns, per watcher id,
// the routed events not delivered yet, per conductor, conductors by name.
func PendingDeliveries(dir string) (map[string][]PendingDelivery, error) {
	recs, err := sendqueue.List(dir, "")
	if err != nil {
		return nil, err
	}
	out := map[string][]PendingDelivery{}
	for _, r := range recs {
		rest, ok := strings.CutPrefix(r.Key, deliveryKeyPrefix)
		if !ok || r.Final() {
			continue
		}
		watcherID, _, _ := strings.Cut(rest, ":")
		conductor := strings.TrimPrefix(r.SessionTitle, session.ConductorSessionTitlePrefix)
		queued, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
		list := out[watcherID]
		i := 0
		for i < len(list) && list[i].Conductor != conductor {
			i++
		}
		if i == len(list) {
			list = append(list, PendingDelivery{Conductor: conductor, Oldest: queued})
		}
		list[i].Count++
		if queued.Before(list[i].Oldest) {
			list[i].Oldest = queued
		}
		out[watcherID] = list
	}
	for _, list := range out {
		sortPending(list)
	}
	return out, nil
}

func sortPending(list []PendingDelivery) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Conductor < list[j-1].Conductor; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
