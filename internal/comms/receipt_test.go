package comms

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// The consumer contract is pinned by fixtures under testdata so P2, P3 and
// a workflow runner build on one frozen shape.

func TestReceiptTransitionsMatchFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/receipt_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Transitions []struct {
			From, To string
			OK       bool
		}
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	for _, tr := range fx.Transitions {
		r := Receipt{MessageID: "m", Recipient: "p", Generation: 1, Attempt: 1, State: tr.From}
		got, err := r.Advance(tr.To, 10)
		if (err == nil) != tr.OK {
			t.Errorf("%q -> %q: ok=%v err=%v", tr.From, tr.To, err == nil, err)
			continue
		}
		if err == nil && (got.State != tr.To || got.At != 10) {
			t.Errorf("%q -> %q: advanced to %+v", tr.From, tr.To, got)
		}
	}
	failed := Receipt{State: ReceiptFailed, Attempt: 2}
	next := failed.Retry(20)
	if next.Attempt != 3 || next.State != ReceiptDurable || next.At != 20 {
		t.Fatalf("Retry: %+v", next)
	}
}

func TestConsumerStateWatermarkAndSparseAcksMatchFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/consumer_state_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Steps []struct {
			Ack  uint64
			Want struct {
				Watermark uint64
				Acked     []uint64
			}
		}
		PendingAfter struct {
			Records []uint64
			Want    []uint64
		} `json:"pending_after"`
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	var c ConsumerState
	for i, st := range fx.Steps {
		if err := c.Ack(events.Cursor(st.Ack)); err != nil {
			t.Fatalf("step %d ack %d: %v", i, st.Ack, err)
		}
		var acked []uint64
		for _, a := range c.Acked {
			acked = append(acked, uint64(a))
		}
		if uint64(c.Watermark) != st.Want.Watermark || !reflect.DeepEqual(acked, st.Want.Acked) {
			t.Fatalf("step %d (ack %d): watermark %d acked %v, want %d %v", i, st.Ack, c.Watermark, acked, st.Want.Watermark, st.Want.Acked)
		}
	}
	var recs []Exported
	for _, n := range fx.PendingAfter.Records {
		recs = append(recs, Exported{Cursor: events.Cursor(n)})
	}
	var got []uint64
	for _, e := range c.Pending(recs) {
		got = append(got, uint64(e.Cursor))
	}
	if !reflect.DeepEqual(got, fx.PendingAfter.Want) {
		t.Fatalf("pending %v, want %v", got, fx.PendingAfter.Want)
	}
}

func TestConsumerStateNeverSkipsPendingInfoWhenUrgentOvertakes(t *testing.T) {
	// info 10 pending, urgent 11 delivered and acknowledged: 10 stays pending.
	c := ConsumerState{Watermark: 9}
	if err := c.Ack(11); err != nil {
		t.Fatal(err)
	}
	pending := c.Pending([]Exported{{Cursor: 10}, {Cursor: 11}, {Cursor: 12}})
	if len(pending) != 2 || pending[0].Cursor != 10 || pending[1].Cursor != 12 {
		t.Fatalf("pending: %+v", pending)
	}
	if c.Watermark != 9 || !c.IsAcked(11) || c.IsAcked(10) {
		t.Fatalf("state: %+v", c)
	}
	if err := c.Ack(10); err != nil || c.Watermark != 11 || len(c.Acked) != 0 {
		t.Fatalf("after ack 10: %+v err %v", c, err)
	}
}

func TestConsumerStateBoundsSparseAcksAndChecksEpoch(t *testing.T) {
	c := ConsumerState{Watermark: 0}
	for i := 2; i < MaxSparseAcks+2; i++ {
		if err := c.Ack(events.Cursor(i)); err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
	}
	if err := c.Ack(events.Cursor(MaxSparseAcks + 2)); err != ErrAckWindow {
		t.Fatalf("window not enforced: %v", err)
	}
	if err := c.Ack(1); err != nil || c.Watermark != events.Cursor(MaxSparseAcks+1) || len(c.Acked) != 0 {
		t.Fatalf("filling the hole must collapse the set: %+v err %v", c.Watermark, err)
	}
	store := StoreIdentity{ID: "s1", Epoch: 2}
	if err := (ConsumerState{Store: "s1", Epoch: 2}).Check(store); err != nil {
		t.Fatal(err)
	}
	if err := (ConsumerState{Store: "s1", Epoch: 1}).Check(store); err != ErrEpoch {
		t.Fatalf("old epoch accepted: %v", err)
	}
	if err := (ConsumerState{Store: "other", Epoch: 2}).Check(store); err != ErrEpoch {
		t.Fatalf("other store accepted: %v", err)
	}
	if err := (ConsumerState{}).Check(store); err != nil {
		t.Fatalf("fresh state must bind to any store: %v", err)
	}
}

