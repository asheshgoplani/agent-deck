package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Comms Ledger delivery (P2 canary, docs/comms.md "Delivery"). A Claude
// parent listed in [comms] consumers keeps the unchanged inbox path:
// every record is stored, and inbox wakes, digests and Stop blocks run.
//
//   - UserPromptSubmit injects pending ledger news, then drains the inbox
//     in the rest of one 9000-byte context budget. Status edges and reply
//     copies addressed to the tagged sender stay on the inbox path.
//   - Exact transcript turn keys deduplicate the two paths in both
//     directions. Done(true) remembers keys shown by either prompt path;
//     matching text or a recent turn from the same child is not proof.
//   - Only ledger-only urgent records (such as a failed async delivery or
//     the wake-cap error), never turns or sends, trigger a ledger wake or
//     Stop block. Otherwise LedgerStopDecision runs the inbox Stop drain
//     with the shown-turn filter and returns its decision.
//   - Automatic ledger wakes pause after comms.MaxAutoWakes without a
//     prompt of the parent's own. There is no ledger digest; the inbox
//     digest covers turns. Leaving the list drains what the ledger owed.

const (
	// commsWakeDebounce: at most one automatic wake per parent per minute.
	commsWakeDebounce = time.Minute
	// commsWakeSettle: a wake whose records no prompt showed within this,
	// while the parent is idle again, did not start a turn.
	commsWakeSettle = 90 * time.Second
	// commsAttemptStale: a prompt or Stop attempt never confirmed as
	// printed (the hook died) returns to pending after this.
	commsAttemptStale = 2 * time.Minute
	// commsMaxWakeTries: a record whose wake did not take this many times
	// waits for the prompt path instead of waking again.
	commsMaxWakeTries = 2
	// commsContextBudget keeps injected context under Claude's
	// additionalContext limit, as the inbox prompt drain does.
	commsContextBudget = promptContextBudgetBytes
	// commsLedgerContextBudget is the ledger's share of it; the inbox's
	// records get the rest of the same budget.
	commsLedgerContextBudget = 6000
)

// commsNow is the delivery clock (a test seam).
var commsNow = time.Now

// commsConsumersOverride is a test seam for [comms] consumers.
var commsConsumersOverride atomic.Pointer[[]string]

// SetCommsConsumersForTest forces [comms] consumers for the current test.
func SetCommsConsumersForTest(list []string) func() {
	prev := commsConsumersOverride.Swap(&list)
	return func() { commsConsumersOverride.Store(prev) }
}

func commsConsumerList() []string {
	if v := commsConsumersOverride.Load(); v != nil {
		return *v
	}
	cfg, _ := LoadUserConfig()
	if cfg == nil {
		return nil
	}
	return cfg.Comms.Consumers
}

// commsConsumes reports whether the ledger delivers to inst: the ledger is
// on, inst is listed (id, title or "*") and its harness has the delivery
// path the canary needs: prompt-time injection, a Stop hook and a typed
// wake. That is Claude in this phase; a Codex parent (no prompt hook is
// installed for it) stays on the inbox, while Codex children are recorded
// with their text like any other.
// A title selects a parent only when exactly one session of the profile
// carries it (titles is the profile's title -> count), so a second
// session named like the conductor is never enrolled by accident.
func commsConsumes(inst *Instance, list []string, titles map[string]int) bool {
	if inst == nil || len(list) == 0 || !IsClaudeCompatible(inst.Tool) {
		return false
	}
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v == "*" || v == inst.ID || (v != "" && v == inst.Title && titles[v] == 1) {
			return true
		}
	}
	return false
}

// CommsEnrollment is the marker the daemon writes for an enrolled parent
// (<runtime>/comms/consumers/<id>.json): the hooks find the ledger through
// it without loading the registry or the config's consumer list.
//
// The inbox keeps all records throughout enrollment and removal. When the
// parent leaves the list (or the ledger is turned off), the marker turns
// Draining: its hooks still deliver ledger records signalled before it
// left, allowing commsDrainGrace for lagging spools, then remove the marker.
type CommsEnrollment struct {
	Profile      string `json:"profile"`
	Dir          string `json:"dir"`
	Tool         string `json:"tool"`
	At           int64  `json:"at"`                      // Unix ms of enrollment
	EnrollCursor uint64 `json:"enroll_cursor,omitempty"` // the ledger end at enrollment (a lost state is rebuilt here)
	Draining     bool   `json:"draining,omitempty"`
	DrainedAt    int64  `json:"drained_at,omitempty"` // Unix ms the parent left the canary
}

