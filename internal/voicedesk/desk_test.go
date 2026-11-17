package voicedesk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	idConductor = "a1000001-1700000001"
	idMacapp    = "a1000002-1700000002"
	idVoicePlan = "a1000003-1700000003"
	idAudit     = "a1000004-1700000004"
	idPhone     = "a1000005-1700000005"
	idVoiceTest = "a1000006-1700000006"
	idTsNotes   = "a1000007-1700000007"
	idAudit0955 = "a1000008-1700000008"
	idRelease   = "a1000010-1700000010"
	idBoxCond   = "b1000001-1700000021"
	idTsFix     = "b1000002-1700000022"
)

type row struct {
	sentence string
	intent   string
	lang     string
	target   string // "" = none
	refused  string
	confirm  string // "" | yes | choice
	handled  bool
}

// Every phase-1 action in English, Roman Hindi and Devanagari, on the saved
// session list (this machine + boxone).
func TestIntentTable(t *testing.T) {
	rows := []row{
		// needs_me
		{"Who is waiting for me?", IntentNeedsMe, LangEN, "", "", "", true},
		{"who needs me", IntentNeedsMe, LangEN, "", "", "", true},
		{"Jarvis, what needs me?", IntentNeedsMe, LangEN, "", "", "", true},
		{"kaun mera intezaar kar raha hai", IntentNeedsMe, LangHI, "", "", "", true},
		{"kisko meri zaroorat hai", IntentNeedsMe, LangHI, "", "", "", true},
		{"कौन मेरा इंतज़ार कर रहा है", IntentNeedsMe, LangHI, "", "", "", true},
		{"किसको मेरी ज़रूरत है", IntentNeedsMe, LangHI, "", "", "", true},
		// status
		{"Status of the Mac app", IntentStatus, LangEN, idMacapp, "", "", true},
		{"how is phone build doing?", IntentStatus, LangEN, idPhone, "", "", true},
		{"what is the status of voice plan", IntentStatus, LangEN, idVoicePlan, "", "", true},
		{"status of conductor on boxone", IntentStatus, LangEN, idBoxCond, "", "", true},
		{"status of tailscale fix", IntentStatus, LangEN, idTsFix, "", "", true},
		{"status of audit 0955", IntentStatus, LangEN, idAudit0955, "", "", true},
		{"macapp redesign ka status kya hai", IntentStatus, LangHI, idMacapp, "", "", true},
		{"phone build kya kar raha hai", IntentStatus, LangHI, idPhone, "", "", true},
		{"boxone wala tailscale fix ka status batao", IntentStatus, LangHI, idTsFix, "", "", true},
		{"मैक ऐप का स्टेटस क्या है", IntentStatus, LangHI, "", RefusedAmbiguous, "choice", true}, // Devanagari look-alike: asked
		// read
		{"Read proactive audit", IntentRead, LangEN, idAudit, "", "", true},
		{"what did deck conductor say", IntentRead, LangEN, idConductor, "", "", true},
		{"read the last reply of mac app", IntentRead, LangEN, idMacapp, "", "", true},
		{"deck conductor ka jawab padho", IntentRead, LangHI, idConductor, "", "", true},
		{"proactive audit ne kya bola", IntentRead, LangHI, idAudit, "", "", true},
		{"डेक कंडक्टर का जवाब पढ़ो", IntentRead, LangHI, idConductor, "", "", true},
		// tell (he is on no session: every tell asks first)
		{"Tell deck conductor to check the phone build", IntentTell, LangEN, idConductor, "", "yes", true},
		{"ask voice test whether the tests pass", IntentTell, LangEN, idVoiceTest, "", "yes", true},
		{"tell tailscale fix on boxone that the notes are ready", IntentTell, LangEN, idTsFix, "", "yes", true},
		{"deck conductor ko bolo ki phone build kal karna", IntentTell, LangHI, idConductor, "", "yes", true},
		{"voice test se poocho ki tests pass hue", IntentTell, LangHI, idVoiceTest, "", "yes", true},
		{"डेक कंडक्टर को बोलो कि बिल्ड चेक करे", IntentTell, LangHI, idConductor, "", "yes", true},
		// find
		{"find the tailscale session", IntentFind, LangEN, "", "", "choice", true},
		{"find the phone build session", IntentFind, LangEN, idPhone, "", "yes", true},
		{"tailscale wali session dhoondo", IntentFind, LangHI, "", "", "choice", true},
		{"phone build wali session dhoondo", IntentFind, LangHI, idPhone, "", "yes", true},
		// overview
		{"How is everything?", IntentOverview, LangEN, "", "", "", true},
		{"how are things", IntentOverview, LangEN, "", "", "", true},
		{"sab kaisa chal raha hai", IntentOverview, LangHI, "", "", "", true},
		{"sab theek hai?", IntentOverview, LangHI, "", "", "", true},
		{"सब कैसा चल रहा है", IntentOverview, LangHI, "", "", "", true},
		// list
		{"list my sessions", IntentList, LangEN, "", "", "", true},
		{"what sessions do I have", IntentList, LangEN, "", "", "", true},
		{"meri sessions batao", IntentList, LangHI, "", "", "", true},
		{"meri sessions sunao", IntentList, LangHI, "", "", "", true},
		{"मेरी सेशन्स बताओ", IntentList, LangHI, "", "", "", true},
		// refusals and questions
		{"Status of sam macdesign", IntentStatus, LangEN, "", RefusedOtherPerson, "", true},
		{"sam macdesign ka status kya hai", IntentStatus, LangHI, "", RefusedOtherPerson, "", true},
		{"Tell conductor deck robin to stop", IntentTell, LangEN, "", RefusedOtherPerson, "", true},
		{"conductor deck rob ko bolo ki ruk jao", IntentTell, LangHI, "", RefusedOtherPerson, "", true},
		{"Tell phone walk to continue", IntentTell, LangEN, "", RefusedNotFound, "", true},
		{"status of stray session", IntentStatus, LangEN, "", RefusedNotFound, "", true},
		{"status of audit 0956", IntentStatus, LangEN, "", RefusedNotFound, "", true},
		{"status of old phone build", IntentStatus, LangEN, "", RefusedNotFound, "", true},
		{"status of voice text", IntentStatus, LangEN, "", RefusedAmbiguous, "choice", true},
		{"voice text ka status kya hai", IntentStatus, LangHI, "", RefusedAmbiguous, "choice", true},
		{"status of conductor", IntentStatus, LangEN, "", RefusedAmbiguous, "choice", true},
		{"tell release notes to add the date", IntentTell, LangEN, idRelease, RefusedUnsafe, "", true},
	}
	for _, r := range rows {
		t.Run(r.sentence, func(t *testing.T) {
			h := newHarness(t)
			got := h.say(r.sentence)
			if got.Handled != r.handled || got.Intent != r.intent || got.Lang != r.lang {
				t.Fatalf("handled/intent/lang = %v/%s/%s, want %v/%s/%s (say %q)", got.Handled, got.Intent, got.Lang, r.handled, r.intent, r.lang, got.Say)
			}
			if targetID(got) != r.target {
				t.Errorf("target = %q, want %q (say %q)", targetID(got), r.target, got.Say)
			}
			if refusedOf(got) != r.refused {
				t.Errorf("refused = %q, want %q (say %q)", refusedOf(got), r.refused, got.Say)
			}
			kind := ""
			if got.Confirm != nil {
				kind = got.Confirm.Kind
			}
			if kind != r.confirm {
				t.Errorf("confirm = %q, want %q (say %q)", kind, r.confirm, got.Say)
			}
			if len(h.deck.sent) != 0 {
				t.Errorf("sent without a yes: %+v", h.deck.sent)
			}
		})
	}
}

