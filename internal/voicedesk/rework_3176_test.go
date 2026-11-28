package voicedesk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 3176 rework F1: a plural stop, restart or archive never acts on a
// conductor or on the session the mic is on, whatever the words say. The
// single form refuses both at once; the plural form must not be a way
// around that.

func actedOn(h *harness, id string) bool {
	for _, a := range h.deck.acts {
		if strings.HasSuffix(a, " "+id) {
			return true
		}
	}
	return false
}

func TestRework3176BulkNeverConductors(t *testing.T) {
	for _, s := range []string{"stop all conductors", "restart all conductors", "archive all conductors"} {
		h := phase2Harness(t)
		q := h.say(s)
		if q.Confirm != nil {
			t.Errorf("%q asks a question it must refuse: %q", s, q.Say)
			h.yes(q)
		}
		if refusedOf(q) != RefusedUnsafe {
			t.Errorf("%q: refused=%q want %q (say %q)", s, refusedOf(q), RefusedUnsafe, q.Say)
		}
		if len(h.deck.acts) != 0 {
			t.Errorf("%q acted: %v", s, h.deck.acts)
		}
	}
}

func TestRework3176BulkNeverMicSession(t *testing.T) {
	for _, s := range []string{"stop all phone build", "restart all phone build", "archive all phone build"} {
		h := phase2Harness(t)
		q := h.do(Request{Sentence: s, On: idPhone})
		if q.Confirm != nil {
			t.Errorf("%q (mic on phone build) asks: %q", s, q.Say)
			h.do(Request{Sentence: "yes", Pending: q.Confirm.Token, On: idPhone, AnswerDelayMs: 800, HasAnswerDelay: true})
		}
		if actedOn(h, idPhone) {
			t.Errorf("%q acted on the mic's session: %v", s, h.deck.acts)
		}
	}
}

// The yes re-checks each target: one that became the mic's session (the
// mic moved after the question) or a conductor is left alone.
func TestRework3176BulkYesRechecksMicAndConductor(t *testing.T) {
	h := phase2Harness(t)
	q := h.say("stop all workers")
	if q.Confirm == nil || !strings.Contains(q.Say, "phone build") {
		t.Fatalf("setup: %q", q.Say)
	}
	h.do(Request{Sentence: "yes", Pending: q.Confirm.Token, On: idPhone, AnswerDelayMs: 800, HasAnswerDelay: true})
	if actedOn(h, idPhone) {
		t.Errorf("a yes said on phone build stopped phone build: %v", h.deck.acts)
	}

	h = phase2Harness(t)
	q = h.say("stop all workers")
	if q.Confirm == nil {
		t.Fatalf("setup: %q", q.Say)
	}
	for i := range h.deck.sessions {
		if h.deck.sessions[i].ID == idPhone {
			h.deck.sessions[i].Conductor = true // became a conductor after the question
		}
	}
	h.yes(q)
	if actedOn(h, idPhone) {
		t.Errorf("a yes stopped a row that is a conductor now: %v", h.deck.acts)
	}
	if len(h.deck.acts) == 0 {
		t.Errorf("the other targets were not stopped either")
	}
}

// R10: a bulk restart reaches the sixth session and later: the sessions
// restarted a moment ago are not asked about again.
func TestRework3176BulkRestartReachesTheRest(t *testing.T) {
	h := phase2Harness(t)
	seen := map[string]bool{}
	for round := 0; round < 3; round++ {
		q := h.say("restart all workers")
		if q.Confirm == nil {
			break
		}
		for _, it := range q.Show.Items {
			seen[it.ID] = true
		}
		h.yes(q)
		h.now = h.now.Add(5 * time.Second)
	}
	if len(seen) != 7 {
		t.Errorf("restart all workers: %d of 7 asked after 3 rounds (acts=%v)", len(seen), h.deck.acts)
	}
	if q := h.say("restart all workers"); q.Confirm != nil || q.Say != "Nothing done: all seven were restarted a moment ago." {
		t.Errorf("right after: %q", q.Say)
	}
	// a minute later the whole list is back
	h.now = h.now.Add(2 * time.Minute)
	q := h.say("restart all workers")
	if q.Confirm == nil || !strings.Contains(q.Say, "of seven") {
		t.Errorf("a minute later: %q", q.Say)
	}
}

// R8: the owner filter runs before the cut to five, so other people's hits
// cannot crowd out his own.
func TestRework3176SearchCutAfterOwnerFilter(t *testing.T) {
	h := phase2Harness(t)
	var hits []SearchHit
	for i := 0; i < 8; i++ { // eight hits of Sam's first
		hits = append(hits, SearchHit{Title: fmt.Sprintf("sam-%d", i), DeckID: "c1000002-1700000012", Snippet: "the invoice"})
	}
	mine := []string{idTsNotes, idRelease, idAudit, idPhone, idVoiceTest, "a1000002-1700000002", "a1000003-1700000003"}
	for i, id := range mine {
		hits = append(hits, SearchHit{Title: fmt.Sprintf("mine-%d", i), DeckID: id, Snippet: "the invoice"})
	}
	h.deck.hits = hits
	q := h.say("where did we talk about the invoice")
	if len(q.Show.Items) != 5 {
		t.Errorf("want his first five hits, got %d: %q", len(q.Show.Items), q.Say)
	}
	for _, it := range q.Show.Items {
		if strings.HasPrefix(it.Title, "sam-") {
			t.Errorf("a hit of Sam's is shown: %+v", it)
		}
	}
}

// R8: when the session list fails, a search says it could not search, not
// "nothing found".
func TestRework3176SearchListErrorIsNotNothingFound(t *testing.T) {
	h := phase2Harness(t)
	h.deck.sessions = nil
	h.deck.errs = []HostError{{Host: "", Profile: "work", Error: "unreachable"}}
	q := h.say("where did we talk about the invoice")
	if q.Say != "I could not search right now." {
		t.Errorf("say %q", q.Say)
	}
}

// R10: the file store keeps the restarted sessions for a minute only, by
// host and id, in a 0600 file.
func TestRework3176FileStoreRecentRestarts(t *testing.T) {
	dir := t.TempDir()
	fs := FileStore{Dir: dir}
	at := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	fs.MarkRestarted([]Session{{ID: idPhone}, {ID: "b1000002-1700000022", Host: "boxone"}}, at)
	got := fs.RestartedSince(at.Add(-RecentRestartWindow))
	if !got[restartKey("", idPhone)] || !got[restartKey("boxone", "b1000002-1700000022")] || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got := fs.RestartedSince(at.Add(time.Second)); len(got) != 0 {
		t.Errorf("older entries kept: %v", got)
	}
	// a later write drops what is older than the window
	fs.MarkRestarted([]Session{{ID: idAudit}}, at.Add(2*time.Minute))
	got = fs.RestartedSince(time.Time{})
	if len(got) != 1 || !got[restartKey("", idAudit)] {
		t.Errorf("after prune: %v", got)
	}
	info, err := os.Stat(filepath.Join(dir, restartsFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("restarts file mode: %v %v", info, err)
	}
}