func TestRetentionKeepsPendingAndReportsGaps(t *testing.T) {
	states := []ConsumerState{{Consumer: "a", Watermark: 40}, {Consumer: "b", Watermark: 12}, {Consumer: "c", Watermark: 99}}
	if got := RetainFrom(states); got != 13 {
		t.Fatalf("RetainFrom = %d, want 13 (b's first pending)", got)
	}
	if RetainFrom(nil) != 0 {
		t.Fatal("no consumers: nothing to retain for delivery")
	}
	gap, ok := GapFor(ConsumerState{Consumer: "b", Watermark: 12}, 20)
	if !ok || gap.From != 13 || gap.To != 19 || gap.Consumer != "b" {
		t.Fatalf("gap: %+v ok=%v", gap, ok)
	}
	if _, ok := GapFor(ConsumerState{Watermark: 19}, 20); ok {
		t.Fatal("contiguous consumer reported a gap")
	}
	if _, ok := GapFor(ConsumerState{Watermark: 5}, 0); ok {
		t.Fatal("empty log reported a gap")
	}
}

func TestRecordCarriesSchemaVersionStoreAndEpoch(t *testing.T) {
	dir := t.TempDir() + "/ledger"
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := l.Commit(Record{Kind: KindTurn, From: "c", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	store := l.Store()
	if r.V != SchemaVersion || r.Store == "" || r.Store != store.ID || r.Epoch != 1 || store.Epoch != 1 {
		t.Fatalf("stamped %+v store %+v", r, store)
	}
	_ = l.Close()
	l2, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.Store().ID != store.ID || l2.Store().Epoch != store.Epoch {
		t.Fatalf("store identity changed across reopen: %+v vs %+v", l2.Store(), store)
	}
	// An imported record keeps the origin's store and epoch.
	remote := Record{Kind: KindTurn, From: "r", Store: "remote-store", Epoch: 3, Key: "k"}
	remote.Stamp(r2time())
	done, err := l2.Import("box", []Exported{{Cursor: 1, Record: remote}})
	if err != nil || done != 1 {
		t.Fatalf("import: %d %v", done, err)
	}
	bus, _ := OpenReaderDir(dir)
	defer bus.Close()
	recs, _, _ := ReadAfter(bus, 1, 0)
	if len(recs) != 1 || recs[0].Store != "remote-store" || recs[0].Epoch != 3 || recs[0].Origin != "box" {
		t.Fatalf("imported: %+v", recs)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("ledger dir mode %v", info.Mode().Perm())
	}
	for _, name := range []string{"active.ndjson", "writer.lock", "store.json"} {
		info, err := os.Stat(dir + "/" + name)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v err %v", name, info.Mode().Perm(), err)
		}
	}
}

// Verifier defect 5 (G6 gap rule): a cursor no record carries (a malformed
// line, a rolled-back commit) can never be delivered, so it can never be
// acknowledged. SkipSpent moves the watermark over it once every record
// before it is acknowledged, so the watermark, the sparse set and
// RetainFrom never stick on it.
func TestConsumerStateSkipsSpentCursorsMatchFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/consumer_state_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		SkipSpent struct {
			Records []uint64
			End     uint64
			Steps   []struct {
				Ack  uint64
				Want struct {
					Watermark uint64
					Acked     []uint64
				}
			}
			RetainFrom uint64 `json:"retain_from"`
		} `json:"skip_spent"`
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	fs := fx.SkipSpent
	if len(fs.Steps) == 0 {
		t.Fatal("fixture has no skip_spent steps")
	}
	var read []Exported
	for _, n := range fs.Records {
		read = append(read, Exported{Cursor: events.Cursor(n)})
	}
	var c ConsumerState
	for i, st := range fs.Steps {
		if err := c.Ack(events.Cursor(st.Ack)); err != nil {
			t.Fatalf("step %d ack %d: %v", i, st.Ack, err)
		}
		c.SkipSpent(0, read, events.Cursor(fs.End))
		var acked []uint64
		for _, a := range c.Acked {
			acked = append(acked, uint64(a))
		}
		if uint64(c.Watermark) != st.Want.Watermark || !reflect.DeepEqual(acked, st.Want.Acked) {
			t.Fatalf("step %d (ack %d): watermark %d acked %v, want %d %v", i, st.Ack, c.Watermark, acked, st.Want.Watermark, st.Want.Acked)
		}
	}
	if got := RetainFrom([]ConsumerState{c}); uint64(got) != fs.RetainFrom {
		t.Fatalf("RetainFrom %d, want %d", got, fs.RetainFrom)
	}

	// The same rule against a real ledger with a corrupt middle line: a
	// consumer that acknowledges every record it was handed ends with its
	// watermark at the end of the pass.
	dir := t.TempDir() + "/ledger"
	l, err := OpenDir("p", dir)
	if err != nil {
		t.Fatal(err)
	}
	commitTexts(t, l, "a", "b", "c")
	_ = l.Close()
	corruptLedgerLine(t, dir, 1)
	bus, err := OpenReaderDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	got, end, err := Export(bus, 0, 0)
	if err != nil || len(got) != 2 || end != 3 {
		t.Fatalf("Export = %d records, end %d, err %v", len(got), end, err)
	}
	var cs ConsumerState
	for _, e := range got {
		if err := cs.Ack(e.Cursor); err != nil {
			t.Fatal(err)
		}
	}
	cs.SkipSpent(0, got, end)
	if cs.Watermark != 3 || cs.Acked != nil || len(cs.Pending(got)) != 0 {
		t.Fatalf("after acking every record: watermark %d acked %v", cs.Watermark, cs.Acked)
	}
}
