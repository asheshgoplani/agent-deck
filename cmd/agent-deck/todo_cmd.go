package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/todos"
)

const todoUsage = `Usage: agent-deck todo <command> [flags]

The owner's to-do list: one list per host, shared by every profile
(docs/todos.md). Every change publishes a todo.changed frame on the
events bus of every profile.

Commands:
  add "<text>" [--source s] [--session id] [--json]   Add an open item
  list [--all] [--json]                                Open items (--all: done ones too)
  done <id | words of the text> [--source s] [--json]  Tick an item off
  undo <id> [--source s] [--json]                      Reopen a done item
  remove <id> [--source s] [--json]                    Delete an item

--source is one of voice, cli, phone, mac, tui (default cli): on add it is
the item's source; on done, undo and remove it is "by" in the event.
"done" with words ticks off the one open item whose text has every word
(exact, a 3+ letter prefix, or one typo away in 5+ letter words next to
another word that matches exactly). When several items match it changes
nothing and exits 3 with the candidates; it never guesses. An exact match
of the whole text wins over longer ones, and an exact match of an item
already done answers already_done. A number (12, #12) is only an id.

Exit codes: 0 ok, 1 error (bad input, unreadable list), 2 no item
matches, 3 more than one item matches.

JSON (--json prints exactly one object on stdout):
  item:    {"id":"3","text":"call the bank","created_at":"RFC3339",
            "done_at":null|"RFC3339","source":"voice","session_id":"..."}
            (session_id only when set)
  list:    {"todos":[item...],"open":2,"done":1}
  add, done, undo, remove:
           {"status":"added|done|already_done|reopened|already_open|removed",
            "todo":item,"open":2}
  done no match:   {"status":"not_found","query":"bank"}            (exit 2)
  done ambiguous:  {"status":"ambiguous","query":"milk",
                    "candidates":[item...]}                         (exit 3)
  error:           {"status":"error","error":"..."}                 (exit 1)`

// Exit codes beyond 0 and 1 (the same meaning as `agent-deck switch`).
const (
	todoExitNotFound  = 2
	todoExitAmbiguous = 3
)

// todoEnv is what runTodo needs from the outside world.
type todoEnv struct {
	store   func() (*todos.Store, error)
	publish func(todos.Change)
}

func handleTodo(profile string, args []string) {
	env := todoEnv{
		store: todos.Open,
		publish: func(c todos.Change) {
			// The caller's profile is resolved only when there is a change
			// to publish, so help and list never touch profile state.
			resolved, err := session.ResolveProfileForStorage(profile)
			if err != nil {
				resolved = ""
			}
			todos.Publish(c, resolved)
		},
	}
	os.Exit(runTodo(args, os.Stdout, os.Stderr, env))
}

func runTodo(args []string, out, errOut io.Writer, env todoEnv) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, todoUsage)
		return 1
	}
	switch args[0] {
	case "help", "--help", "-h":
		fmt.Fprintln(out, todoUsage)
		return 0
	case "add":
		return runTodoAdd(args[1:], out, errOut, env)
	case "list", "ls":
		return runTodoList(args[1:], out, errOut, env)
	case "done":
		return runTodoChange("done", args[1:], out, errOut, env)
	case "undo":
		return runTodoChange("undo", args[1:], out, errOut, env)
	case "remove", "rm":
		return runTodoChange("remove", args[1:], out, errOut, env)
	}
	fmt.Fprintf(errOut, "Error: todo: unknown command %q\n\n%s\n", args[0], todoUsage)
	return 1
}

// todoParseFailed handles a flag parse error: help exits 0; any other
// error exits 1, as one JSON error object when --json was asked for (the
// flag package already printed the usage to stderr).
func todoParseFailed(err error, args []string, out, errOut io.Writer) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	for _, a := range args {
		if a == "--json" || a == "-json" || a == "--json=true" || a == "-json=true" {
			return todoFail(true, out, io.Discard, err)
		}
	}
	return 1
}

func todoFlagSet(name, usage string, args []string, out, errOut io.Writer) (*flag.FlagSet, *bool) {
	fs := flag.NewFlagSet("todo "+name, flag.ContinueOnError)
	fs.SetOutput(helpOutput(args, out, errOut))
	jsonOut := fs.Bool("json", false, "print one JSON object")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck todo "+usage)
		fmt.Fprintln(fs.Output(), "\nSee agent-deck todo --help for the JSON shapes and exit codes.")
		fs.PrintDefaults()
	}
	return fs, jsonOut
}

// todoFail prints err (as JSON with --json) and returns 1.
func todoFail(jsonOut bool, out, errOut io.Writer, err error) int {
	if jsonOut {
		_ = json.NewEncoder(out).Encode(todos.Result{Status: todos.StatusError, Error: err.Error()})
	}
	fmt.Fprintf(errOut, "Error: todo: %v\n", err)
	return 1
}

func todoEmit(jsonOut bool, out io.Writer, v any, text string) int {
	if jsonOut {
		if err := json.NewEncoder(out).Encode(v); err != nil {
			return 1
		}
		return 0
	}
	fmt.Fprintln(out, text)
	return 0
}

