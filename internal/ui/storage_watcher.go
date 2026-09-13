package ui

import (
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

var watcherLog = logging.ForComponent(logging.CompStorage)

const pollInterval = 2 * time.Second

// StorageWatcher observes committed material registry changes on a dedicated
// live SQLite connection. Own writes are conservatively reloaded too.
type StorageWatcher struct {
	observer       *statedb.RegistryObserver
	reloadCh       chan struct{}
	closeCh        chan struct{}
	closeOnce      sync.Once
	mu             sync.Mutex
	acknowledged   *statedb.RegistrySnapshotResult
	scannedVersion int64
	scannedEpoch   uint64
	pending        bool
	sequence       uint64
	closed         bool
}

type storageLoadTicket struct {
	sequence                uint64
	before, after           int64
	beforeEpoch, afterEpoch uint64
}

func NewStorageWatcher(db *statedb.StateDB) (*StorageWatcher, error) {
	if db == nil {
		return nil, nil
	}
	observer, err := db.NewRegistryObserver()
	if err != nil {
		return nil, err
	}
	snapshot, _, after, err := observer.Snapshot()
	if err != nil {
		_ = observer.Close()
		return nil, err
	}
	return &StorageWatcher{observer: observer, reloadCh: make(chan struct{}, 1), closeCh: make(chan struct{}), acknowledged: snapshot, scannedVersion: after, pending: true}, nil
}
func (sw *StorageWatcher) Start() { go sw.pollLoop() }
func (sw *StorageWatcher) pollLoop() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sw.closeCh:
			return
		case <-ticker.C:
			sw.checkAndNotify()
		}
	}
}
func (sw *StorageWatcher) signalLocked() {
	if sw.closed {
		return
	}
	select {
	case sw.reloadCh <- struct{}{}:
	default:
	}
}
func (sw *StorageWatcher) checkAndNotify() {
	version, epoch, err := sw.observer.Probe()
	if err != nil {
		watcherLog.Debug("watcher_poll_failed", slog.String("error", err.Error()))
		sw.mu.Lock()
		sw.pending = true
		sw.signalLocked()
		sw.mu.Unlock()
		return
	}
	sw.mu.Lock()
	unchanged := version == sw.scannedVersion && epoch == sw.scannedEpoch && !sw.pending
	closed := sw.closed
	sw.mu.Unlock()
	if unchanged || closed {
		return
	}
	snapshot, before, after, err := sw.observer.Snapshot()
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.closed {
		return
	}
	if err != nil {
		sw.pending = true
		sw.signalLocked()
		return
	}
	sw.scannedVersion = after
	sw.scannedEpoch = epoch
	if before != after || !reflect.DeepEqual(snapshot, sw.acknowledged) {
		sw.pending = true
	}
	if sw.pending {
		sw.signalLocked()
	}
}
func (sw *StorageWatcher) ReloadChannel() <-chan struct{} { return sw.reloadCh }

// NotifySave remains compatible with callers. Intent is not commit provenance;
// it never suppresses unrelated writes.
func (sw *StorageWatcher) NotifySave() {}
func (sw *StorageWatcher) TriggerReload() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.pending = true
	sw.signalLocked()
}
func (sw *StorageWatcher) issueLoad() storageLoadTicket {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.sequence++
	return storageLoadTicket{sequence: sw.sequence}
}

func (sw *StorageWatcher) beginLoad() (storageLoadTicket, error) {
	return sw.probeLoad(sw.issueLoad())
}

func (sw *StorageWatcher) probeLoad(ticket storageLoadTicket) (storageLoadTicket, error) {
	version, epoch, err := sw.observer.Probe()
	ticket.beforeEpoch = epoch
	ticket.before = version
	return ticket, err
}
func (sw *StorageWatcher) endLoad(ticket storageLoadTicket) (storageLoadTicket, error) {
	version, epoch, err := sw.observer.Probe()
	ticket.afterEpoch = epoch
	ticket.after = version
	return ticket, err
}
func (sw *StorageWatcher) current(ticket storageLoadTicket) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return !sw.closed && ticket.sequence == sw.sequence
}
func (sw *StorageWatcher) acknowledge(ticket storageLoadTicket, snapshot *statedb.RegistrySnapshotResult, success bool) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.closed || ticket.sequence != sw.sequence {
		return
	}
	if !success || snapshot == nil {
		sw.pending = true
		return
	}
	sw.acknowledged = snapshot
	// Never acknowledge a version observed after the load's own boundary.
	sw.pending = ticket.before != ticket.after || ticket.beforeEpoch != ticket.afterEpoch || sw.scannedEpoch != ticket.beforeEpoch || sw.scannedVersion != ticket.before
	sw.scannedVersion = ticket.before
	sw.scannedEpoch = ticket.beforeEpoch
	if sw.pending {
		sw.signalLocked()
	}
}
func (sw *StorageWatcher) Warning() string { return "" }
func (sw *StorageWatcher) Close() error {
	var err error
	sw.closeOnce.Do(func() { sw.mu.Lock(); sw.closed = true; close(sw.closeCh); sw.mu.Unlock(); err = sw.observer.Close() })
	return err
}