// commsTransitionGrace covers the lag between a turn reaching the inbox
// and reaching the ledger (the next poll or two) at both ends of a
// parent's time in the canary.
const commsTransitionGrace = 2 * time.Minute

// commsDrainGrace is how long a parent that left the canary keeps its
// marker after its last owed record, for turns signalled before it left
// but committed to the ledger later (a lagging spool).
const commsDrainGrace = 10 * time.Minute

// ledgerNews reports whether a pending record is the ledger's to deliver to
// an enrolled consumer. Status edges and the copy of a reply addressed to
// the tagged sender keep the inbox path (the inbox holds them, wakes for
// them and, for a reply, treats them as urgent), so the ledger leaves them.
// Queued failures also stay on the inbox path after its durable write succeeds.
func ledgerNews(r comms.Record, consumer string) bool {
	if r.Kind == comms.KindStatus || (r.Kind == comms.KindDelivery && r.Trigger == "inbox" && r.Origin == "") {
		return false
	}
	if r.ReplyTo == consumer && len(r.To) > 0 && r.To[0] != consumer {
		return false
	}
	return true
}

// splitNews returns the pending records that are news and the cursors of
// the ones that are not (acknowledged silently).
func splitNews(pending []comms.Exported, consumer string) ([]comms.Exported, []events.Cursor) {
	var news []comms.Exported
	var skip []events.Cursor
	for _, e := range pending {
		if ledgerNews(e.Record, consumer) {
			news = append(news, e)
		} else {
			skip = append(skip, e.Cursor)
		}
	}
	return news, skip
}

func commsEnrollmentDir() string {
	return runtimeDirOrTemp(filepath.Join("comms", "consumers"))
}

func commsEnrollmentPath(id string) string {
	return filepath.Join(commsEnrollmentDir(), sanitizeInboxName(id)+".json")
}

// LoadCommsEnrollment returns the parent's marker, enrolled or draining.
func LoadCommsEnrollment(id string) (CommsEnrollment, bool) {
	if strings.TrimSpace(id) == "" {
		return CommsEnrollment{}, false
	}
	data, err := os.ReadFile(commsEnrollmentPath(id))
	if err != nil {
		return CommsEnrollment{}, false
	}
	var en CommsEnrollment
	if json.Unmarshal(data, &en) != nil || en.Dir == "" {
		return CommsEnrollment{}, false
	}
	return en, true
}

func writeCommsEnrollment(id string, en CommsEnrollment) error {
	data, err := json.Marshal(en)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(commsEnrollmentDir(), 0o700); err != nil {
		return err
	}
	return writeFileDurable(commsEnrollmentPath(id), data, 0o600)
}

// commsDeliversTo reports whether id is an enrolled (not draining) ledger
// consumer with the ledger on. Enrollment never suppresses inbox writes.
func commsDeliversTo(id string) bool {
	en, ok := LoadCommsEnrollment(id)
	return ok && !en.Draining && CommsLedgerEnabled()
}

// commsAnyEnrollment reports whether any marker exists (the daemon's cheap
// exit when the ledger is off).
func commsAnyEnrollment() bool {
	entries, err := os.ReadDir(commsEnrollmentDir())
	return err == nil && len(entries) > 0
}