func runTodoAdd(args []string, out, errOut io.Writer, env todoEnv) int {
	fs, jsonOut := todoFlagSet("add", `add "<text>" [--source voice|cli|phone|mac|tui] [--session <id>] [--json]`, args, out, errOut)
	source := fs.String("source", todos.SourceCLI, "where the item came from: voice, cli, phone, mac or tui")
	sessionID := fs.String("session", "", "optional session id the item belongs to")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return todoParseFailed(err, args, out, errOut)
	}
	text := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(text) == "" {
		return todoFail(*jsonOut, out, errOut, errors.New(`add needs the text: agent-deck todo add "call the bank"`))
	}
	store, err := env.store()
	if err != nil {
		return todoFail(*jsonOut, out, errOut, err)
	}
	it, err := store.Add(text, todos.AddOptions{Source: *source, SessionID: *sessionID})
	if err != nil {
		return todoFail(*jsonOut, out, errOut, err)
	}
	res := todos.Result{Status: todos.StatusAdded, Todo: &it, Open: store.OpenCount()}
	env.publish(todos.Change{Action: todos.ActionAdd, Todo: it, Open: derefInt(res.Open), By: it.Source})
	return todoEmit(*jsonOut, out, res, fmt.Sprintf("Added %s: %s", it.ID, it.Text))
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func runTodoList(args []string, out, errOut io.Writer, env todoEnv) int {
	fs, jsonOut := todoFlagSet("list", "list [--all] [--json]", args, out, errOut)
	all := fs.Bool("all", false, "include done items")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return todoParseFailed(err, args, out, errOut)
	}
	if fs.NArg() != 0 {
		return todoFail(*jsonOut, out, errOut, errors.New("list takes no arguments"))
	}
	store, err := env.store()
	if err != nil {
		return todoFail(*jsonOut, out, errOut, err)
	}
	res, err := store.ListResult(*all)
	if err != nil {
		return todoFail(*jsonOut, out, errOut, err)
	}
	if *jsonOut {
		return todoEmit(true, out, res, "")
	}
	items := res.Todos
	if len(items) == 0 {
		fmt.Fprintln(out, "No open to-dos.")
		return 0
	}
	for _, it := range items {
		fmt.Fprintln(out, todoLine(it))
	}
	return 0
}

// todoLine is one item in text output: "  3  call the bank" or, done,
// "  3  [done] call the bank".
func todoLine(it todos.Item) string {
	mark := ""
	if it.Done() {
		mark = "[done] "
	}
	return fmt.Sprintf("%4s  %s%s", it.ID, mark, it.Text)
}

func runTodoChange(action string, args []string, out, errOut io.Writer, env todoEnv) int {
	usage := action + " <id> [--source s] [--json]"
	if action == "done" {
		usage = "done <id | words of the text> [--source s] [--json]"
	}
	fs, jsonOut := todoFlagSet(action, usage, args, out, errOut)
	source := fs.String("source", todos.SourceCLI, "who made the change, reported as \"by\" in the todo.changed event: voice, cli, phone, mac or tui")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return todoParseFailed(err, args, out, errOut)
	}
	if !todos.ValidSource(*source) {
		return todoFail(*jsonOut, out, errOut, todos.ErrBadSource)
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return todoFail(*jsonOut, out, errOut, fmt.Errorf("%s needs an id: agent-deck todo %s", action, usage))
	}
	store, err := env.store()
	if err != nil {
		return todoFail(*jsonOut, out, errOut, err)
	}
	var (
		it      todos.Item
		changed bool
	)
	switch action {
	case "done":
		it, changed, err = store.Done(query)
	case "undo":
		it, changed, err = store.Undo(query)
	case "remove":
		it, err = store.Remove(query)
		changed = err == nil
	}
	var amb *todos.AmbiguousError
	switch {
	case errors.As(err, &amb):
		res := todos.Result{Status: todos.StatusAmbiguous, Query: query, Candidates: amb.Candidates}
		if *jsonOut {
			_ = json.NewEncoder(out).Encode(res)
		} else {
			fmt.Fprintf(out, "%q matches %d to-dos; say which one (by id):\n", query, len(amb.Candidates))
			for _, c := range amb.Candidates {
				fmt.Fprintln(out, todoLine(c))
			}
		}
		return todoExitAmbiguous
	case errors.Is(err, todos.ErrNotFound):
		if *jsonOut {
			_ = json.NewEncoder(out).Encode(todos.Result{Status: todos.StatusNotFound, Query: query})
		} else {
			fmt.Fprintf(errOut, "No to-do matches %q (agent-deck todo list shows them)\n", query)
		}
		return todoExitNotFound
	case err != nil:
		return todoFail(*jsonOut, out, errOut, err)
	}
	res := todos.Result{Todo: &it, Open: store.OpenCount()}
	var text string
	switch {
	case action == "done" && changed:
		res.Status, text = todos.StatusDone, fmt.Sprintf("Done %s: %s", it.ID, it.Text)
	case action == "done":
		res.Status, text = todos.StatusAlreadyDone, fmt.Sprintf("Already done %s: %s", it.ID, it.Text)
	case action == "undo" && changed:
		res.Status, text = todos.StatusReopened, fmt.Sprintf("Reopened %s: %s", it.ID, it.Text)
	case action == "undo":
		res.Status, text = todos.StatusAlreadyOpen, fmt.Sprintf("Already open %s: %s", it.ID, it.Text)
	default:
		res.Status, text = todos.StatusRemoved, fmt.Sprintf("Removed %s: %s", it.ID, it.Text)
	}
	if changed {
		env.publish(todos.Change{Action: action, Todo: it, Open: derefInt(res.Open), By: *source})
	}
	return todoEmit(*jsonOut, out, res, text)
}
