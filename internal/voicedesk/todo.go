package voicedesk

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// To-dos by voice: add, read the list, tick one off. They use the `todo`
// command (one list per host); a deck without it says so. Ticking off the
// whole item needs no yes (todo undo exists); a part of an item asks first,
// or is talk in a bare "X is done" (see todoDone).

// Todo is one item of his list.
type Todo struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// TodoDeck is the optional to-do part of a Deck.
type TodoDeck interface {
	TodoAdd(ctx context.Context, text string) (Todo, error)
	TodoList(ctx context.Context) ([]Todo, error)
	TodoDone(ctx context.Context, id string) (Todo, error)
}

const actionTodoDone = "todo_done"

func grammarTodos(a *patternAdder) {
	a.add(IntentTodoAdd, LangEN, false, false,
		"add [a|an] [new] todo|todos [item] {msg}", "add [a|an] [new] to do [item] {msg}",
		"new todo|to do {msg}", "add {msg} to my todo|to do [list]", "add {msg} to my list",
		"put {msg} on my todo|to do [list]", "put {msg} on my list", "remind me to {msg}",
	)
	for _, td := range []string{"todo", "to do", "tu du"} { // "टू-डू" is heard as "tu du"
		a.add(IntentTodoAdd, LangHI, false, false,
			td+" mein|me daalo|dalo|daal|likho|likh [do] {msg}", "{msg} "+td+" mein|me daalo|dalo|daal|likho|likh [do]",
			td+" add karo {msg}",
		)
	}
	a.add(IntentTodoAdd, LangHI, false, false,
		"{msg} [meri] list mein|me daalo|dalo|daal|likho|likh [do]", "{msg} yaad dilana",
	)
	// reading the list needs "list" or "to-do" with "my"/"meri": "what is
	// left to do", "list mein kya hai" are talk
	for _, lst := range []string{"list", "todo", "to do", "tu du", "todo list", "to do list", "tu du list", "todos", "to dos"} {
		a.add(IntentTodoList, LangEN, false, false,
			"[what] is|whats on my "+lst, "read|show my "+lst, "list my "+lst, "what are my "+lst,
		)
		a.add(IntentTodoList, LangHI, false, false,
			"meri|mera|mere "+lst+" sunao|batao|dikhao|padho", "meri|mera|mere "+lst+" mein|me kya hai",
		)
	}
	// done. Explicit to-do forms (not soft): a part of an item asks
	// "Tick off X?". Bare forms (soft) are what he says to a coding session
	// all day ("the build is done", "build ho gaya"): they tick only when
	// the words are the whole item, else they are talk (see todoDone).
	a.add(IntentTodoDone, LangEN, false, false,
		"mark {msg} [as] done", "tick off {msg}", "check off {msg}", "tick {msg} off [my] [list]",
		"cross {msg} off [my] [list]", "cross off {msg}", "{msg} is done on my list",
		"{msg} is done on my todo [list]", "{msg} is done on my to do [list]",
	)
	a.add(IntentTodoDone, LangHI, false, false,
		"list mein|me {msg} wala|wali|wale ho gaya|gayi|gaye",
		"{msg} wala|wali|wale todo ho gaya|gayi|gaye", "{msg} wala|wali|wale to do ho gaya|gayi|gaye",
		"{msg} wala|wali|wale tu du ho gaya|gayi|gaye",
		"{msg} wala|wali|wale ho gaya|gayi|gaye", "{msg} wala|wali|wale done kar|karo [do]",
		"{msg} wala|wali|wale khatam [ho] [gaya|gayi]",
	)
	a.add(IntentTodoDone, LangEN, true, false,
		"{msg} is|are done|finished", "done with {msg}",
	)
	a.add(IntentTodoDone, LangHI, true, false,
		"{msg} ho gaya|gayi|gaye", "{msg} done kar|karo [do]", "{msg} khatam [ho] [gaya|gayi]",
	)
}

func (d *Desk) todos() (TodoDeck, bool) {
	td, ok := d.Deck.(TodoDeck)
	return td, ok
}

