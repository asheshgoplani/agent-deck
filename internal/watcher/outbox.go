package watcher

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
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

// ErrNoConductorSession is the Enqueue error for a conductor with no
// session to address. Unlike a failure to read the sessions or write the
// queue, retrying does not help until the conductor is created.
var ErrNoConductorSession = errors.New("no conductor session")

// ErrDeliveryQueueFull is the Enqueue error for a watcher that already has
// the most undelivered routed events the outbox keeps for it per conductor.
// The event is not queued (new events are refused; queued ones are never
// dropped, so what was queued is still delivered in order); the engine
// stores it in watcher_events and counts it as refused in state.db. It is
// never delivered: retrying does not help until the conductor's worker
// delivers some, and holding it would move the growth into the writer.
var ErrDeliveryQueueFull = errors.New("conductor delivery queue full")

// The outbox caps, per watcher and conductor, the routed events queued and
// not yet delivered. A webhook watcher bound to loopback with no secret
// accepts unsigned requests from any local process, and every new body is a
// new event, so without a cap one local process could grow the durable send
// queue without limit while the conductor is stopped or busy.
//
// The cap is per watcher, not per conductor, so a flood stays inside the
// watcher it comes through: an unsigned loopback webhook that fills its share
// cannot make a signed GitHub watcher routed to the same conductor be
// refused. There is no shared per-conductor ceiling on top: any ceiling the
// flooding watcher can reach alone brings that refusal back, and the total
// is already bounded by the watchers the user configured times these caps.
//
// DefaultMaxPendingDeliveries is far above a real backlog (a conductor that
// is behind by a thousand events is not going to read them) and keeps
// listing the queue cheap. DefaultMaxPendingDeliveryBytes bounds the disk a
// few large bodies can take: a webhook body may be 1 MiB, so the entry cap
// alone would allow about 1 GiB.
const (
	DefaultMaxPendingDeliveries          = 1000
	DefaultMaxPendingDeliveryBytes int64 = 64 << 20
)

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

	maxPending      int
	maxPendingBytes int64
	// full holds the (watcher, conductor session) pairs last found full, so
	// the outbox can say once when a pair has room again. The engine logs
	// and counts the refusals themselves.
	mu   sync.Mutex
	full map[string]bool
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
	// MaxPending caps the undelivered routed events queued per watcher and
	// conductor; 0 means DefaultMaxPendingDeliveries.
	MaxPending int
	// MaxPendingBytes caps their message text per watcher and conductor; 0
	// means DefaultMaxPendingDeliveryBytes.
	MaxPendingBytes int64
}

// NewConductorOutbox returns an outbox writing to cfg.Dir.
func NewConductorOutbox(cfg OutboxConfig) *ConductorOutbox {
	o := &ConductorOutbox{profile: cfg.Profile, dir: cfg.Dir, db: cfg.DB, deadline: cfg.Deadline, spawn: cfg.SpawnWorker, log: cfg.Logger,
		maxPending: cfg.MaxPending, maxPendingBytes: cfg.MaxPendingBytes, full: map[string]bool{}}
	if o.maxPending <= 0 {
		o.maxPending = DefaultMaxPendingDeliveries
	}
	if o.maxPendingBytes <= 0 {
		o.maxPendingBytes = DefaultMaxPendingDeliveryBytes
	}
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
// key is not queued again. The conductor's session is its non-archived one:
// an archived session keeps its identity in storage, but it is not the
// conductor any more. The session is matched by its stored conductor name
// when it has one (stable across renames and title decoration), else by a
// title that parses to the name, as Instance.IsConductorNamed does. A
// conductor with no such session cannot be queued for: the error says so,
// and the event stays in watcher_events.
func (o *ConductorOutbox) Enqueue(watcherID, watcherName string, evt Event) error {
	rows, err := o.db.LoadInstances()
	if err != nil {
		return fmt.Errorf("look up conductor %q: %w", evt.RoutedTo, err)
	}
	var target *statedb.InstanceRow
	for _, row := range rows {
		if rowIsConductorNamed(row, evt.RoutedTo) && row.ArchivedAt.IsZero() {
			target = row
			break
		}
	}
	if target == nil {
		return fmt.Errorf("%w: conductor %q has no session (stored name or title %q) that is not archived", ErrNoConductorSession, evt.RoutedTo, session.ConductorSessionTitle(evt.RoutedTo))
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
	if err := o.checkRoom(target, watcherID, watcherName, evt.RoutedTo, s); err != nil {
		return err
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

// checkRoom refuses s with ErrDeliveryQueueFull when the conductor's queue
// already holds maxPending undelivered routed events of the watcher
// watcherID, or s would take their message text past maxPendingBytes. Other
// watchers' events for the same conductor do not count. A send whose key is
// already queued is let through: EnqueueOnce reports it as already queued
// and writes nothing. The first event queued for a pair after a refusal logs
// that the pair has room again, naming that watcher.
func (o *ConductorOutbox) checkRoom(target *statedb.InstanceRow, watcherID, watcherName, conductor string, s sendqueue.Send) error {
	if _, err := os.Stat(sendqueue.KeyPath(o.dir, s.Key)); err == nil {
		return nil
	}
	recs, err := sendqueue.List(o.dir, target.ID)
	if err != nil {
		return fmt.Errorf("read the send queue of conductor %q: %w", conductor, err)
	}
	prefix := deliveryKeyPrefix + watcherID + ":"
	count, size := 0, int64(len(s.Message))
	for _, r := range recs {
		if r.Final() || !strings.HasPrefix(r.Key, prefix) {
			continue
		}
		count++
		size += int64(len(r.Message))
	}
	pair := watcherID + "\x00" + target.ID
	o.mu.Lock()
	defer o.mu.Unlock()
	if count < o.maxPending && size <= o.maxPendingBytes {
		if o.full[pair] {
			delete(o.full, pair)
			o.log.Info("watcher_delivery_queue_has_room",
				slog.String("watcher", watcherName), slog.String("conductor", conductor),
				slog.Int("pending", count))
		}
		return nil
	}
	o.full[pair] = true
	return fmt.Errorf("%w: watcher %q has %d undelivered routed events for conductor %q (cap %d, %d bytes, cap %d)",
		ErrDeliveryQueueFull, watcherName, count, conductor, o.maxPending, size-int64(len(s.Message)), o.maxPendingBytes)
}

// rowIsConductorNamed reports whether the stored session row is the named
// conductor: a stored conductor name decides alone on a row flagged
// IsConductor (an unflagged row's stored name is ignored, #2708); otherwise
// the title must parse to name (Instance.IsConductorNamed on a statedb row).
func rowIsConductorNamed(row *statedb.InstanceRow, name string) bool {
	if row == nil || name == "" {
		return false
	}
	if stored := session.ReadConductorNameFromToolData(row.ToolData); stored != "" && row.IsConductor {
		return stored == name
	}
	return session.ConductorNameFromTitle(row.Title) == name
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
