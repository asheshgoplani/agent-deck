package voicedesk

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// todoFake adds a to-do list to the fake deck.
type todoFake struct {
	*fakeDeck
	items []Todo
	done  []string
}

func (f *todoFake) TodoAdd(_ context.Context, text string) (Todo, error) {
	it := Todo{ID: strconv.Itoa(len(f.items) + 1), Text: text}
	f.items = append(f.items, it)
	return it, nil
}

func (f *todoFake) TodoList(context.Context) ([]Todo, error) {
	return append([]Todo(nil), f.items...), nil
}

func (f *todoFake) TodoDone(_ context.Context, id string) (Todo, error) {
	for i := range f.items {
		if f.items[i].ID == id {
			f.items[i].Done = true
			f.done = append(f.done, id)
			return f.items[i], nil
		}
	}
	return Todo{}, ErrUnsupported
}

func todoHarness(t *testing.T) (*harness, *todoFake) {
	h := newHarness(t)
	tf := &todoFake{fakeDeck: h.deck, items: []Todo{
		{ID: "1", Text: "call the bank"}, {ID: "2", Text: "buy milk"}, {ID: "3", Text: "buy oat milk"},
		{ID: "4", Text: "renew the passport"}, {ID: "5", Text: "old thing", Done: true},
	}}
	h.desk.Deck = tf
	return h, tf
}

func TestTodoByVoice(t *testing.T) {
	rows := []struct{ sentence, intent, lang, say, confirm string }{
		{"add a to-do: water the plants", IntentTodoAdd, LangEN, "Added: water the plants.", ""},
		{"add pay the rent to my list", IntentTodoAdd, LangEN, "Added: pay the rent.", ""},
		{"remind me to call mom", IntentTodoAdd, LangEN, "Added: call mom.", ""},
		{"to-do mein daalo: bank ko call karna", IntentTodoAdd, LangHI, "Added: bank ko call karna.", ""},
		{"doodh lana list mein daal do", IntentTodoAdd, LangHI, "Added: doodh lana.", ""},
		{"todo mein daalo dawai lena", IntentTodoAdd, LangHI, "Added: dawai lena.", ""},
		{"what is on my list", IntentTodoList, LangEN, "Four to-dos: call the bank, buy milk, buy oat milk, and 1 more on screen.", ""},
		{"read my to-dos", IntentTodoList, LangEN, "Four to-dos: call the bank, buy milk, buy oat milk, and 1 more on screen.", ""},
		{"meri list sunao", IntentTodoList, LangHI, "Four to-dos: call the bank, buy milk, buy oat milk, and 1 more on screen.", ""},
		{"meri to-do list batao", IntentTodoList, LangHI, "Four to-dos: call the bank, buy milk, buy oat milk, and 1 more on screen.", ""},
		{"the bank is done", IntentTodoDone, LangEN, "Ticked off: call the bank.", ""},
		{"mark renew the passport as done", IntentTodoDone, LangEN, "Ticked off: renew the passport.", ""},
		{"bank wala ho gaya", IntentTodoDone, LangHI, "Ticked off: call the bank.", ""},
		{"paasport ho gaya", IntentTodoDone, LangHI, "Ticked off: renew the passport.", ""},
		{"milk is done", IntentTodoDone, LangEN, "Did you mean buy milk or buy oat milk?", "choice"},
	}
	for _, r := range rows {
		t.Run(r.sentence, func(t *testing.T) {
			h, tf := todoHarness(t)
			got := h.say(r.sentence)
			kind := ""
			if got.Confirm != nil {
				kind = got.Confirm.Kind
			}
			if got.Intent != r.intent || got.Lang != r.lang || got.Say != r.say || kind != r.confirm {
				t.Fatalf("got intent=%s lang=%s confirm=%s say=%q", got.Intent, got.Lang, kind, got.Say)
			}
			if r.confirm != "" && len(tf.done) != 0 {
				t.Fatalf("ticked off while asking: %v", tf.done)
			}
		})
	}
}

func TestTodoDoneChoice(t *testing.T) {
	h, tf := todoHarness(t)
	ask := h.say("milk is done")
	got := h.do(Request{Sentence: "second", Pending: ask.Confirm.Token, AnswerDelayMs: 600, HasAnswerDelay: true})
	if got.Intent != IntentChoice || got.Say != "Ticked off: buy oat milk." || strings.Join(tf.done, ",") != "3" {
		t.Fatalf("got %+v done=%v", got, tf.done)
	}
}

// "X is done" without a matching open item is talk to the session.
func TestTodoDoneNotMine(t *testing.T) {
	for _, s := range []string{"the build is done", "the tests are done", "deploy ho gaya", "kaam ho gaya", "old thing is done", "done with the refactor"} {
		t.Run(s, func(t *testing.T) {
			h, tf := todoHarness(t)
			if got := h.say(s); got.Handled || len(tf.done) != 0 {
				t.Fatalf("stole %q: %+v", s, got)
			}
		})
	}
}

func TestTodoWithoutTheCommand(t *testing.T) {
	h := newHarness(t) // the fake deck has no to-do list
	if got := h.say("what is on my list"); got.Say != "To-dos are not available in this build." {
		t.Fatalf("got %+v", got)
	}
	if got := h.say("milk is done"); got.Handled {
		t.Fatalf("got %+v", got)
	}
}

func TestTodoSecretIsNotAdded(t *testing.T) {
	h, tf := todoHarness(t)
	got := h.say("add a to-do: rotate key " + fakeKey())
	if refusedOf(got) != RefusedUnsafe || len(tf.items) != 5 {
		t.Fatalf("got %+v items=%d", got, len(tf.items))
	}
}