func (d *Desk) runTodo(ctx context.Context, req Request, r Result, p Parsed) (Result, error) {
	td, ok := d.todos()
	unavailable := func() (Result, error) {
		if p.Intent == IntentTodoDone {
			return notMine(p.Lang), nil
		}
		r.Say = "To-dos are not available in this build."
		return r, nil
	}
	if !ok {
		return unavailable()
	}
	switch p.Intent {
	case IntentTodoAdd:
		text := strings.TrimSpace(strings.Trim(strings.TrimSpace(p.Message), ":,;-"))
		if text == "" || todoTalkText(text) {
			return notMine(p.Lang), nil
		}
		if HasSecret(text) {
			refuse(&r, RefusedUnsafe, "I will not put a secret on your list; type it instead.")
			return r, nil
		}
		if req.DryRun {
			r.Say = "Added: " + ensureStop(text)
			return r, nil
		}
		it, err := td.TodoAdd(ctx, text)
		if errors.Is(err, ErrUnsupported) {
			return unavailable()
		}
		if err != nil {
			r.Say = "I could not add that to your list."
			return r, nil
		}
		r.Say = "Added: " + ensureStop(it.Text)
		r.Show = Show{Kind: "todos", Items: []ShowItem{{ID: it.ID, Title: it.Text, Status: "open"}}}
		return r, nil
	case IntentTodoList:
		items, err := td.TodoList(ctx)
		if errors.Is(err, ErrUnsupported) {
			return unavailable()
		}
		if err != nil {
			r.Say = "I could not read your list."
			return r, nil
		}
		var open []Todo
		for _, it := range items {
			if !it.Done {
				open = append(open, it)
			}
		}
		r.Show = Show{Kind: "todos", Items: todoItems(open)}
		if len(open) == 0 {
			r.Say = "Your list is empty."
			return r, nil
		}
		names := make([]string, len(open))
		for i, it := range open {
			names[i] = strings.TrimRight(it.Text, ".")
		}
		r.Say = strings.Replace(spokenList(names), ":", plural2(len(open), " to-do:", " to-dos:"), 1)
		return r, nil
	case IntentTodoDone:
		return d.todoDone(ctx, req, r, p, td)
	}
	return r, errors.New("not a to-do intent")
}

func todoItems(ts []Todo) []ShowItem {
	out := make([]ShowItem, 0, len(ts))
	for _, t := range ts {
		st := "open"
		if t.Done {
			st = "done"
		}
		out = append(out, ShowItem{ID: t.ID, Title: t.Text, Status: st})
	}
	return out
}

// todoDone matches the spoken words against the open items.
//
// Whole: every spoken word is a word of the item and every word of the item
// was said (the same word, or a plural "test"/"tests"; a Devanagari word by
// its vowels). One whole item is ticked off at once (todo undo exists).
// Part: every spoken word is a word of the item, but not the whole item.
// An explicit form ("tick off build", "build wala ho gaya", "the build one
// is done") asks "Tick off fix the build?" or "Did you mean A or B?" and
// ticks only on the answer. Near: a word only by a prefix or by sound
// ("rent"/"rental", "plan"/"plane"): an explicit form changes nothing and
// says so, as `todo done` answers close_match. A bare form ("the build is done", "build ho
// gaya") is what he says to a coding session all day: a part is talk, and
// so is a whole item made only of coding words ("build"). None: not mine.
func (d *Desk) todoDone(ctx context.Context, req Request, r Result, p Parsed, td TodoDeck) (Result, error) {
	q := newNameQuery(p.Message, nil)
	var words, roman []string
	var deva []bool
	for i, w := range q.words {
		if todoNoise[w] {
			continue
		}
		words, roman, deva = append(words, w), append(roman, q.roman[i]), append(deva, q.deva[i])
	}
	q.words, q.roman, q.deva = words, roman, deva
	if len(q.words) == 0 {
		return notMine(p.Lang), nil
	}
	items, err := td.TodoList(ctx)
	if err != nil {
		return notMine(p.Lang), nil
	}
	bare := p.Soft
	// "the bank one is done": "one" points at a list item, so this is an
	// explicit form for "bank" (it asks), unless an item has the word "one".
	if n := len(q.words); n > 1 && (q.words[n-1] == "one" || q.words[n-1] == "1") && !todoHasWord(items, q.words[n-1]) {
		q.words, q.roman, q.deva = q.words[:n-1], q.roman[:n-1], q.deva[:n-1]
		bare = false
	}
	if bare && genericOnlyQ(q) {
		return notMine(p.Lang), nil // "the build is done", "test ho gaya"
	}
	var whole, part []Todo
	near := false
	for _, it := range items {
		if it.Done {
			continue
		}
		if todoWhole(q, it.Text) {
			whole = append(whole, it)
			continue
		}
		if todoPart(q, it.Text) {
			part = append(part, it)
		} else if ok, level, _ := coverage(q, Session{Title: it.Text}); ok && level >= wordPhonetic {
			near = true
		}
	}
	if len(whole) == 1 {
		return d.tickOff(ctx, req, r, td, whole[0])
	}
	hits := whole
	if len(hits) == 0 {
		if bare {
			return notMine(p.Lang), nil
		}
		hits = part
	}
	if len(hits) == 0 {
		if !bare && near {
			// a close match only ("tick off pay rent" for "pay rental
			// insurance"): nothing changes, as `todo done` answers close_match
			refuse(&r, RefusedNotFound, "No to-do is called "+strings.Join(q.roman, " ")+"; say its whole name.")
			return r, nil
		}
		return notMine(p.Lang), nil
	}
	choices := hits
	if len(choices) > 3 {
		choices = choices[:3]
	}
	names := make([]string, len(choices))
	refs := make([]Ref, len(choices))
	for i, c := range choices {
		names[i] = c.Text
		refs[i] = Ref{ID: c.ID, Title: c.Text}
	}
	r.Show = Show{Kind: "todos", Items: todoItems(choices)}
	if len(choices) == 1 {
		// one choice: a yes ticks it, a no or silence ticks nothing
		prompt := "Tick off " + strings.TrimRight(names[0], ".?! ") + "?"
		r.Say = prompt
		return d.pend(req, r, Pending{Profile: req.Profile, Kind: "choice", Action: actionTodoDone, Intent: IntentTodoDone, Lang: p.Lang, Choices: refs}, prompt)
	}
	prompt := "Did you mean " + strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1] + "?"
	why := RefusedAmbiguous
	r.Refused = &why
	r.Say = prompt
	return d.pend(req, r, Pending{Profile: req.Profile, Kind: "choice", Action: actionTodoDone, Intent: IntentTodoDone, Lang: p.Lang, Choices: refs}, prompt)
}

