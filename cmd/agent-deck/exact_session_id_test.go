package main

import (
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The collision these tests exist for: a session's TITLE is free text, so it
// can be set to another session's ID. ResolveSession tries titles before ids,
// which lets the title holder answer for an id it does not own.
func collidingInstances() []*session.Instance {
	return []*session.Instance{
		{ID: "aaaa1111-1", Title: "worker"},
		{ID: "bbbb2222-2", Title: "aaaa1111-1"},
	}
}

func TestExactSessionIDIsNotRedirectedByACollidingTitle(t *testing.T) {
	instances := collidingInstances()

	inst, errMsg, _ := ResolveSessionByExactID("aaaa1111-1", instances)
	if inst == nil {
		t.Fatalf("ResolveSessionByExactID(aaaa1111-1) found nothing: %s", errMsg)
	}
	if inst.ID != "aaaa1111-1" {
		t.Fatalf("ResolveSessionByExactID(aaaa1111-1) = %s, want the id holder", inst.ID)
	}

	// The contrast that says why the exact form is needed at all: the flexible
	// form answers the same identifier with the session merely titled after it.
	redirected, _, _ := ResolveSession("aaaa1111-1", instances)
	if redirected == nil || redirected.ID != "bbbb2222-2" {
		t.Fatal("ResolveSession no longer prefers a colliding title; the exact form's premise changed")
	}
}

func TestExactSessionIDDoesNotFallBackToAMatchingTitle(t *testing.T) {
	// Only the title holder exists, so the id itself is genuinely absent.
	instances := []*session.Instance{{ID: "bbbb2222-2", Title: "aaaa1111-1"}}

	inst, _, code := ResolveSessionByExactID("aaaa1111-1", instances)
	if inst != nil {
		t.Fatalf("ResolveSessionByExactID(aaaa1111-1) = %s, want no match", inst.ID)
	}
	if code != ErrCodeNotFound {
		t.Fatalf("code = %q, want %q", code, ErrCodeNotFound)
	}
}

func TestExactSessionIDDoesNotPrefixMatch(t *testing.T) {
	instances := []*session.Instance{{ID: "aaaa1111-1", Title: "worker"}}

	if inst, _, code := ResolveSessionByExactID("aaaa1111", instances); inst != nil {
		t.Fatalf("ResolveSessionByExactID(aaaa1111) = %s, want no match on a prefix", inst.ID)
	} else if code != ErrCodeNotFound {
		t.Fatalf("code = %q, want %q", code, ErrCodeNotFound)
	}

	// The control: the flexible form does resolve that prefix, so the case is
	// reachable and the exact form is what refuses it.
	if inst, _, _ := ResolveSession("aaaa1111", instances); inst == nil || inst.ID != "aaaa1111-1" {
		t.Fatal("ResolveSession no longer prefix-matches; this test no longer proves the difference")
	}
}

func TestExactSessionIDRequiresAnID(t *testing.T) {
	inst, errMsg, code := ResolveSessionByExactID("", collidingInstances())
	if inst != nil {
		t.Fatalf("ResolveSessionByExactID(\"\") = %s, want no match", inst.ID)
	}
	if code != ErrCodeNotFound {
		t.Fatalf("code = %q, want %q", code, ErrCodeNotFound)
	}
	if errMsg == "" {
		t.Fatal("an empty id must still explain itself")
	}
}

// The positional form is what every human caller and documented workflow types.
// Adding an exact form must not change any of it.
func TestPositionalResolutionIsUnchanged(t *testing.T) {
	instances := []*session.Instance{
		{ID: "aaaa1111-1", Title: "worker"},
		{ID: "bbbb2222-2", Title: "other"},
	}

	if inst, _, _ := ResolveSession("worker", instances); inst == nil || inst.ID != "aaaa1111-1" {
		t.Fatal("ResolveSession no longer resolves an exact title")
	}
	if inst, _, _ := ResolveSession("bbbb2222", instances); inst == nil || inst.ID != "bbbb2222-2" {
		t.Fatal("ResolveSession no longer resolves an id prefix when no title matches")
	}

	shared := []*session.Instance{
		{ID: "aaaa1111-1", Title: "twin"},
		{ID: "bbbb2222-2", Title: "twin"},
	}
	if _, _, code := ResolveSession("twin", shared); code != ErrCodeAmbiguous {
		t.Fatalf("a title two sessions share reported %q, want %q", code, ErrCodeAmbiguous)
	}
}

// Under --id every positional is message text, so no positional is left to stop flag parsing at a
// message that begins with a dash. The caller's own separator has to survive hoisting for that
// text to reach the session as a message rather than an undefined flag.
func TestSendSplitsTheCallerSeparatorBeforeHoisting(t *testing.T) {
	split := func(args []string) (flagArgs, literal []string) {
		flagArgs = args
		for i, arg := range args {
			if arg == "--" {
				return args[:i], args[i+1:]
			}
		}
		return flagArgs, nil
	}

	flagArgs, literal := split([]string{"--json", "--id", "aaaa1111-1", "--", "--not-a-flag"})
	if !reflect.DeepEqual(flagArgs, []string{"--json", "--id", "aaaa1111-1"}) {
		t.Fatalf("flagArgs = %q, want the flags before the separator", flagArgs)
	}
	if !reflect.DeepEqual(literal, []string{"--not-a-flag"}) {
		t.Fatalf("literal = %q, want the dash-leading message intact", literal)
	}

	// The legacy form keeps both of its positionals, in order.
	flagArgs, literal = split([]string{"--json", "--", "my-project", "hello"})
	if !reflect.DeepEqual(flagArgs, []string{"--json"}) {
		t.Fatalf("flagArgs = %q, want only the flag", flagArgs)
	}
	if !reflect.DeepEqual(literal, []string{"my-project", "hello"}) {
		t.Fatalf("literal = %q, want session ref then message", literal)
	}

	// A caller that wrote no separator is unaffected.
	flagArgs, literal = split([]string{"my-project", "hello", "--json"})
	if literal != nil {
		t.Fatalf("literal = %q, want none without a separator", literal)
	}
	if len(flagArgs) != 3 {
		t.Fatalf("flagArgs = %q, want every argument left to hoisting", flagArgs)
	}
}
