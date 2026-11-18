package voicedesk

import (
	"strings"
	"testing"
)

// Lane-1 style: the listener writes English words of a Hindi sentence
// phonetically, in Roman or in Devanagari. A name that sounds like exactly
// one of his sessions (every word, nothing else close) is that session, and
// the line names it; two sound-alikes ask.

const (
	idGarden  = "f1000001-1700000041"
	idBilling = "f1000002-1700000042"
	idMusic   = "f1000003-1700000043"
	idRecipe  = "f1000004-1700000044"
	idKitchen = "f1000005-1700000045"
	idWeather = "f1000006-1700000046"
)

func phoneticHarness(t *testing.T) *harness {
	h := newHarness(t)
	for _, s := range []Session{
		{ID: idGarden, Title: "garden-planner", Group: "team/alex/home", Profile: "work", Status: "idle"},
		{ID: idBilling, Title: "billing-dashboard", Group: "team/alex/work", Profile: "work", Status: "running"},
		{ID: idMusic, Title: "music-player", Group: "team/alex/home", Profile: "work", Status: "idle"},
		{ID: idRecipe, Title: "recipe-book", Group: "team/alex/home", Profile: "work", Status: "idle"},
		{ID: idKitchen, Title: "kitchen-timer", Group: "team/alex/home", Profile: "default", Host: "boxone", Status: "idle"},
		{ID: idWeather, Title: "weather-station", Group: "team/alex/home", Profile: "work", Status: "waiting"},
	} {
		h.addSession(s)
	}
	h.deck.replies[idMusic] = "The playlist is ready. Twelve songs."
	return h
}

func TestPhoneticNames(t *testing.T) {
	rows := []struct {
		sentence, intent, target, confirm, sayPrefix string
	}{
		{"biling daishbord ka status kyaa hai", IntentStatus, idBilling, "", "billing dashboard is working"},
		{"बिलिंग डैशबोर्ड का स्टेटस क्या है", IntentStatus, idBilling, "", "billing dashboard is working"},
		{"myoozik pleyar ka javaab padho", IntentRead, idMusic, "", "Music player: The playlist is ready."},
		{"म्यूज़िक प्लेयर का जवाब पढ़ो", IntentRead, idMusic, "", "Music player: The playlist is ready."},
		{"gaarden plaanar ko bolo ki tamaatar ko paanee do", IntentTell, idGarden, "yes", "Send to garden planner: tamaatar ko paanee do. Yes?"},
		{"गार्डन प्लानर को बोलो कि टमाटर को पानी दो", IntentTell, idGarden, "yes", "Send to garden planner: टमाटर को पानी दो. Yes?"},
		{"resipee buk ko bolo ki soop ki resipee aid karo", IntentTell, idRecipe, "yes", "Send to recipe book: "},
		{"रेसिपी बुक को बोलो कि सूप की रेसिपी ऐड करो", IntentTell, idRecipe, "yes", "Send to recipe book: "},
		{"kichan taaimar ko bolo ki das minat baad bajaao", IntentTell, idKitchen, "yes", "Send to kitchen timer on boxone: "},
		{"किचन टाइमर को बोलो कि दस मिनट बाद बजाओ", IntentTell, idKitchen, "yes", "Send to kitchen timer on boxone: "},
		{"vedar vaali seshan dhoondho", IntentFind, idWeather, "yes", "Found weather station."},
		{"वेदर वाली सेशन ढूंढो", IntentFind, idWeather, "yes", "Found weather station."},
		{"vedar steshan kya kar raha hai", IntentStatus, idWeather, "", "weather station is waiting for you"},
	}
	for _, r := range rows {
		t.Run(r.sentence, func(t *testing.T) {
			h := phoneticHarness(t)
			got := h.say(r.sentence)
			kind := ""
			if got.Confirm != nil {
				kind = got.Confirm.Kind
			}
			if got.Intent != r.intent || targetID(got) != r.target || kind != r.confirm || refusedOf(got) != "" || !strings.HasPrefix(got.Say, r.sayPrefix) {
				t.Fatalf("got intent=%s target=%s confirm=%s refused=%s say=%q", got.Intent, targetID(got), kind, refusedOf(got), got.Say)
			}
		})
	}
}

// One Hindi send is one question (the read-back), then the yes sends.
func TestPhoneticTellIsOneQuestion(t *testing.T) {
	h := phoneticHarness(t)
	ask := h.say("gaarden plaanar ko bolo ki tamaatar ko paanee do")
	got := h.do(Request{Sentence: "haan bhejo", Pending: ask.Confirm.Token, AnswerDelayMs: 900, HasAnswerDelay: true})
	if got.Say != "Sent to garden planner." || len(h.deck.sent) != 1 || h.deck.sent[0].id != idGarden {
		t.Fatalf("got %+v sent=%+v", got, h.deck.sent)
	}
}

func TestPhoneticSafety(t *testing.T) {
	t.Run("two sound-alikes ask", func(t *testing.T) {
		h := phoneticHarness(t)
		h.addSession(Session{ID: "f1000007-1700000047", Title: "garden-planer", Group: "team/alex/home", Profile: "work", Status: "idle"})
		got := h.say("gaarden plaanar ka status kyaa hai")
		if refusedOf(got) != RefusedAmbiguous || got.Confirm == nil || targetID(got) != "" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("risky word still asks on his session", func(t *testing.T) {
		h := phoneticHarness(t)
		got := h.do(Request{Sentence: "gaarden plaanar ko bolo ki puraane nots mitaa do", On: idGarden})
		if got.Confirm == nil || len(h.deck.sent) != 0 {
			t.Fatalf("got %+v sent=%+v", got, h.deck.sent)
		}
	})
	t.Run("a different vowel is not the session", func(t *testing.T) {
		h := phoneticHarness(t)
		got := h.say("bolling dashboard ka status kya hai")
		if targetID(got) == idBilling && got.Confirm == nil {
			t.Fatalf("resolved by sound with a different vowel: %+v", got)
		}
	})
	t.Run("number rule stays exact", func(t *testing.T) {
		h := phoneticHarness(t)
		got := h.say("status of audit 0956")
		if refusedOf(got) != RefusedNotFound {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a find choice answer is intent choice", func(t *testing.T) {
		h := phoneticHarness(t)
		ask := h.say("find the tailscale session")
		got := h.do(Request{Sentence: "first", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
		if got.Intent != IntentChoice || got.Then == nil {
			t.Fatalf("got %+v", got)
		}
	})
}
