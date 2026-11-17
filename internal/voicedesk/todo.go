package voicedesk

import (
	"context"
	"errors"
	"strings"
)

// To-dos by voice: add, read the list, tick one off. They use the `todo`
// command (one list per host); a deck without it says so. Ticking off needs
// no yes (todo undo exists), but two close matches ask which one.

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
	for _, td := range []string{"todo", "to do"} {
		a.add(IntentTodoAdd, LangHI, false, false,
			td+" mein|me daalo|dalo|daal|likho|likh [do] {msg}", "{msg} "+td+" mein|me daalo|dalo|daal|likho|likh [do]",
			td+" add karo {msg}",
		)
	}
	a.add(IntentTodoAdd, LangHI, false, false,
		"{msg} [meri] list mein|me daalo|dalo|daal|likho|likh [do]", "{msg} yaad dilana",
	)
	for _, lst := range []string{"list", "todo list", "to do list", "todos", "to dos"} {
		a.add(IntentTodoList, LangEN, false, false,
			"[what] is|whats on my "+lst, "read|show my "+lst, "list my "+lst, "what are my "+lst,
		)
		a.add(IntentTodoList, LangHI, false, false,
			"meri|mera|mere "+lst+" sunao|batao|dikhao|padho", "meri|mera|mere "+lst+" mein|me kya hai",
		)
	}
	a.add(IntentTodoList, LangEN, false, false, "what do i have to do", "what is left to do")
	a.add(IntentTodoList, LangHI, false, false, "list mein|me kya hai")
	// done: an unknown item is not mine ("the build is done" is talk)
	a.add(IntentTodoDone, LangEN, true, false,
		"{msg} is done|finished", "mark {msg} [as] done", "tick off {msg}", "check off {msg}", "done with {msg}",
	)
	a.add(IntentTodoDone, LangHI, true, false,
		"{msg} [wala|wali|wale] ho gaya|gayi|gaye", "{msg} done kar|karo [do]", "{msg} [wala|wali] khatam [ho] [gaya|gayi]",
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
		if text == "" {
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

// todoDone matches the spoken words against the open items: every content
// word must be in the item (sure or by sound). One match is ticked off;
// two or more ask; none is not the desk's.
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
	var hits []Todo
	for _, it := range items {
		if it.Done {
			continue
		}
		if ok, level, _ := coverage(q, Session{Title: it.Text}); ok && level >= wordPhonetic {
			hits = append(hits, it)
		}
	}
	switch len(hits) {
	case 0:
		return notMine(p.Lang), nil
	case 1:
		return d.tickOff(ctx, req, r, td, hits[0])
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
	prompt := "Did you mean " + strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1] + "?"
	why := RefusedAmbiguous
	r.Refused = &why
	r.Say = prompt
	r.Show = Show{Kind: "todos", Items: todoItems(hits)}
	return d.pend(req, r, Pending{Profile: req.Profile, Kind: "choice", Action: actionTodoDone, Intent: IntentTodoDone, Lang: p.Lang, Choices: refs}, prompt)
}

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
