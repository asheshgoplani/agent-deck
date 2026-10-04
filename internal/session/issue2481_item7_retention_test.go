package session

// Issue #2481 item 7: transition log retention, inbox stats that survive a
// mixed-version host, and the per-child 64-turn overflow collapsing into one
// counted digest record instead of `failed` results retried every poll.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// readTransitionLogTitles returns the child titles of every JSONL record in path, in
// file order (nil when the file does not exist).
func readTransitionLogTitles(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var ev TransitionNotificationEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("%s: undecodable record %q: %v", path, sc.Text(), err)
		}
		out = append(out, ev.ChildTitle)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func logTestEvent(seq int) TransitionNotificationEvent {
	return TransitionNotificationEvent{
		ChildSessionID: "rotation-child", ChildTitle: fmt.Sprintf("seq-%06d", seq), Profile: "p",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Unix(int64(seq), 0),
		TargetSessionID: "rotation-parent", TargetKind: "parent", DeliveryResult: transitionDeliveryCommitted,
		Text: strings.Repeat("x", 300),
	}
}

// The transition log must not grow without bound: once a record would take it
// past the retention size it rotates to <log>.1, and the rotated file is plain
// JSONL any reader can parse. Measured against the production default (8 MiB).
func TestIssue2481_TransitionLogRotatesPastDefaultSize(t *testing.T) {
	inboxTestHome(t)
	n := NewTransitionNotifier()
	const limit = 8 << 20
	line, _ := json.Marshal(logTestEvent(0))
	records := limit/(len(line)+1) + 50
	for i := 0; i < records; i++ {
		n.logEvent(logTestEvent(i))
	}
	info, err := os.Stat(n.logPath)
	if err != nil {
		t.Fatalf("live log: %v", err)
	}
	if info.Size() > limit {
		t.Fatalf("live log is %d bytes after %d records, want rotation at %d", info.Size(), records, limit)
	}
	live := readTransitionLogTitles(t, n.logPath)
	rotated := readTransitionLogTitles(t, n.logPath+".1")
	if len(rotated) == 0 {
		t.Fatal("no rotated file <log>.1")
	}
	if got := len(rotated) + len(live); got != records {
		t.Fatalf("records across <log>.1 + <log> = %d, want all %d (nothing lost on rotation)", got, records)
	}
	if rotated[0] != "seq-000000" || live[len(live)-1] != fmt.Sprintf("seq-%06d", records-1) {
		t.Fatalf("order broken: first rotated %q, last live %q", rotated[0], live[len(live)-1])
	}
}

// Retention keeps exactly Keep (4) rotated files of at most 8 MiB; every
// record still on disk is a contiguous, ordered run ending at the newest
// record (rotation drops only whole oldest files, never a record in between).
func TestIssue2481_TransitionLogKeepsNRotatedFiles(t *testing.T) {
	inboxTestHome(t)
	const limit, keep = 8 << 20, 4
	n := NewTransitionNotifier()
	line, _ := json.Marshal(logTestEvent(0))
	records := (keep+2)*limit/(len(line)+1) + 10
	for i := 0; i < records; i++ {
		n.logEvent(logTestEvent(i))
	}
	if _, err := os.Stat(fmt.Sprintf("%s.%d", n.logPath, keep+1)); !os.IsNotExist(err) {
		t.Fatalf("<log>.%d exists beyond Keep=%d (err=%v)", keep+1, keep, err)
	}
	var all []string
	for i := keep; i >= 1; i-- {
		path := fmt.Sprintf("%s.%d", n.logPath, i)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("<log>.%d is missing: %v", i, err)
		}
		if info.Size() > limit {
			t.Fatalf("<log>.%d is %d bytes, over the %d limit", i, info.Size(), limit)
		}
		all = append(all, readTransitionLogTitles(t, path)...)
	}
	all = append(all, readTransitionLogTitles(t, n.logPath)...)
	if len(all) >= records {
		t.Fatalf("retained %d of %d records: nothing was pruned", len(all), records)
	}
	first := records - len(all)
	for i, title := range all {
		if want := fmt.Sprintf("seq-%06d", first+i); title != want {
			t.Fatalf("record %d = %q, want %q: retained history is not contiguous", i, title, want)
		}
	}
}