// The voice daemon's own sentences (switch, look, audio, harness, app) and
// plain talk come back handled:false. The daemon's own commands come back
// without any session listing (parse first).
func TestDaemonSentencesAreNotTheDesks(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "daemon_phrases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]json.RawMessage
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	for group, raw := range fx {
		if strings.HasPrefix(group, "_") {
			continue
		}
		var sentences []string
		if err := json.Unmarshal(raw, &sentences); err != nil {
			t.Fatal(err)
		}
		for _, s := range sentences {
			t.Run(group+"/"+s, func(t *testing.T) {
				h := newHarness(t)
				h.deck.noList = group != "talk" && group != "talk_coding"
				got := h.say(s)
				if got.Handled || got.Intent != IntentNone || got.Say != "" || got.Confirm != nil {
					t.Fatalf("stole a sentence: %+v", got)
				}
			})
		}
	}
}

// The 11 sentences of the 9 Nov router proof (names made generic): each
// comes back with the right intent or not handled.
func TestRouterProofSentences(t *testing.T) {
	cases := []struct {
		sentence, intent, target, refused, confirm string
		handled                                    bool
	}{
		{"Ask deck conductor what is still open tonight", IntentTell, idConductor, "", "yes", true},
		{"Status of the Mac app", IntentStatus, idMacapp, "", "", true},
		{"macapp redesign ka status kya hai", IntentStatus, idMacapp, "", "", true},
		{"Tell voice plan to finish the page first", IntentTell, idVoicePlan, "", "yes", true},
		{"deck conductor ko bolo ki phone build kal karna", IntentTell, idConductor, "", "yes", true},
		{"Tell deck conductor to merge and push the release", IntentTell, idConductor, "", "yes", true},
		{"Tell conductor deck robin to stop", IntentTell, "", RefusedOtherPerson, "", true},
		{"Status of sam macdesign", IntentStatus, "", RefusedOtherPerson, "", true},
		{"Read proactive audit", IntentRead, idAudit, "", "", true},
		{"Tell phone walk to continue", IntentTell, "", RefusedNotFound, "", true},
		{"kuch bhi random baat", IntentNone, "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.sentence, func(t *testing.T) {
			h := newHarness(t)
			got := h.say(c.sentence)
			kind := ""
			if got.Confirm != nil {
				kind = got.Confirm.Kind
			}
			if got.Handled != c.handled || got.Intent != c.intent || targetID(got) != c.target || refusedOf(got) != c.refused || kind != c.confirm {
				t.Fatalf("got handled=%v intent=%s target=%s refused=%s confirm=%s say=%q", got.Handled, got.Intent, targetID(got), refusedOf(got), kind, got.Say)
			}
		})
	}
}

