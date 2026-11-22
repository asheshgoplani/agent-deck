package voicedesk

import (
	"strings"
	"testing"
)

// nearTodos is the phone lane's list: items whose words are close to, but
// not the same as, what he says ("rent"/"rental", "plan"/"plane").
func nearTodos(t *testing.T) (*harness, *todoFake) {
	h, tf := todoHarness(t)
	tf.items = []Todo{{ID: "1", Text: "book the plane"}, {ID: "2", Text: "pay rental insurance"}, {ID: "3", Text: "call the bank"}, {ID: "4", Text: "call the bank manager"}}
	return h, tf
}

// A near match is refused as the CLI does (todo done: close_match, nothing
// changes): no tick, and no question a yes could turn into a wrong tick.
func TestReworkTodoNearMatchRefusedLikeCLI(t *testing.T) {
	for _, s := range []string{"tick off pay rent", "tick off plan", "mark plan as done", "plan wala ho gaya", "cross pay rent off my list"} {
		t.Run(s, func(t *testing.T) {
			h, tf := nearTodos(t)
			r := h.say(s)
			if len(tf.done) != 0 {
				t.Fatalf("ticked %v", tf.done)
			}
			if r.Confirm != nil {
				t.Fatalf("asks %q: a yes would tick a to-do he did not name", r.Say)
			}
			if !r.Handled || refusedOf(r) != RefusedNotFound || !strings.Contains(r.Say, "whole name") {
				t.Fatalf("handled=%v refused=%q say=%q, want a not-found line asking for the whole name", r.Handled, refusedOf(r), r.Say)
			}
		})
	}
}

// "the X one is done" points at a list item: one fit asks "Tick off X?",
// several fits ask which one; nothing is ticked before the answer.
func TestReworkTodoTheXOneIsDone(t *testing.T) {
	for _, row := range []struct{ s, say string }{
		{"the plane one is done", "Tick off book the plane?"},
		{"tick off the plane one", "Tick off book the plane?"},
		{"the bank one is done", "Did you mean call the bank or call the bank manager?"},
		{"the manager one is done", "Tick off call the bank manager?"},
	} {
		t.Run(row.s, func(t *testing.T) {
			h, tf := nearTodos(t)
			r := h.say(row.s)
			if len(tf.done) != 0 || r.Confirm == nil || r.Say != row.say {
				t.Fatalf("handled=%v confirm=%v ticked=%v say=%q, want %q", r.Handled, r.Confirm != nil, tf.done, r.Say, row.say)
			}
		})
	}
	h, tf := nearTodos(t)
	ask := h.say("the plane one is done")
	if got := h.yes(ask); strings.Join(tf.done, ",") != "1" {
		t.Fatalf("after yes: ticked=%v say=%q", tf.done, got.Say)
	}
}

// "the plan one is done" is a near match too: refused, never asked.
func TestReworkTodoTheXOneNearRefused(t *testing.T) {
	h, tf := nearTodos(t)
	r := h.say("the plan one is done")
	if len(tf.done) != 0 || r.Confirm != nil || refusedOf(r) != RefusedNotFound {
		t.Fatalf("handled=%v confirm=%v ticked=%v say=%q", r.Handled, r.Confirm != nil, tf.done, r.Say)
	}
}