// A log unlinked under a running writer (log cleanup, an operator, a stale
// rotation) is recreated by the next record: the writer never keeps a
// descriptor to a deleted file.
func TestIssue2481_TransitionLogRecreatedAfterUnlink(t *testing.T) {
	inboxTestHome(t)
	n := NewTransitionNotifier()
	n.logEvent(logTestEvent(1))
	if err := os.Remove(n.logPath); err != nil {
		t.Fatal(err)
	}
	n.logEvent(logTestEvent(2))
	if got := readTransitionLogTitles(t, n.logPath); len(got) != 1 || got[0] != "seq-000002" {
		t.Fatalf("records after unlink = %v, want [seq-000002]", got)
	}
}

// A stats file written by another agent-deck version keeps the counters this
// binary does not know when this binary bumps it (mixed versions on one host:
// hooks on the new binary, the daemon still on the old one).
func TestIssue2481_InboxStatsKeepUnknownFieldsAcrossBump(t *testing.T) {
	inboxTestHome(t)
	const parent = "stats-parent-mixed"
	if err := os.MkdirAll(InboxStatsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	started := "2026-09-01T10:00:00Z"
	seed := `{"parent":"stats-parent-mixed","started_at":"` + started + `","records_urgent":41,"future_counter":7,"future_block":{"a":1}}`
	if err := os.WriteFile(inboxStatsPath(parent), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := BumpInboxStats(parent, func(s *InboxStats) { s.RecordsUrgent++ }); err != nil {
		t.Fatalf("bump: %v", err)
	}
	data, err := os.ReadFile(inboxStatsPath(parent))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("stats file is not JSON: %v\n%s", err, data)
	}
	if string(raw["future_counter"]) != "7" || string(raw["future_block"]) != `{"a":1}` {
		t.Fatalf("bump dropped another version's fields:\n%s", data)
	}
	st, err := ReadInboxStats(parent)
	if err != nil {
		t.Fatal(err)
	}
	if st.RecordsUrgent != 42 || st.StartedAt.UTC().Format(time.RFC3339) != started {
		t.Fatalf("counters = %+v, want records_urgent 42 since %s", st, started)
	}
}

// A field of an unexpected type (another version changed it) must not zero
// the counters that did decode; a file that is not JSON at all is set aside
// for inspection rather than silently overwritten.
func TestIssue2481_InboxStatsNotZeroedOnUndecodableFile(t *testing.T) {
	inboxTestHome(t)
	if err := os.MkdirAll(InboxStatsDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	const mixed = "stats-parent-typed"
	seed := `{"parent":"stats-parent-typed","started_at":"2026-09-01T10:00:00Z","records_urgent":41,"drains":{"by_hook":3}}`
	if err := os.WriteFile(inboxStatsPath(mixed), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := BumpInboxStats(mixed, func(s *InboxStats) { s.RecordsUrgent++ }); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if st, _ := ReadInboxStats(mixed); st.RecordsUrgent != 42 || st.StartedAt.IsZero() {
		t.Fatalf("a mistyped field reset the counters: %+v", st)
	}

	const broken = "stats-parent-broken"
	if err := os.WriteFile(inboxStatsPath(broken), []byte(`{"records_urgent":41,`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := BumpInboxStats(broken, func(s *InboxStats) { s.RecordsUrgent++ }); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if kept, err := os.ReadFile(inboxStatsPath(broken) + ".corrupt"); err != nil || string(kept) != `{"records_urgent":41,` {
		t.Fatalf("undecodable stats file was not kept aside: %q err=%v", kept, err)
	}
}

// Past the per-child bound, a child's turns fold into ONE digest record that
// counts them; the commit succeeds (never `failed`, never retried) and the
// queue stays at maxPendingTurnsPerChild+1 records for that child.
func TestIssue2481_OverflowFoldsIntoOneCountedDigest(t *testing.T) {
	n, parentID, event := newWakeNudgeFixture(t)
	fillPendingTurns(t, parentID, event.ChildSessionID, maxPendingTurnsPerChild)

	const extra = 20
	for i := 0; i < extra; i++ {
		ev := event
		ev.DoneSummary = fmt.Sprintf("overflow turn %d", i)
		ev.Timestamp = time.Unix(int64(10_000+i), 0)
		res := n.NotifyFinished(ev)
		if res.DeliveryResult != transitionDeliveryCommitted {
			t.Fatalf("overflow turn %d: delivery_result = %q, want %q", i, res.DeliveryResult, transitionDeliveryCommitted)
		}
	}

	got := rawInboxRecords(t, parentID)
	if len(got) != maxPendingTurnsPerChild+1 {
		t.Fatalf("inbox holds %d records, want %d (bound + one digest)", len(got), maxPendingTurnsPerChild+1)
	}
	var digests []map[string]any
	for _, rec := range got {
		if _, ok := rec["overflow_turns"]; ok {
			digests = append(digests, rec)
		}
	}
	if len(digests) != 1 {
		t.Fatalf("digest records = %d, want exactly 1", len(digests))
	}
	d := digests[0]
	if d["overflow_turns"] != float64(extra) || d["done_summary"] != fmt.Sprintf("overflow turn %d", extra-1) {
		t.Fatalf("digest = count %v summary %v, want count %d with the newest turn", d["overflow_turns"], d["done_summary"], extra)
	}
	// A replay of a turn already folded into the digest (the first one or a
	// later one) is not a new turn.
	for _, i := range []int{0, 7, extra - 1} {
		replay := event
		replay.DoneSummary = fmt.Sprintf("overflow turn %d", i)
		replay.Timestamp = time.Unix(int64(10_000+i), 0)
		if res := n.NotifyFinished(replay); res.DeliveryResult != transitionDeliveryCommitted {
			t.Fatalf("replay of turn %d: %q", i, res.DeliveryResult)
		}
	}
	text := FormatInboxRecords(readInboxLines(t, parentID), "h")
	if !strings.Contains(text, fmt.Sprintf("overflow digest: %d turns", extra)) {
		t.Fatalf("rendered records do not show the digest count %d:\n%s", extra, text)
	}

	// Once the parent drains, the next turn is a regular record again.
	if _, err := DrainInboxForParent(parentID); err != nil {
		t.Fatalf("drain: %v", err)
	}
	next := event
	next.DoneSummary = "after drain"
	if res := n.NotifyFinished(next); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("after drain: %q", res.DeliveryResult)
	}
	if got := rawInboxRecords(t, parentID); len(got) != 1 || got[0]["overflow_turns"] != nil {
		t.Fatalf("after drain: %+v, want one regular record", got)
	}
}

// rawInboxRecords decodes the parent's inbox lines as generic JSON objects.
func rawInboxRecords(t *testing.T, parentID string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(InboxPathFor(parentID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("inbox line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// A digest record is an ordinary record plus overflow_turns: a reader that
// does not know the field decodes it as the newest turn, and the field
// round-trips for readers that do.
func TestIssue2481_DigestRecordReadableByOlderReader(t *testing.T) {
	base, err := json.Marshal(logTestEvent(1))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(string(base), "}") + `,"overflow_turns":5}`
	var old struct {
		ChildSessionID string `json:"child_session_id"`
		ToStatus       string `json:"to_status"`
		Text           string `json:"text"`
	}
	if err := json.Unmarshal([]byte(line), &old); err != nil || old.ChildSessionID != "rotation-child" || old.ToStatus != "waiting" {
		t.Fatalf("old shape decode: %+v err=%v", old, err)
	}
	var ev TransitionNotificationEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(ev)
	if !strings.Contains(string(again), `"overflow_turns":5`) {
		t.Fatalf("overflow_turns does not round-trip: %s", again)
	}
	if strings.Contains(string(base), "overflow_turns") {
		t.Fatalf("a regular record carries overflow_turns: %s", base)
	}
}
