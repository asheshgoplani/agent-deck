package session

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNoteResultRead_RecordsTurnsReadPerChild(t *testing.T) {
	inboxTestHome(t)
	t0 := time.Now().Add(-time.Hour)
	for _, uuid := range []string{"u1", "u2", "u1"} { // a re-read is not stored twice
		if err := NoteResultRead("p", "c", uuid, t0); err != nil {
			t.Fatal(err)
		}
	}
	for _, ignored := range [][3]string{{"", "c", "u"}, {"p", "", "u"}, {"p", "p", "u"}, {"p", "d", ""}} {
		if err := NoteResultRead(ignored[0], ignored[1], ignored[2], t0); err != nil {
			t.Fatal(err)
		}
	}
	reads := loadResultReads("p")
	if len(reads) != 1 || strings.Join(reads["c"].Turns, ",") != "u1,u2" {
		t.Fatalf("reads = %+v, want only c with turns u1,u2", reads)
	}
	finished := TransitionNotificationEvent{ChildSessionID: "c", Kind: transitionKindFinished, TurnUUID: "u2", Timestamp: t0.Add(-time.Minute)}
	if !reads.covers(finished) {
		t.Fatal("a read of the record's own turn, made after the turn ended, must cover it")
	}
	// Review B1(b): a read made before the record's time (a mid-turn peek)
	// never covers it, even with the same uuid; nor does a record with no time.
	finished.Timestamp = t0.Add(time.Hour)
	if reads.covers(finished) {
		t.Fatal("a read made before the turn ended must not cover its record")
	}
	finished.Timestamp = time.Time{}
	if reads.covers(finished) {
		t.Fatal("a record without a time must not be covered")
	}
	interim := TransitionNotificationEvent{ChildSessionID: "c", Kind: transitionKindFinished, TurnUUID: "u2", Timestamp: t0.Add(-time.Minute), TurnTextInterim: true}
	if reads.covers(interim) {
		t.Fatal("a record keyed on interim text must never be covered")
	}
	for name, ev := range map[string]TransitionNotificationEvent{
		"other turn": {ChildSessionID: "c", Kind: transitionKindFinished, TurnUUID: "u3", Timestamp: t0.Add(-time.Hour)},
		"no turn":    {ChildSessionID: "c", Kind: transitionKindFinished, Timestamp: t0.Add(-time.Hour)},
		"question":   {ChildSessionID: "c", ToStatus: "waiting", Question: true, TurnUUID: "u1"},
		"error":      {ChildSessionID: "c", ToStatus: "error", TurnUUID: "u1"},
		"remote":     {ChildSessionID: "c", Kind: transitionKindFinished, SourceRemote: "box", TurnUUID: "u1"},
		"digest":     {ChildSessionID: "c", ToStatus: "waiting", OverflowTurns: 3, TurnUUID: "u1"},
		"other kid":  {ChildSessionID: "d", Kind: transitionKindFinished, TurnUUID: "u1"},
	} {
		if reads.covers(ev) {
			t.Errorf("%s record must never be withdrawn", name)
		}
	}
	waiting := TransitionNotificationEvent{ChildSessionID: "c", ToStatus: "waiting", TurnUUID: "u1", Timestamp: t0.Add(-time.Minute)}
	if !reads.covers(waiting) {
		t.Fatal("a finished turn (waiting edge) whose turn was read must be covered")
	}
}

func TestNoteResultRead_KeepsOnlyRecentTurns(t *testing.T) {
	inboxTestHome(t)
	now := time.Now()
	for i := 0; i < resultReadTurnsKept+3; i++ {
		if err := NoteResultRead("p", "c", fmt.Sprintf("u%d", i), now); err != nil {
			t.Fatal(err)
		}
	}
	turns := loadResultReads("p")["c"].Turns
	if len(turns) != resultReadTurnsKept || turns[len(turns)-1] != fmt.Sprintf("u%d", resultReadTurnsKept+2) || turns[0] != "u3" {
		t.Fatalf("turns = %v, want the newest %d", turns, resultReadTurnsKept)
	}
}

// The read side and the record side must name the same transcript record:
// `session output` reports the uuid of the assistant record it printed and
// the daemon stamps a record with the uuid of the turn's final text record.
func TestResultRead_ReadUUIDMatchesRecordTurnUUID(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u-prompt","sessionId":"s1","message":{"role":"user","content":"do it"}}`,
		`{"type":"assistant","uuid":"a-mid","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"working"},{"type":"tool_use","id":"t1"}]}}`,
		`{"type":"user","uuid":"u-tool","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"}]}}`,
		`{"type":"assistant","uuid":"a-final","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"all done"}]}}`,
		`{"type":"assistant","uuid":"a-side","isSidechain":true,"sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"subagent"}]}}`,
	}
	resp, err := parseClaudeLastAssistantMessage([]byte(strings.Join(lines, "\n")+"\n"), "s1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	facts := classifyTranscriptTail(lines)
	if resp.AssistantUUID != "a-final" || facts.UUID != resp.AssistantUUID {
		t.Fatalf("read uuid %q, record uuid %q; want both a-final", resp.AssistantUUID, facts.UUID)
	}
	// Stop before the final record is written: the read names the mid-turn
	// record, which no final-turn record carries.
	early, err := parseClaudeLastAssistantMessage([]byte(strings.Join(lines[:3], "\n")+"\n"), "s1.jsonl")
	if err != nil || early.AssistantUUID != "a-mid" {
		t.Fatalf("early read uuid = %q err=%v, want a-mid", early.AssistantUUID, err)
	}
}

func TestResultRead_WithdrawalsAreCounted(t *testing.T) {
	f := newResultReadFixture(t, 2)
	if err := NoteResultRead(f.parentID, f.children[0].ID, turnOf(f.children[0]), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.children {
		f.finish(t, c, "r")
	}
	if _, err := DrainInboxForParent(f.parentID); err != nil {
		t.Fatal(err)
	}
	st, err := ReadInboxStats(f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if st.WakeupsWithdrawn != 1 || st.RecordsWithdrawn != 1 || st.WakeupsUrgent != 1 {
		t.Fatalf("stats = withdrawn wakes %d records %d, urgent wakes %d; want 1, 1, 1", st.WakeupsWithdrawn, st.RecordsWithdrawn, st.WakeupsUrgent)
	}
}

// BenchmarkNoteResultRead is the cost `session output` pays per read made
// from inside a session (one lock and one small atomic rewrite).
func BenchmarkNoteResultRead(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("AGENT_DECK_HOME", "")
	at := time.Now()
	for i := 0; i < b.N; i++ {
		if err := NoteResultRead("bench-parent", "bench-child", fmt.Sprintf("u%d", i%16), at); err != nil {
			b.Fatal(err)
		}
	}
}