func TestTellOnCurrentSessionSendsAtOnce(t *testing.T) {
	h := newHarness(t)
	got := h.do(Request{Sentence: "tell deck conductor to check the phone build", On: idConductor})
	if got.Confirm != nil || len(h.deck.sent) != 1 || h.deck.sent[0].msg != "check the phone build" {
		t.Fatalf("got %+v, sent %+v", got, h.deck.sent)
	}
	if got.Say != "Sent to conductor deck alex." {
		t.Errorf("say = %q", got.Say)
	}
}

func TestTellRiskyWordAsksEvenOnCurrentSession(t *testing.T) {
	for _, s := range []string{
		"tell deck conductor to merge and push the release",
		"tell deck conductor to delete the old branch",
		"deck conductor ko bolo ki purani files hata do",
		"deck conductor ko bolo ki mail bhej do",
		"tell deck conductor to pay the invoice",
		"tell deck conductor to send an email to the team",
	} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			got := h.do(Request{Sentence: s, On: idConductor})
			if got.Confirm == nil || got.Confirm.Kind != "yes" || len(h.deck.sent) != 0 {
				t.Fatalf("risky message not asked first: %+v sent=%+v", got, h.deck.sent)
			}
			if !strings.HasPrefix(got.Say, "Send to conductor deck alex: ") || !strings.HasSuffix(got.Say, ". Yes?") {
				t.Errorf("read-back = %q", got.Say)
			}
		})
	}
}

