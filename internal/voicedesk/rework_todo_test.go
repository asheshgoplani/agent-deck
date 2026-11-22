package voicedesk

import (
	"encoding/json"
	"strings"
	"testing"
)

// codingTodos is the reviewer's list: to-dos named with everyday coding words.
func codingTodos(t *testing.T) (*harness, *todoFake) {
	h, tf := todoHarness(t)
	tf.items = []Todo{{ID: "1", Text: "fix the build"}, {ID: "2", Text: "write the tests"}, {ID: "3", Text: "deploy the server"}, {ID: "4", Text: "update the docs"}}
	return h, tf
}

// M2: a bare "X is done" that names only part of a to-do is talk to the
// session; it never ticks and never asks.
func TestReworkTodoDoneOrdinaryTalkIsTalk(t *testing.T) {
	for _, s := range []string{"the build is done", "build ho gaya", "the test is finished", "done with the tests", "the server is done",
		"bill is done", "the docs is done", "deploy is done", "test ho gaya", "build done kar do", "टेस्ट हो गया", "the build khatam ho gaya",
		"deploy the server ho gaya"} {
		t.Run(s, func(t *testing.T) {
			h, tf := codingTodos(t)
			r := h.say(s)
			if len(tf.done) != 0 || r.Handled {
				t.Errorf("%q: handled=%v ticked=%v say=%q", s, r.Handled, tf.done, r.Say)
			}
		})
	}
}

// M2: a one-word to-do that is a coding word is still not ticked by a bare
// "the build is done".
func TestReworkTodoDoneCodingNounItemIsTalk(t *testing.T) {
	h, tf := todoHarness(t)
	tf.items = []Todo{{ID: "1", Text: "build"}}
	if r := h.say("the build is done"); r.Handled || len(tf.done) != 0 {
		t.Fatalf("handled=%v ticked=%v say=%q", r.Handled, tf.done, r.Say)
	}
}

// M2: the explicit forms on part of an item ask first; the yes ticks.
func TestReworkTodoDoneExplicitPartialAsks(t *testing.T) {
	for _, row := range []struct{ s, prompt, id string }{
		{"mark the test as done", "Tick off write the tests?", "2"},
		{"tick off build", "Tick off fix the build?", "1"},
		{"server wala ho gaya", "Tick off deploy the server?", "3"},
		{"build wala todo ho gaya", "Tick off fix the build?", "1"},
		{"cross the docs off my list", "Tick off update the docs?", "4"},
	} {
		t.Run(row.s, func(t *testing.T) {
			h, tf := codingTodos(t)
			ask := h.say(row.s)
			if len(tf.done) != 0 || ask.Confirm == nil || ask.Say != row.prompt {
				t.Fatalf("got ticked=%v confirm=%v say=%q", tf.done, ask.Confirm != nil, ask.Say)
			}
			got := h.yes(ask)
			if strings.Join(tf.done, ",") != row.id || !strings.HasPrefix(got.Say, "Ticked off:") {
				t.Fatalf("after yes: ticked=%v say=%q", tf.done, got.Say)
			}
		})
	}
	h, tf := codingTodos(t)
	ask := h.say("tick off build")
	if got := h.do(Request{Sentence: "no", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true}); len(tf.done) != 0 || got.Intent != IntentConfirmNo {
		t.Fatalf("a no ticked: %v %+v", tf.done, got)
	}
}

// M2: words that cover the whole item tick at once (plural allowed).
func TestReworkTodoDoneWholeItemTicks(t *testing.T) {
	for _, row := range []struct{ s, id string }{
		{"fix the build is done", "1"}, {"tick off fix the build", "1"}, {"write the test is done", "2"},
		{"fix the build ho gaya", "1"}, {"deploy the server wala ho gaya", "3"}, {"mark update the docs as done", "4"}, {"done with write the tests", "2"},
	} {
		t.Run(row.s, func(t *testing.T) {
			h, tf := codingTodos(t)
			r := h.say(row.s)
			if strings.Join(tf.done, ",") != row.id || r.Confirm != nil {
				t.Fatalf("ticked=%v confirm=%v say=%q", tf.done, r.Confirm != nil, r.Say)
			}
		})
	}
	// a prefix is not the whole word: "pay rent" / "pay rental" asks
	h, tf := todoHarness(t)
	tf.items = []Todo{{ID: "1", Text: "pay rental insurance"}}
	if r := h.say("tick off pay rent insurance"); len(tf.done) != 0 || r.Confirm == nil {
		t.Fatalf("prefix ticked: %v say=%q", tf.done, r.Say)
	}
}