// deliverCommsLedger runs the delivery pass for every listed parent of the
// profile, after the ingest pass committed this poll's records, and moves
// parents in and out of the canary.
func (d *TransitionDaemon) deliverCommsLedger(profile string, byID map[string]*Instance, statuses map[string]string) {
	ledgerOn := CommsLedgerEnabled()
	if !ledgerOn && !commsAnyEnrollment() {
		return
	}
	var list []string
	if ledgerOn {
		list = commsConsumerList()
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	names := comms.Names{}
	titles := map[string]int{}
	for _, id := range ids {
		if inst := byID[id]; inst != nil {
			names[id] = inst.Title
			titles[inst.Title]++
		}
	}
	var l *comms.Ledger
	for _, id := range ids {
		inst := byID[id]
		want := ledgerOn && commsConsumes(inst, list, titles)
		en, has := LoadCommsEnrollment(id)
		if has && en.Profile != profile {
			continue
		}
		if has && (!want || en.Draining) {
			commsRebuildIfBroken(id, en)
		}
		switch {
		case want && (!has || en.Draining):
			if l == nil {
				if l = d.commsLedgerFor(profile); l == nil {
					return
				}
			}
			r := l.Reader()
			end, _, err := r.Bus.Ends()
			if err != nil {
				continue
			}
			// A parent re-listed while it still drains keeps what the ledger
			// owes it; what the inbox delivered after it left (committed past
			// the drain grace) is acknowledged. A new one starts at the end
			// (the inbox delivered what came before).
			if _, err := r.Enroll(id, has); err != nil {
				commsLog.Warn("comms_enroll_failed", slog.String("parent", id), slog.String("error", err.Error()))
				continue
			}
			if has {
				_, _ = r.Do(id, func(p comms.Pass) ([]events.Cursor, error) {
					var acks []events.Cursor
					for _, e := range p.Pending {
						signal := e.Record.TSignal
						if signal == 0 {
							signal = e.Record.TRecord
						}
						if signal > en.DrainedAt {
							acks = append(acks, e.Cursor)
						}
					}
					return acks, nil
				})
			}
			if err := writeCommsEnrollment(id, CommsEnrollment{Profile: profile, Dir: r.Dir, Tool: commsToolName(inst),
				At: commsNow().UnixMilli(), EnrollCursor: uint64(end)}); err != nil {
				commsLog.Warn("comms_enroll_failed", slog.String("parent", id), slog.String("error", err.Error()))
				continue
			}
			commsLog.Info("comms_consumer_enrolled", slog.String("parent", id))
		case !want && has && !en.Draining:
			d.startCommsDrain(id, en)
			continue
		case !want:
			continue
		}
		if l == nil {
			if l = d.commsLedgerFor(profile); l == nil {
				return
			}
		}
		if en, ok := LoadCommsEnrollment(id); ok {
			commsRebuildIfBroken(id, en)
		}
		d.deliverToConsumer(l, l.Reader(), profile, inst, statuses[id], names)
	}
}

// commsRebuildIfBroken rebuilds an enrolled or draining parent's state at
// its enrollment point when it is unreadable or missing (a crash, a disk
// fault): the hooks never recreate it themselves, so the records the
// ledger owes are re-delivered (at least once), never stranded.
func commsRebuildIfBroken(id string, en CommsEnrollment) {
	r, err := comms.OpenReaderAt(en.Dir)
	if err != nil {
		return
	}
	defer r.Close()
	if _, found, err := comms.ReadConsumer(r.Dir, id); err == nil && found {
		return
	}
	if _, err := r.Rebuild(id, events.Cursor(en.EnrollCursor)); err != nil {
		commsLog.Warn("comms_state_rebuild_failed", slog.String("parent", id), slog.String("error", err.Error()))
		return
	}
	commsLog.Warn("comms_state_rebuilt", slog.String("parent", id), slog.Uint64("from", en.EnrollCursor))
}

// startCommsDrain hands an unlisted parent back to the inbox: its marker
// turns Draining, so the records the ledger still owes it (committed up to
// now plus the grace) are delivered by its hooks and none is lost.
func (d *TransitionDaemon) startCommsDrain(id string, en CommsEnrollment) {
	if r, err := comms.OpenReaderAt(en.Dir); err == nil {
		if _, err := r.Unenroll(id); err != nil {
			commsLog.Warn("comms_unenroll_failed", slog.String("parent", id), slog.String("error", err.Error()))
		}
		_ = r.Close()
	}
	en.Draining, en.DrainedAt = true, commsNow().UnixMilli()
	if err := writeCommsEnrollment(id, en); err != nil {
		commsLog.Warn("comms_unenroll_failed", slog.String("parent", id), slog.String("error", err.Error()))
		return
	}
	commsLog.Info("comms_consumer_unenrolled", slog.String("parent", id))
}

// deliverToConsumer settles stale attempts and, when the parent is idle
// and something warrants it, types one combined wake.
func (d *TransitionDaemon) deliverToConsumer(l *comms.Ledger, r *comms.Reader, profile string, inst *Instance, status string, names comms.Names) {
	id := inst.ID
	// Only an idle parent is ever woken, and only an idle parent can show
	// that a wake did not take: a busy, stopped or unknown one is left to
	// its own hooks, and its state is not touched (a pass is activity).
	idle := status == string(StatusIdle) || status == string(StatusWaiting)
	if !idle {
		return
	}
	now := commsNow()
	if f, found, _ := comms.ReadConsumer(r.Dir, id); found {
		// Nothing new since the last pass and no deadline due: skip the
		// read entirely (a parent with records waiting for its own prompt
		// costs two small file reads per poll, not a scan).
		flag, _ := comms.ReadFlag(r.Dir, id)
		if flag.Last <= f.Through && (f.NextDue == 0 || now.UnixMilli() < f.NextDue) {
			return
		}
	}
	var line string
	var carried []comms.Exported
	capped := false
	_, err := r.Do(id, func(p comms.Pass) ([]events.Cursor, error) {
		var acks []events.Cursor
		f := p.File
		var wakeFailed []events.Cursor
		f.Settle(func(in comms.Inflight) bool {
			age := now.UnixMilli() - in.At
			if in.Via == comms.ViaWake {
				if age > commsWakeSettle.Milliseconds() {
					wakeFailed = append(wakeFailed, in.Cursor)
					return true
				}
				return false
			}
			return in.State == comms.ReceiptAttempted && age > commsAttemptStale.Milliseconds()
		})
		f.NoteWakeFailed(wakeFailed)
		news, skip := splitNews(p.Pending, id)
		acks = skip
		// The inbox, which keeps every record, wakes for every turn; the
		// ledger wakes only for urgent records the inbox never holds (a
		// queued send's failure returned to its sender, the wake-cap note).
		var pending []comms.Exported
		for _, e := range news {
			if _, inflight := f.InflightFor(e.Cursor); !inflight && ledgerOnlyUrgent(e.Record) && f.WakeTriesFor(e.Cursor) < commsMaxWakeTries {
				pending = append(pending, e)
			}
		}
		defer func() { f.NextDue = commsNextDue(f, pending) }()
		if len(pending) == 0 {
			return acks, nil
		}
		if f.AutoWakes >= comms.MaxAutoWakes {
			if !f.CapNoted {
				f.CapNoted, capped = true, true
			}
			return acks, nil
		}
		if f.LastWake > 0 && now.UnixMilli()-f.LastWake < commsWakeDebounce.Milliseconds() {
			return acks, nil
		}
		var whole, preview []comms.Exported
		line, whole, preview = comms.WakeLine(pending, names, comms.WakeLineBytes)
		carried = append(append([]comms.Exported{}, whole...), preview...)
		if len(carried) == 0 {
			line = "" // a wake that carries no record is never typed
			return acks, nil
		}
		if err := d.commsWakeSend(inst, profile, line); err != nil {
			commsLog.Warn("comms_wake_send_failed", slog.String("parent", id), slog.String("error", err.Error()))
			line, carried = "", nil
			return acks, nil
		}
		f.Carry(carried, comms.ViaWake, now.UnixMilli())
		f.MarkPreview(exportedCursors(preview))
		f.Accept(exportedCursors(carried))
		f.LastWake = now.UnixMilli()
		f.AutoWakes++
		return acks, nil
	})
	if err != nil {
		commsLog.Warn("comms_deliver_failed", slog.String("parent", id), slog.String("error", err.Error()))
		return
	}
	if line != "" {
		if _, _, err := l.Commit(comms.Record{Kind: comms.KindWake, From: "agent-deck", To: []string{id}, Profile: profile,
			Trigger: "ledger", Via: "tmux", State: comms.StateTyped, Text: line, Refs: exportedIDs(carried),
			TSignal: now.UnixMilli()}); err != nil {
			commsLog.Warn("comms_wake_record_failed", slog.String("parent", id), slog.String("error", err.Error()))
		}
	}
	if capped {
		text := fmt.Sprintf("automatic wakes paused after %d without a turn of your own; pending records arrive with your next prompt", comms.MaxAutoWakes)
		if _, _, err := l.Commit(comms.Record{Kind: comms.KindError, From: "agent-deck", To: []string{id}, Profile: profile,
			Tier: comms.TierInfo, Err: text, Text: text}); err != nil {
			commsLog.Warn("comms_cap_record_failed", slog.String("parent", id), slog.String("error", err.Error()))
		}
	}
}

// ledgerOnlyUrgent reports an urgent record the inbox never holds, the
// only kind the ledger wakes a parent for: anything but a child's turn (a
// queued send's failure returned to its sender, the wake-cap note).
func ledgerOnlyUrgent(r comms.Record) bool {
	return r.Kind != comms.KindTurn && r.Kind != comms.KindSend && r.IsUrgent()
}

// commsNextDue is the earliest time a pass could act without a new record:
// a wake attempt settling, a stale attempt expiring, or the debounce ending
// for a pending urgent record. 0 when nothing is due (the next record
// wakes the pass).
func commsNextDue(f *comms.ConsumerFile, pending []comms.Exported) int64 {
	var due int64
	note := func(t int64) {
		if t > 0 && (due == 0 || t < due) {
			due = t
		}
	}
	for _, in := range f.Inflight {
		if in.Via == comms.ViaWake {
			note(in.At + commsWakeSettle.Milliseconds() + 1)
		} else if in.State == comms.ReceiptAttempted {
			note(in.At + commsAttemptStale.Milliseconds() + 1)
		}
	}
	if f.AutoWakes < comms.MaxAutoWakes && len(pending) > 0 {
		note(f.LastWake + commsWakeDebounce.Milliseconds())
	}
	return due
}

// commsWakeSend types the wake line through the same wiring the inbox
// uses (a detached `session send --no-wait`); tests swap the wiring.
func (d *TransitionDaemon) commsWakeSend(parent *Instance, profile, line string) error {
	if d.notifier != nil && d.notifier.wake != nil && d.notifier.wake.send != nil {
		return d.notifier.wake.send(parent, profile, line)
	}
	return sendWakeNudge(parent, profile, line)
}

func exportedCursors(es []comms.Exported) []events.Cursor {
	out := make([]events.Cursor, 0, len(es))
	for _, e := range es {
		out = append(out, e.Cursor)
	}
	return out
}

func exportedIDs(es []comms.Exported) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Record.ID)
	}
	return out
}