func TestTellThenYesSends(t *testing.T) {
	for _, yes := range []string{"yes", "Yes.", "yes send", "yes send it", "haan", "haan bhejo", "haan ji", "ji haan", "send it", "bhej do", "हाँ", "हाँ भेजो", "Jarvis, yes"} {
		t.Run(yes, func(t *testing.T) {
			h := newHarness(t)
			ask := h.say("Tell deck conductor to check the phone build")
			if ask.Say != "Send to conductor deck alex: check the phone build. Yes?" {
				t.Fatalf("read-back = %q", ask.Say)
			}
			h.now = h.now.Add(3 * time.Second)
			got := h.do(Request{Sentence: yes, Pending: ask.Confirm.Token, AnswerDelayMs: 1200, HasAnswerDelay: true})
			if got.Intent != IntentConfirmYes || got.Say != "Sent to conductor deck alex." {
				t.Fatalf("got %+v", got)
			}
			if len(h.deck.sent) != 1 || h.deck.sent[0].id != idConductor || h.deck.sent[0].msg != "check the phone build" {
				t.Fatalf("sent = %+v", h.deck.sent)
			}
		})
	}
}

func TestTellThenNoCancels(t *testing.T) {
	for _, no := range []string{"no", "cancel", "nahin", "nahi", "rehne do", "mat bhejo", "नहीं", "रहने दो"} {
		t.Run(no, func(t *testing.T) {
			h := newHarness(t)
			ask := h.say("Tell deck conductor to check the phone build")
			got := h.do(Request{Sentence: no, Pending: ask.Confirm.Token, AnswerDelayMs: 800, HasAnswerDelay: true})
			if got.Intent != IntentConfirmNo || got.Say != "Cancelled." || len(h.deck.sent) != 0 {
				t.Fatalf("got %+v sent=%+v", got, h.deck.sent)
			}
		})
	}
}

func TestYesAfterTheWindowSendsNothing(t *testing.T) {
	t.Run("measured delay 11 s", func(t *testing.T) {
		h := newHarness(t)
		ask := h.say("Tell deck conductor to check the phone build")
		got := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 11000, HasAnswerDelay: true})
		if refusedOf(got) != RefusedExpired || got.Say != "Too late, nothing sent." || len(h.deck.sent) != 0 {
			t.Fatalf("got %+v sent=%+v", got, h.deck.sent)
		}
	})
	t.Run("token 11 s old, no delay given", func(t *testing.T) {
		h := newHarness(t)
		ask := h.say("Tell deck conductor to check the phone build")
		h.now = h.now.Add(11 * time.Second)
		got := h.do(Request{Sentence: "haan bhejo", Pending: ask.Confirm.Token})
		if refusedOf(got) != RefusedExpired || len(h.deck.sent) != 0 {
			t.Fatalf("got %+v sent=%+v", got, h.deck.sent)
		}
	})
	t.Run("hard 60 s even with a short delay", func(t *testing.T) {
		h := newHarness(t)
		ask := h.say("Tell deck conductor to check the phone build")
		h.now = h.now.Add(61 * time.Second)
		got := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
		if refusedOf(got) != RefusedExpired || len(h.deck.sent) != 0 {
			t.Fatalf("got %+v sent=%+v", got, h.deck.sent)
		}
	})
	t.Run("exactly 10 s is still in time", func(t *testing.T) {
		h := newHarness(t)
		ask := h.say("Tell deck conductor to check the phone build")
		got := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 10000, HasAnswerDelay: true})
		if refusedOf(got) != "" || len(h.deck.sent) != 1 {
			t.Fatalf("got %+v sent=%+v", got, h.deck.sent)
		}
	})
}

func TestYesInsideALongerSentenceIsNotAYes(t *testing.T) {
	for _, s := range []string{"yes I think so, also check the build", "yes but first run the tests", "haan lekin pehle tests chalao", "I said yes"} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			ask := h.say("Tell deck conductor to check the phone build")
			got := h.do(Request{Sentence: s, Pending: ask.Confirm.Token, AnswerDelayMs: 900, HasAnswerDelay: true})
			if got.Intent == IntentConfirmYes || len(h.deck.sent) != 0 {
				t.Fatalf("a longer sentence counted as yes: %+v sent=%+v", got, h.deck.sent)
			}
			// the token was dropped: a later plain yes sends nothing
			again := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 900, HasAnswerDelay: true})
			if refusedOf(again) != RefusedExpired || len(h.deck.sent) != 0 {
				t.Fatalf("token survived a non-answer: %+v sent=%+v", again, h.deck.sent)
			}
		})
	}
}

