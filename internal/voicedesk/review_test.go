package voicedesk

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression rows for the independent review of the desk.

func (h *harness) addSession(s Session) { h.deck.sessions = append(h.deck.sessions, s) }

func (h *harness) setStatus(id, status string, archived bool) {
	for i := range h.deck.sessions {
		if h.deck.sessions[i].ID == id {
			h.deck.sessions[i].Status = status
			h.deck.sessions[i].Archived = archived
		}
	}
}

// A Devanagari word whose consonants match a title word but whose vowels
// do not is a look-alike: asked, never silently the target.
func TestDevanagariLookAlikeAsks(t *testing.T) {
	for _, s := range []string{"टोस्टर का स्टेटस क्या है", "बोलिंग का स्टेटस क्या है"} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			h.addSession(Session{ID: "e1000001-1700000031", Title: "api-tester", Group: "team/alex", Profile: "work", Status: "idle"})
			h.addSession(Session{ID: "e1000002-1700000032", Title: "billing", Group: "team/alex", Profile: "work", Status: "idle"})
			got := h.say(s)
			if targetID(got) != "" || refusedOf(got) != RefusedAmbiguous || got.Confirm == nil {
				t.Fatalf("look-alike resolved silently: %+v", got)
			}
		})
	}
}

func TestSecretDetectorEnvForms(t *testing.T) {
	for _, s := range []string{
		"DB_PASSWORD=hunter22", "POSTGRES_PASSWORD: Sup3rS3cret", "ADMIN_PASS=Winter2026!", "export STRIPE_SECRET=Abc123xyz",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "pass: s3cr3tP@ss", "password hai hunter22",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	} {
		if !HasSecret(s) {
			t.Errorf("missed secret in %q", s)
		}
	}
}

func TestInvalidUTF8DoesNotPanic(t *testing.T) {
	h := newHarness(t)
	for _, s := range []string{"tell phone build to \xff\xfe go", "\xffstatus of phone build", "read \xc3"} {
		r := h.say(s)
		if r.Handled && r.Say == "" {
			t.Fatalf("%q: %+v", s, r)
		}
	}
	if len(h.deck.sent) != 0 {
		t.Fatalf("sent: %+v", h.deck.sent)
	}
}

func TestMoreRiskyForms(t *testing.T) {
	for _, s := range []string{"voice test ko bolo ki ईमेल भेज दो", "tell voice test to reinstall the app", "tell voice test to revert the last commit", "tell voice test to republish the page"} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			got := h.do(Request{Sentence: s, On: idVoiceTest})
			if got.Confirm == nil || len(h.deck.sent) != 0 {
				t.Fatalf("risky message sent without a yes: %+v sent=%+v", got, h.deck.sent)
			}
		})
	}
}

func TestFileStoreSweepsOldQuestions(t *testing.T) {
	dir := t.TempDir()
	st := FileStore{Dir: dir}
	tok, _ := NewToken()
	p := Pending{Token: tok, Profile: "work", Kind: "yes", Action: actionSend, Message: "secret plan", CreatedAt: t0}
	p.Seal()
	if err := st.Put(p); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, tok+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pending file mode: %v %v", info, err)
	}
	old := time.Now().Add(-2 * HardTTL)
	_ = os.Chtimes(filepath.Join(dir, tok+".json"), old, old)
	st.Sweep()
	if _, err := os.Stat(filepath.Join(dir, tok+".json")); !os.IsNotExist(err) {
		t.Fatalf("an unanswered message stayed on disk: %v", err)
	}
	if _, err := st.Take("../../etc/passwd"); err == nil {
		t.Fatal("a token that is not 32 hex characters was accepted")
	}
}

// Choosing among look-alikes never sends straight away, even to the
// session the mic is on: the message is read back first.
func TestChoiceThenTellReadsBack(t *testing.T) {
	h := newHarness(t)
	ask := h.do(Request{Sentence: "tell voice to run tests", On: idVoiceTest})
	if ask.Confirm == nil || ask.Confirm.Kind != "choice" {
		t.Fatalf("ask = %+v", ask)
	}
	var idx string
	for i, it := range ask.Show.Items {
		if it.ID == idVoiceTest {
			idx = []string{"first", "second", "third"}[i]
		}
	}
	got := h.do(Request{Sentence: idx, On: idVoiceTest, Pending: ask.Confirm.Token, AnswerDelayMs: 600, HasAnswerDelay: true})
	if got.Confirm == nil || got.Confirm.Kind != "yes" || len(h.deck.sent) != 0 {
		t.Fatalf("sent without a read-back: %+v sent=%+v", got, h.deck.sent)
	}
}

// A prompt that would speak a secret is not asked, and its token is gone.
func TestSecretInPromptDropsTheQuestion(t *testing.T) {
	h := newHarness(t)
	h.addSession(Session{ID: "e1000003-1700000033", Title: "pass: s3cr3tP4ssw0rd", Group: "team/alex", Profile: "work", Status: "idle"})
	got := h.say("tell pass s3cr3tP4ssw0rd to go")
	b, _ := json.Marshal(got)
	if got.Confirm != nil || h.store.Len() != 0 || strings.Contains(string(b), "s3cr3t") {
		t.Fatalf("got %s store=%d", b, h.store.Len())
	}
}

// The session is looked up again before a yes acts.
func TestYesRechecksTheSession(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		archived bool
		want     string
	}{{"stopped meanwhile", "stopped", false, RefusedUnsafe}, {"archived meanwhile", "idle", true, RefusedNotFound}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			ask := h.say("Tell deck conductor to check the phone build")
			h.setStatus(idConductor, c.status, c.archived)
			got := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 600, HasAnswerDelay: true})
			if refusedOf(got) != c.want || len(h.deck.sent) != 0 {
				t.Fatalf("got %+v sent=%+v", got, h.deck.sent)
			}
		})
	}
	t.Run("clock went back", func(t *testing.T) {
		h := newHarness(t)
		ask := h.say("Tell deck conductor to check the phone build")
		h.now = h.now.Add(-5 * time.Second)
		got := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token})
		if refusedOf(got) != RefusedExpired || len(h.deck.sent) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestDevanagariSilentE(t *testing.T) {
	for dev, en := range map[string]string{"नोट्स": "notes", "कोड": "code", "डेक": "deck", "कंडक्टर": "conductor"} {
		if vowelClass(fold(toRoman(dev))) != vowelClass(fold(en)) {
			t.Errorf("%s / %s: %q != %q", dev, en, vowelClass(fold(toRoman(dev))), vowelClass(fold(en)))
		}
	}
	if vowelClass(fold(toRoman("टोस्टर"))) == vowelClass(fold("tester")) {
		t.Error("toaster reads as tester")
	}
}

var _ = context.Background