// commsAckPrompt acknowledges, for an enrolled parent, the records a
// prompt the harness reported (a prompt-start spool edge) showed: the ids
// a wake line typed. This is how a Codex parent's wake is confirmed. A
// prompt of the parent's own resets its automatic wake count.
func (d *TransitionDaemon) commsAckPrompt(l *comms.Ledger, inst *Instance, prompt string) {
	if !commsDeliversTo(inst.ID) {
		return
	}
	// A parent with no prompt-time injection (Codex) only ever gets the
	// wake line, so a preview it was shown is its delivery.
	acceptPreview := !IsClaudeCompatible(inst.Tool)
	if _, err := l.Reader().Do(inst.ID, func(p comms.Pass) ([]events.Cursor, error) {
		return ackShownInPrompt(p, prompt, acceptPreview), nil
	}); err != nil {
		commsLog.Warn("comms_prompt_ack_failed", slog.String("parent", inst.ID), slog.String("error", err.Error()))
	}
}

// ackShownInPrompt returns the pending records a wake prompt named and
// settles their wake attempts; a prompt of the parent's own resets the
// automatic wake count.
func ackShownInPrompt(p comms.Pass, prompt string, acceptPreview bool) []events.Cursor {
	f := p.File
	if !comms.IsWakePrompt(prompt) {
		f.AutoWakes, f.CapNoted = 0, false
		return nil
	}
	// Only records a wake actually carried count, and only when the
	// prompt names them: the line also holds children's own text, which
	// may quote any "#XXXXXX", so an id alone never acknowledges a record
	// the line did not show.
	ids := comms.IDsIn(prompt)
	var acks []events.Cursor
	for _, e := range p.Pending {
		in, inflight := f.InflightFor(e.Cursor)
		if inflight && in.Via == comms.ViaWake && in.ID == e.Record.ID && ids[comms.ShortID(e.Record.ID)] && (!in.Preview || acceptPreview) {
			acks = append(acks, e.Cursor)
		}
	}
	shown := map[events.Cursor]bool{}
	for _, c := range acks {
		shown[c] = true
	}
	f.Settle(func(in comms.Inflight) bool { return shown[in.Cursor] })
	return acks
}

