package ui

// Live voice badge and hook-driven status (plan 2.3, "every change visible
// within 1 s"). Both used to wait for the 2 s tick: the badge read the
// voice file on the tick, and a hook status change reached state.db (and
// so the session.status bus frame) on the next status sweep.
//
// voiceWatch owns the voice reader in one goroutine that stats the file
// every voiceWatchInterval and sends a message only when the state
// changed, so an unchanged file costs no Update and no render. The hook
// watcher's change callback asks for one status sweep at once.

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

const voiceWatchInterval = 100 * time.Millisecond

type voiceStateMsg struct{ st *voicestate.State }

type hookChangedMsg struct{}

type voiceWatch struct {
	ch   chan *voicestate.State // newest state only
	stop chan struct{}
	wg   sync.WaitGroup
}

func startVoiceWatch(r *voicestate.Reader, interval time.Duration) *voiceWatch {
	w := &voiceWatch{ch: make(chan *voicestate.State, 1), stop: make(chan struct{})}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		var last *voicestate.State
		first := true
		for {
			st := r.Poll()
			if first || !sameVoice(last, st) {
				first, last = false, st
				select {
				case <-w.ch: // drop a state the UI has not taken yet
				default:
				}
				w.ch <- st
			}
			select {
			case <-w.stop:
				return
			case <-t.C:
			}
		}
	}()
	return w
}

// Stop ends the goroutine and waits for it.
func (w *voiceWatch) Stop() {
	if w == nil {
		return
	}
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	w.wg.Wait()
}

// sameVoice ignores updatedAt: a heartbeat that changes nothing a row
// draws needs no render.
func sameVoice(a, b *voicestate.State) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
	x.UpdatedAt, y.UpdatedAt = 0, 0
	return reflect.DeepEqual(x, y)
}

func listenForVoice(w *voiceWatch) tea.Cmd {
	if w == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case st := <-w.ch:
			return voiceStateMsg{st: st}
		case <-w.stop:
			return nil
		}
	}
}

// hookChangedNotify is the hook watcher's change callback: it may run on
// the watcher's goroutine, so it only signals.
func (h *Home) hookChangedNotify() {
	if h.hookChanged == nil {
		return
	}
	select {
	case h.hookChanged <- struct{}{}:
	default:
	}
}

func listenForHookChange(ch chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return hookChangedMsg{}
	}
}

// processHookPass runs on the status worker goroutine (the owner of
// lastPersistedStatus): it re-evaluates only the sessions whose hook
// sample changed since the last pass and persists what changed, which is
// what publishes their session.status frame.
func (h *Home) processHookPass() {
	if h.hookWatcher == nil {
		return
	}
	if hotUntil := h.navigationHotUntil.Load(); hotUntil > 0 && time.Now().UnixNano() < hotUntil {
		// The user is scrolling: no probes now; the worker retries after
		// its next pass or sweep.
		h.hookPending.Store(true)
		return
	}
	h.hookPending.Store(false)
	if h.hookPassSeen == nil {
		h.hookPassSeen = make(map[string]string)
	}
	h.instancesMu.RLock()
	instances := make([]*session.Instance, len(h.instances))
	copy(instances, h.instances)
	h.instancesMu.RUnlock()
	var changed []*session.Instance
	live := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		live[inst.ID] = struct{}{}
		if !session.HookStatusTool(inst.Tool) || !h.isPolledByMe(inst.ID) {
			continue
		}
		hs := h.hookWatcher.GetHookStatus(inst.ID)
		if hs == nil {
			continue
		}
		key := hookSampleKey(hs)
		if key == h.hookPassSeen[inst.ID] {
			continue
		}
		h.hookPassSeen[inst.ID] = key
		inst.UpdateHookStatus(hs)
		before := inst.GetStatusThreadSafe()
		_ = inst.UpdateStatus()
		if inst.GetStatusThreadSafe() != before {
			changed = append(changed, inst)
		}
	}
	for id := range h.hookPassSeen {
		if _, ok := live[id]; !ok {
			delete(h.hookPassSeen, id)
		}
	}
	if len(changed) == 0 {
		return
	}
	if db := statedb.GetGlobal(); db != nil {
		_ = h.persistPolledStatuses(db, changed)
	}
	h.cachedStatusCounts.valid.Store(false)
	h.publishWebSessionStates(instances)
	h.refreshSessionRenderSnapshot(instances)
	// Repaint now: the rows read the snapshot, but nothing else wakes the
	// UI until the next tick.
	if h.statusRepaint != nil {
		select {
		case h.statusRepaint <- struct{}{}:
		default:
		}
	}
}

type statusRepaintMsg struct{}

func listenForStatusRepaint(ch chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return statusRepaintMsg{}
	}
}

// runPendingHookPass runs a hook pass the UI asked for while the worker was
// busy. Status worker goroutine only.
func (h *Home) runPendingHookPass() {
	if h.hookPending.Load() {
		h.processHookPass()
	}
}

// hookSampleKey identifies one hook sample. Hook files carry whole-second
// times, so two events in one second (a prompt, then its stop) share
// UpdatedAt: the hook's own generation and sequence tell them apart.
func hookSampleKey(hs *session.HookStatus) string {
	return fmt.Sprintf("%d|%s|%d|%s|%s", hs.UpdatedAt.UnixNano(), hs.HookGeneration, hs.Sequence, hs.Status, hs.Event)
}