func TestTokenIsSingleUseAndBound(t *testing.T) {
	t.Run("reuse", func(t *testing.T) {
		h := newHarness(t)
		ask := h.say("Tell deck conductor to check the phone build")
		first := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 900, HasAnswerDelay: true})
		second := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 900, HasAnswerDelay: true})
		if first.Say != "Sent to conductor deck alex." || refusedOf(second) != RefusedExpired || len(h.deck.sent) != 1 {
			t.Fatalf("first=%+v second=%+v sent=%+v", first, second, h.deck.sent)
		}
	})
	t.Run("other profile", func(t *testing.T) {
		h := newHarness(t)
		ask := h.say("Tell deck conductor to check the phone build")
		got := h.do(Request{Sentence: "yes", Profile: "personal", Pending: ask.Confirm.Token, AnswerDelayMs: 900, HasAnswerDelay: true})
		if refusedOf(got) != RefusedExpired || len(h.deck.sent) != 0 {
			t.Fatalf("token crossed profiles: %+v", got)
		}
	})
	t.Run("tampered action", func(t *testing.T) {
		h := newHarness(t)
		ask := h.say("Tell deck conductor to check the phone build")
		p := h.store.m[ask.Confirm.Token]
		p.Message = "delete everything"
		h.store.m[ask.Confirm.Token] = p
		got := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 900, HasAnswerDelay: true})
		if refusedOf(got) != RefusedExpired || len(h.deck.sent) != 0 {
			t.Fatalf("tampered pending acted: %+v sent=%+v", got, h.deck.sent)
		}
	})
	t.Run("unknown token", func(t *testing.T) {
		h := newHarness(t)
		got := h.do(Request{Sentence: "yes", Pending: "0123456789abcdef0123456789abcdef"})
		if refusedOf(got) != RefusedExpired || len(h.deck.sent) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestDryRunNeverSendsOrStores(t *testing.T) {
	h := newHarness(t)
	ask := h.do(Request{Sentence: "Tell deck conductor to check the phone build", DryRun: true})
	if !ask.DryRun || ask.Confirm == nil || h.store.Len() != 0 {
		t.Fatalf("dry run stored a question: %+v (store %d)", ask, h.store.Len())
	}
	on := h.do(Request{Sentence: "tell deck conductor to check the phone build", On: idConductor, DryRun: true})
	if on.Say != "Sent to conductor deck alex." || len(h.deck.sent) != 0 {
		t.Fatalf("dry run sent: %+v %+v", on, h.deck.sent)
	}
	find := h.do(Request{Sentence: "find the phone build session", DryRun: true})
	real := h.say("find the phone build session")
	yes := h.do(Request{Sentence: "yes", Pending: real.Confirm.Token, DryRun: true, AnswerDelayMs: 500, HasAnswerDelay: true})
	if find.Confirm == nil || yes.Then == nil || len(h.deck.shown) != 0 || h.store.Len() != 1 {
		t.Fatalf("dry run switched or consumed: find=%+v yes=%+v shown=%v store=%d", find, yes, h.deck.shown, h.store.Len())
	}
}

func fakeKey() string {
	// built at run time so no scanner mistakes the fixture for a real key
	return "sk-" + "ant-" + "api03-" + strings.Repeat("Zx9Qw7Lm", 4)
}

func TestSecretsAreNeverSpokenOrShown(t *testing.T) {
	key := fakeKey()
	for _, s := range []string{"Read proactive audit", "status of proactive audit", "proactive audit ka jawab padho"} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			h.deck.replies[idAudit] = "Done. Use this key: " + key + " for the next call."
			got := h.say(s)
			b, _ := json.Marshal(got)
			if strings.Contains(string(b), "Zx9Qw7Lm") {
				t.Fatalf("secret leaked: %s", b)
			}
			if !strings.Contains(got.Say, "There is a secret in this reply, read it on screen.") {
				t.Errorf("say = %q", got.Say)
			}
		})
	}
	t.Run("tell with a key", func(t *testing.T) {
		h := newHarness(t)
		got := h.do(Request{Sentence: "tell deck conductor to use the token " + key, On: idConductor})
		b, _ := json.Marshal(got)
		if refusedOf(got) != RefusedUnsafe || len(h.deck.sent) != 0 || strings.Contains(string(b), "Zx9Qw7Lm") {
			t.Fatalf("got %s sent=%+v", b, h.deck.sent)
		}
	})
	t.Run("password in a reason line", func(t *testing.T) {
		h := newHarness(t)
		for i := range h.deck.sessions {
			if h.deck.sessions[i].ID == idPhone {
				h.deck.sessions[i].Reason = "asks: password = hunter2hunter2"
			}
		}
		got := h.say("who is waiting for me")
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), "hunter2") {
			t.Fatalf("secret in show: %s", b)
		}
	})
}