// --- hook side -------------------------------------------------------------

// LedgerDelivery is one hook's delivery: the text to print and a Done to
// call after printing (true when the text reached stdout), which records
// that the transport took the records. Done is safe to call once.
type LedgerDelivery struct {
	Text      string
	Decision  StopHookDecision
	Blocked   bool
	r         *comms.Reader
	id        string
	carried   []events.Cursor
	shownKeys []string // turn keys shown by this delivery (either path)
	onShown   func()
}

// Done finishes the delivery: shown records become transport_accepted.
func (dl *LedgerDelivery) Done(printed bool) {
	if dl == nil || dl.r == nil {
		return
	}
	defer func() { _ = dl.r.Close(); dl.r = nil }()
	if !printed || (len(dl.carried) == 0 && len(dl.shownKeys) == 0) {
		return
	}
	if _, err := dl.r.Do(dl.id, func(p comms.Pass) ([]events.Cursor, error) {
		p.File.Accept(dl.carried)
		p.File.NoteShown(dl.shownKeys...)
		return nil, nil
	}); err != nil {
		commsLog.Warn("comms_delivery_accept_failed", slog.String("parent", dl.id), slog.String("error", err.Error()))
		return
	}
	if dl.onShown != nil {
		dl.onShown()
	}
}

// openLedgerFor opens the enrolled parent's ledger; ok false means the
// inbox path applies (not enrolled, or the ledger cannot be read, in which
// case the inbox, still written for the parent, delivers).
func openLedgerFor(id string) (*comms.Reader, CommsEnrollment, bool) {
	en, ok := LoadCommsEnrollment(id)
	if !ok {
		return nil, en, false
	}
	r, err := comms.OpenReaderAt(en.Dir)
	if err != nil {
		commsLog.Warn("comms_ledger_unreadable_using_inbox", slog.String("parent", id), slog.String("error", err.Error()))
		return nil, en, false
	}
	if _, found, err := comms.ReadConsumer(r.Dir, id); err != nil || !found {
		// Never recreated here (it would start at the end and skip what is
		// owed): the daemon rebuilds it at the enrollment point.
		_ = r.Close()
		return nil, en, false
	}
	return r, en, true
}