// todoWhole: the spoken words are the item's words, all of them.
func todoWhole(q nameQuery, text string) bool {
	var iw []word
	for _, w := range splitWords(text) {
		if !todoNoise[w.folded] && !todoGlue[w.folded] {
			iw = append(iw, w)
		}
	}
	if len(iw) == 0 {
		return false
	}
	for i, w := range q.words {
		found := false
		for _, x := range iw {
			if todoSameWord(w, x.folded, q.deva[i]) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, x := range iw {
		found := false
		for i, w := range q.words {
			if todoSameWord(w, x.folded, q.deva[i] || x.deva) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// todoPart: every spoken word is the same word as one of the item's words
// (todoSameWord: no prefix, no sound-alike).
func todoPart(q nameQuery, text string) bool {
	iw := splitWords(text)
	for i, w := range q.words {
		found := false
		for _, x := range iw {
			if todoSameWord(w, x.folded, q.deva[i] || x.deva) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(q.words) > 0
}

// todoHasWord: an open item holds the word w.
func todoHasWord(items []Todo, w string) bool {
	for _, it := range items {
		if it.Done {
			continue
		}
		for _, x := range splitWords(it.Text) {
			if x.folded == w {
				return true
			}
		}
	}
	return false
}

// todoSameWord: equal, a plural ending (s, es), or for a word heard in
// Devanagari the same vowel classes. A prefix ("rent"/"rental") or a sound
// alike ("bill"/"build") is not the same word.
func todoSameWord(a, b string, deva bool) bool {
	if a == b {
		return true
	}
	for _, suf := range []string{"s", "es"} {
		if a+suf == b || b+suf == a {
			return true
		}
	}
	return deva && utf8.RuneCountInString(a) >= 3 && vowelClass(a) == vowelClass(b)
}

// todoGlue are small words of an item he need not say ("write tests is done"
// is the whole of "write the tests for the parser"? no: "parser" was not said).
var todoGlue = foldSet("of", "for", "in", "on", "at", "with", "and", "ka", "ki", "ke", "ko", "se", "mein", "me")

// todoTalkText: an added text that is not a to-do: only a pronoun ("add
// this to my list", "ye yaad dilana") or a code comment ("add a todo
// comment here").
func todoTalkText(text string) bool {
	ws := splitWords(text)
	if len(ws) == 0 {
		return true
	}
	if todoComment[ws[0].folded] {
		return true
	}
	for _, w := range ws {
		if !todoPronoun[w.folded] && !todoNoise[w.folded] {
			return false
		}
	}
	return true
}

var todoComment = foldSet("comment", "comments")

var todoPronoun = foldSet("this", "that", "it", "these", "those", "them", "one", "ye", "yeh", "yah", "ya", "ie", "ia",
	"wo", "woh", "vo", "voh", "ise", "isse", "isko", "isako", "use", "usko", "usako", "inhe", "unhe", "ये", "यह", "वो", "वह", "इसे", "इसको", "उसे", "उसको")

var todoNoise = foldSet("the", "my", "a", "an", "as", "to", "do", "todo", "item", "wala", "wali", "wale", "vala", "vali", "kaam", "task")

func (d *Desk) tickOff(ctx context.Context, req Request, r Result, td TodoDeck, it Todo) (Result, error) {
	if !req.DryRun {
		done, err := td.TodoDone(ctx, it.ID)
		if err != nil {
			r.Say = "I could not tick that off."
			return r, nil
		}
		it = done
	}
	r.Say = "Ticked off: " + ensureStop(it.Text)
	r.Show = Show{Kind: "todos", Items: []ShowItem{{ID: it.ID, Title: it.Text, Status: "done"}}}
	return r, nil
}

// ForFrame is r as published in a voice.result frame. A to-do answer
// (todo.changed promises "never the text") keeps its ids and statuses but
// loses every to-do text: the titles on screen, the question and say. The
// caller still speaks r.Say from the result it got, not from the frame.
func ForFrame(r Result) Result {
	if r.Show.Kind != "todos" && !strings.HasPrefix(r.Intent, "todo_") {
		return r
	}
	r.Say = ""
	items := make([]ShowItem, len(r.Show.Items))
	for i, it := range r.Show.Items {
		it.Title, it.Line = "", ""
		items[i] = it
	}
	r.Show.Items = items
	if r.Confirm != nil {
		c := *r.Confirm
		c.Prompt = ""
		r.Confirm = &c
	}
	if r.Target != nil {
		t := *r.Target
		t.Title = ""
		r.Target = &t
	}
	return r
}