// S1: ordinary talk is not read as a to-do command.
func TestReworkTodoOrdinaryTalkNotTaken(t *testing.T) {
	for _, s := range []string{"what is left to do", "what do i have to do", "add this to my list", "put this on my list",
		"ye list mein daal do", "ye yaad dilana", "ये लिस्ट में डाल दो", "isko list mein daal do", "add it to my list",
		"add a todo comment here", "add a to do comment above the function", "add todo comments to the parser", "list mein kya hai"} {
		t.Run(s, func(t *testing.T) {
			h, tf := todoHarness(t)
			r := h.say(s)
			if r.Handled || len(tf.items) != 5 {
				t.Errorf("%q taken: intent=%s say=%q items=%d", s, r.Intent, r.Say, len(tf.items))
			}
		})
	}
	for _, s := range []string{"what's on my list", "meri list dikhao", "show my to-dos", "what are my to-dos", "add this bug to my list"} {
		if r := h0(t).say(s); !r.Handled {
			t.Errorf("%q no longer handled", s)
		}
	}
}

func h0(t *testing.T) *harness { h, _ := todoHarness(t); return h }

// S5: the voice.result frame of a to-do answer carries no to-do text.
func TestReworkTodoFrameHasNoText(t *testing.T) {
	h, tf := codingTodos(t)
	tf.items = append(tf.items, Todo{ID: "5", Text: "fix the builder"})
	steps := []Result{
		h.say("add a to-do: water the plants"),
		h.say("what is on my list"),
		h.say("fix the build is done"),
	}
	ask := h.say("tick off builder")
	steps = append(steps, ask)
	if ask.Confirm != nil {
		steps = append(steps, h.yes(ask))
	}
	ask2 := h.say("tick off update docs")
	steps = append(steps, ask2)
	if ask2.Confirm != nil {
		steps = append(steps, h.do(Request{Sentence: "hmm", Pending: ask2.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true}))
	}
	for i, r := range steps {
		if !r.Handled {
			t.Fatalf("step %d not handled: %+v", i, r)
		}
		b, _ := json.Marshal(ForFrame(r))
		for _, txt := range []string{"plants", "build", "tests", "server", "docs"} {
			if strings.Contains(strings.ToLower(string(b)), txt) {
				t.Errorf("step %d (%s): frame holds %q: %s", i, r.Intent, txt, b)
			}
		}
		if r.Say == "" {
			t.Errorf("step %d: the caller's say lost", i)
		}
	}
	// a session answer is unchanged
	s := Result{Handled: true, Intent: IntentStatus, Show: Show{Kind: "sessions", Items: []ShowItem{{ID: "x", Title: "voice test"}}}}
	if f := ForFrame(s); f.Show.Items[0].Title != "voice test" {
		t.Fatalf("session frame changed: %+v", f)
	}
}

// N5: the contract's examples.
func TestReworkTodoContractExamples(t *testing.T) {
	rows := []struct{ s, intent, say string }{
		{"mere to-do batao", IntentTodoList, "Four to-dos:"},
		{"मेरे टू-डू बताओ", IntentTodoList, "Four to-dos:"},
		{"meri to-do list sunao", IntentTodoList, "Four to-dos:"},
		{"टू-डू में डालो: बैंक जाना", IntentTodoAdd, "Added: बैंक जाना."},
		{"tu du mein daalo: dawai lena", IntentTodoAdd, "Added: dawai lena."},
		{"what are my to-dos", IntentTodoList, "Four to-dos:"},
	}
	for _, r := range rows {
		t.Run(r.s, func(t *testing.T) {
			got := h0(t).say(r.s)
			if got.Intent != r.intent || !strings.HasPrefix(got.Say, r.say) {
				t.Fatalf("got intent=%s say=%q", got.Intent, got.Say)
			}
		})
	}
}