// commsNames maps the profile's session ids to titles for rendering.
func commsNames(profile string) comms.Names {
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		return nil
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return nil
	}
	names := comms.Names{}
	for _, inst := range instances {
		if inst != nil {
			names[inst.ID] = inst.Title
		}
	}
	return names
}

// settleTurnAttempts acknowledges the prompt and Stop attempts whose
// records were printed (the turn that carried them ran) and returns the
// ones never printed to pending.
func settleTurnAttempts(f *comms.ConsumerFile) []events.Cursor {
	acks := f.Settle(func(in comms.Inflight) bool {
		return in.Via != comms.ViaWake && in.State == comms.ReceiptTransportAccepted
	})
	f.Settle(func(in comms.Inflight) bool { return in.Via != comms.ViaWake && in.State == comms.ReceiptAttempted })
	return acks
}

// selectContext picks pending records for injection: urgent first, then
// the rest, oldest first within each, while the rendered lines fit budget.
func selectContext(pending []comms.Exported, names comms.Names, budget int) []comms.Exported {
	used := 300
	var take []comms.Exported
	for _, wantUrgent := range []bool{true, false} {
		for _, e := range pending {
			if e.Record.IsUrgent() != wantUrgent {
				continue
			}
			n := len(comms.QuotedContextLine(e.Record, names)) + 1
			if used+n > budget && len(take) > 0 {
				continue
			}
			used += n
			take = append(take, e)
		}
	}
	sort.Slice(take, func(i, j int) bool { return take[i].Cursor < take[j].Cursor })
	return take
}

func renderContext(header string, take []comms.Exported, left int, names comms.Names) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	for _, e := range take {
		b.WriteString(comms.QuotedContextLine(e.Record, names))
		b.WriteByte('\n')
	}
	if left > 0 {
		fmt.Fprintf(&b, "%d more record(s) stay pending for your next turn (or now: agent-deck msg read).\n", left)
	}
	return b.String()
}

// drainScope says which ledger records a parent's hooks still deliver: all
// of them while it is enrolled; while it drains (it left the list) the ones
// signalled before it left, and graceOver says a lagging one can no longer
// arrive; with the ledger turned off, everything pending.
func drainScope(en CommsEnrollment) (inScope func(comms.Record) bool, draining, graceOver bool) {
	if !en.Draining && CommsLedgerEnabled() {
		return func(comms.Record) bool { return true }, false, false
	}
	if !en.Draining {
		// The ledger was turned off: nothing new arrives, all pending is owed.
		return func(comms.Record) bool { return true }, true, true
	}
	// Owed: every turn signalled before the parent left, however late the
	// ledger committed it; a turn signalled after went to the inbox.
	owed := func(r comms.Record) bool {
		at := r.TSignal
		if at == 0 {
			at = r.TRecord
		}
		return at <= en.DrainedAt
	}
	return owed, true, commsNow().UnixMilli() > en.DrainedAt+commsDrainGrace.Milliseconds()
}

