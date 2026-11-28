package voicedesk

import (
	"strings"
	"testing"
)

// N1: the next-one forms in the grammar are handled.
func TestReworkNextOneForms(t *testing.T) {
	for _, s := range []string{"go to the next one", "go to the next one that needs me", "show me the next one", "show me the next one that needs me"} {
		h := phase2Harness(t)
		if r := h.say(s); !r.Handled || r.Intent != IntentNext {
			t.Errorf("%q: handled=%v intent=%s", s, r.Handled, r.Intent)
		}
	}
}

// N1: an optional part of a form is one word; a spec like
// "[that needs me]" silently never matches.
func TestReworkGrammarOptionalIsOneWord(t *testing.T) {
	for _, p := range grammar {
		for _, e := range p.els {
			for _, w := range e.words {
				if strings.ContainsAny(w, "[]{}") {
					t.Errorf("intent %s: form element %q holds a bracket: a multi-word optional never matches", p.intent, w)
				}
			}
		}
	}
}