func TestSecretDetector(t *testing.T) {
	secrets := []string{
		fakeKey(), "ghp_" + strings.Repeat("a1B2c3D4", 5), "AKIA" + "ABCDEFGHIJ234567", "xoxb-" + "123456789012-abcdefABCDEF",
		"password: correct-horse-9", "token=abc123def456ghi", "-----BEGIN RSA " + "PRIVATE KEY-----",
		"https://user:s3cretpass@example.com/repo", "Bearer abcdefghijklmnop1234",
		"eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKKF2QT4fw", "Q2xvdWQgaXMgZnVuIGJ1dCAxMjM0NTY3OGFiY2Rl",
	}
	for _, s := range secrets {
		if !HasSecret("here: " + s) {
			t.Errorf("missed secret %q", s)
		}
	}
	clean := []string{
		"Tests are running on the new build.", "session a1000001-1700000001 is waiting",
		"commit 80494e5214a1c4c18f2f36a4c0b6ab4f1d0e7c3a landed", "id 3f1f2a4e-9b7c-4e1a-8f00-2c1d5e6f7a8b",
		"see internal/voicedesk/grammar_test.go for the table", "conductor-deck-alex-phone-build-voice", "The token budget is 4000.",
	}
	for _, s := range clean {
		if HasSecret(s) {
			t.Errorf("false secret in %q", s)
		}
	}
}

func TestFindChoiceThenSwitch(t *testing.T) {
	for _, answer := range []string{"second", "the second", "doosra", "दूसरा", "tailscale fix"} {
		t.Run(answer, func(t *testing.T) {
			h := newHarness(t)
			ask := h.say("find the tailscale session")
			if ask.Confirm == nil || ask.Confirm.Kind != "choice" || len(ask.Show.Items) != 2 {
				t.Fatalf("ask = %+v", ask)
			}
			if !strings.HasPrefix(ask.Say, "Two: ") || !strings.HasSuffix(ask.Say, "First or second?") {
				t.Errorf("say = %q", ask.Say)
			}
			// the second spoken name
			want := ask.Show.Items[1].ID
			if answer == "tailscale fix" {
				want = idTsFix
			}
			got := h.do(Request{Sentence: answer, Pending: ask.Confirm.Token, AnswerDelayMs: 700, HasAnswerDelay: true})
			if got.Then == nil || got.Then.Action != "switch" || got.Then.Target.ID != want {
				t.Fatalf("got %+v", got)
			}
			if len(h.deck.shown) != 1 || h.deck.shown[0] != want {
				t.Errorf("shown = %v", h.deck.shown)
			}
		})
	}
}

func TestFindOneThenYesSwitches(t *testing.T) {
	h := newHarness(t)
	ask := h.say("find the phone build session")
	if ask.Say != "Found phone build. Switch to it?" {
		t.Fatalf("say = %q", ask.Say)
	}
	got := h.do(Request{Sentence: "haan", Pending: ask.Confirm.Token, AnswerDelayMs: 700, HasAnswerDelay: true})
	if got.Then == nil || got.Then.Target.ID != idPhone || got.Say != "Switching to phone build." {
		t.Fatalf("got %+v", got)
	}
}