// LedgerPromptContext is the prompt hook of a parent with a ledger
// marker. It acknowledges the records the prompt showed (a wake line names
// them) and the records injected into the previous turn, and returns the
// rest as context, followed by the inbox drain within one shared budget.
// Exact transcript turn keys suppress duplicates in both directions.
// A draining parent gets only what the ledger still
// owed it; once that is delivered its marker is removed. ok is false when
// the inbox path alone applies (no marker, or the ledger cannot be read:
// nothing is consumed then and the caller drains the inbox as before).
func LedgerPromptContext(id, prompt string) (*LedgerDelivery, bool) {
	r, en, ok := openLedgerFor(id)
	if !ok {
		return nil, false
	}
	inScope, draining, graceOver := drainScope(en)
	dl := &LedgerDelivery{r: r, id: id}
	var names comms.Names
	var shownBefore []string
	now := commsNow().UnixMilli()
	finished := false
	_, err := r.Do(id, func(p comms.Pass) ([]events.Cursor, error) {
		f := p.File
		acks := settleTurnAttempts(f)
		acks = append(acks, ackShownInPrompt(p, prompt, false)...)
		news, skip := splitNews(p.Pending, id)
		acks = append(acks, skip...)
		done := map[events.Cursor]bool{}
		for _, c := range acks {
			done[c] = true
		}
		var rest []comms.Exported
		for _, e := range news {
			if done[e.Cursor] || !inScope(e.Record) {
				continue
			}
			if e.Record.Kind == comms.KindTurn && f.WasShown(e.Record.Key) {
				// The inbox already showed this exact turn: never twice.
				acks, done[e.Cursor] = append(acks, e.Cursor), true
				continue
			}
			rest = append(rest, e)
		}
		shownBefore = append([]string(nil), f.Shown...)
		if len(rest) == 0 {
			finished = draining && graceOver && len(f.Inflight) == 0
			return acks, nil
		}
		f.NextDue = 1 // records stay pending: the daemon's next pass looks again
		names = commsNames(en.Profile)
		take := selectContext(rest, names, commsLedgerContextBudget)
		f.Carry(take, comms.ViaPrompt, now)
		dl.carried = exportedCursors(take)
		for _, e := range take {
			if e.Record.Kind == comms.KindTurn {
				dl.shownKeys = append(dl.shownKeys, e.Record.Key)
			}
		}
		dl.Text = renderContext(fmt.Sprintf("%s %d record(s) from your children (%s; act on each, no need to re-read the child):",
			comms.WakePrefix, len(take), comms.DataNote), take, len(rest)-len(take), names)
		return acks, nil
	})
	if err != nil {
		commsLog.Warn("comms_prompt_delivery_failed", slog.String("parent", id), slog.String("error", err.Error()))
		_ = r.Close()
		return nil, false
	}
	if finished {
		_ = os.Remove(commsEnrollmentPath(id))
		commsLog.Info("comms_consumer_drained", slog.String("parent", id))
	}
	// The inbox keeps every record and runs as it always does, in what is
	// left of the one context budget; a turn the ledger showed (now or
	// before, matched by its exact identity) is retired unshown.
	shown := commsShownMatcher(append(shownBefore, dl.shownKeys...))
	drained, evs, derr := drainForPrompt(id, max(commsContextBudget-len(dl.Text), 1000), shown)
	if derr == nil && drained != "" {
		if dl.Text != "" {
			dl.Text += "\n"
		}
		dl.Text += drained
	}
	for _, ev := range evs {
		dl.shownKeys = append(dl.shownKeys, inboxTurnKey(ev))
	}
	return dl, true
}

// inboxTurnKey is the ledger key of the turn an inbox record carries: the
// key the ledger commits a transcript-classified turn under. "" when the
// record is not provably that exact local transcript turn.
func inboxTurnKey(ev TransitionNotificationEvent) string {
	if strings.TrimSpace(ev.TurnUUID) == "" || ev.OutputHashStale ||
		strings.EqualFold(strings.TrimSpace(ev.ToStatus), "error") || ev.SourceRemote != "" {
		return ""
	}
	// TurnFingerprint uses the plain turn/finished form only when a signal
	// exists. Require the transcript UUID signal, not a stale pane hash or
	// a completion without a turn, and reject alternate fingerprints.
	if strings.TrimSpace(ev.LastOutputHash) != (TurnFacts{UUID: ev.TurnUUID}).Signal() ||
		(ev.TurnFingerprint != "" && ev.TurnFingerprint != TurnFingerprint(ev)) {
		return ""
	}
	return comms.Key(comms.KindTurn, ev.ChildSessionID, ev.TurnUUID)
}

func commsShownMatcher(keys []string) func(TransitionNotificationEvent) bool {
	set := map[string]bool{}
	for _, k := range keys {
		if k != "" {
			set[k] = true
		}
	}
	return func(ev TransitionNotificationEvent) bool { return set[inboxTurnKey(ev)] }
}

// LedgerStopDecision is the Stop hook of a parent with a ledger marker:
// the turn that just ended confirms the records it carried; the ledger
// blocks only for an urgent record the inbox never holds; otherwise the
// inbox's own Stop drain runs (same budget), leaving out turns the ledger
// already showed. ok is false when the inbox path alone applies.
func LedgerStopDecision(id string, stopHookActive bool) (*LedgerDelivery, bool) {
	r, en, ok := openLedgerFor(id)
	if !ok {
		return nil, false
	}
	inScope, _, _ := drainScope(en)
	dl := &LedgerDelivery{r: r, id: id}
	now := commsNow().UnixMilli()
	var shownBefore []string
	_, err := r.Do(id, func(p comms.Pass) ([]events.Cursor, error) {
		f := p.File
		acks := settleTurnAttempts(f)
		news, skip := splitNews(p.Pending, id)
		acks = append(acks, skip...)
		done := map[events.Cursor]bool{}
		for _, c := range acks {
			done[c] = true
		}
		var rest []comms.Exported
		urgent := false
		defer func() {
			if len(rest) > 0 {
				f.NextDue = 1 // the daemon's next pass looks again
			}
		}()
		for _, e := range news {
			if e.Record.Kind == comms.KindTurn && f.WasShown(e.Record.Key) {
				acks = append(acks, e.Cursor)
				continue
			}
			if _, inflight := f.InflightFor(e.Cursor); inflight || done[e.Cursor] || !inScope(e.Record) {
				continue
			}
			rest = append(rest, e)
			urgent = urgent || ledgerOnlyUrgent(e.Record)
		}
		shownBefore = append([]string(nil), f.Shown...)
		if !urgent || f.AutoWakes >= comms.MaxAutoWakes || !reserveStopBlock(id, stopHookActive) {
			return acks, nil
		}
		names := commsNames(en.Profile)
		take := selectContext(rest, names, commsLedgerContextBudget)
		f.Carry(take, comms.ViaStop, now)
		f.AutoWakes++
		dl.carried = exportedCursors(take)
		for _, e := range take {
			if e.Record.Kind == comms.KindTurn {
				dl.shownKeys = append(dl.shownKeys, e.Record.Key)
			}
		}
		reason := renderContext(fmt.Sprintf("%s %d record(s) arrived while you were busy (urgent first; %s; act on each):",
			comms.WakePrefix, len(take), comms.DataNote), take, len(rest)-len(take), names)
		dl.Decision, dl.Blocked, dl.Text = StopHookDecision{Decision: "block", Reason: reason}, true, reason
		dl.onShown = func() { SpoolCommsWake(id, "ledger", "stop", reason, "") }
		return acks, nil
	})
	if err != nil {
		commsLog.Warn("comms_stop_delivery_failed", slog.String("parent", id), slog.String("error", err.Error()))
		_ = r.Close()
		return nil, false
	}
	if !dl.Blocked {
		dec, blocked, evs, derr := drainForStopHook(id, stopHookActive, commsShownMatcher(shownBefore))
		if derr == nil && blocked {
			dl.Decision, dl.Blocked, dl.Text = dec, true, dec.Reason
			for _, ev := range evs {
				dl.shownKeys = append(dl.shownKeys, inboxTurnKey(ev))
			}
			dl.onShown = func() { SpoolCommsWake(id, "inbox", "stop", dec.Reason, "") }
		}
	}
	return dl, true
}

// reserveStopBlock applies the inbox's consecutive Stop-block budget
// (MaxStopHookBlocks; a fresh user turn resets it) and reserves one block
// durably before the hook prints, so a counter that cannot be saved never
// blocks.
func reserveStopBlock(id string, stopHookActive bool) bool {
	stopBlockMu.Lock()
	defer stopBlockMu.Unlock()
	count := loadStopBlockCountLocked(id)
	if !stopHookActive {
		count = 0
	}
	if count >= MaxStopHookBlocks {
		return false
	}
	if err := saveStopBlockCountLocked(id, count+1); err != nil {
		commsLog.Warn("comms_stop_block_counter_failed", slog.String("parent", id), slog.String("error", err.Error()))
		return false
	}
	return true
}