func TestLookAlikeAsksThenTellReadsBack(t *testing.T) {
	h := newHarness(t)
	ask := h.say("tell voice text to run the tests")
	if refusedOf(ask) != RefusedAmbiguous || ask.Say != "Did you mean voice test?" || len(h.deck.sent) != 0 {
		t.Fatalf("look-alike was not asked: %+v", ask)
	}
	pick := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 700, HasAnswerDelay: true})
	if pick.Confirm == nil || pick.Say != "Send to voice test: run the tests. Yes?" || len(h.deck.sent) != 0 {
		t.Fatalf("after the choice: %+v", pick)
	}
	sent := h.do(Request{Sentence: "yes", Pending: pick.Confirm.Token, AnswerDelayMs: 700, HasAnswerDelay: true})
	if len(h.deck.sent) != 1 || h.deck.sent[0].id != idVoiceTest || sent.Say != "Sent to voice test." {
		t.Fatalf("sent=%+v got=%+v", h.deck.sent, sent)
	}
}

func TestAmbiguousNamesAskAndNeverPick(t *testing.T) {
	h := newHarness(t)
	ask := h.say("tell conductor to check the build")
	if refusedOf(ask) != RefusedAmbiguous || ask.Confirm == nil || ask.Confirm.Kind != "choice" || len(h.deck.sent) != 0 {
		t.Fatalf("got %+v", ask)
	}
	if ask.Say != "Did you mean conductor deck alex or conductor boxone on boxone?" {
		t.Errorf("say = %q", ask.Say)
	}
	for _, it := range ask.Show.Items {
		if strings.Contains(it.Title, "sam") || strings.Contains(it.Title, "robin") {
			t.Errorf("someone else's session offered: %+v", it)
		}
	}
}

func TestSpokenLines(t *testing.T) {
	cases := []struct{ sentence, say string }{
		{"who is waiting for me", "Three: tailscale fix on boxone, conductor deck alex, phone build."},
		{"how is everything", "Three working, three waiting for you, one failed: proactive audit."},
		{"list my sessions", "Eleven: conductor deck alex, phone build, tailscale fix on boxone, and 8 more on screen."},
		{"status of the mac app", "macapp redesign1 is working, 4 minutes in. Last: Progress Tests are running on the new build."},
		{"read deck conductor", "Two PRs are open, both green."},
		{"read proactive audit", "The audit found three stale sessions. Two of them are safe to archive."},
		{"status of conductor deck sam", "That is Sam's session."},
		{"tell phone walk to continue", "I found no session of yours called phone walk."},
		{"status of release notes", "release notes is stopped, 10 hours."},
	}
	for _, c := range cases {
		t.Run(c.sentence, func(t *testing.T) {
			h := newHarness(t)
			got := h.say(c.sentence)
			if got.Say != c.say {
				t.Fatalf("say = %q\nwant  %q", got.Say, c.say)
			}
		})
	}
}

func TestUnreachableHostIsSaidNotFailed(t *testing.T) {
	h := newHarness(t)
	h.deck.errs = []HostError{{Host: "boxtwo", Error: "unreachable"}}
	got := h.say("who is waiting for me")
	if !strings.HasSuffix(got.Say, "boxtwo did not answer.") {
		t.Fatalf("say = %q", got.Say)
	}
}

func TestSpeakable(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<speak>Short part.</speak> long rest", "Short part."},
		{"# Title\n\n- **one** thing. Two things! Three.", "Title one thing. Two things!"},
		{"See `make test` and [the docs](http://x). Done. More.", "See make test and the docs. Done."},
		{"```\ncode only\n```", ""},
		{strings.Repeat("word ", 80), strings.TrimSpace(strings.Repeat("word ", 48)) + "…"},
	}
	for _, c := range cases {
		if got := Speakable(c.in); got != c.want {
			t.Errorf("Speakable(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDevanagariFolding(t *testing.T) {
	cases := map[string]string{"बोलो": "bolo", "कंडक्टर": "kandaktar", "डेक": "dek", "हाँ": "haan", "पढ़ो": "padho", "ज़रूरत": "zaroorat", "क्या": "kyaa"}
	for in, want := range cases {
		if got := toRoman(in); got != want {
			t.Errorf("toRoman(%q) = %q, want %q", in, got, want)
		}
	}
	if fold("intezaar") != fold("intezar") || fold("dhoondo") != fold("doondo") || fold("nahin") != fold("nahi") {
		t.Errorf("fold does not join common Roman Hindi spellings")
	}
}
