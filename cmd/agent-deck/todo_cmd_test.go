package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/todos"
)

type todoHarness struct {
	store     *todos.Store
	published []todos.Change
}

func newTodoHarness(t *testing.T) *todoHarness {
	t.Helper()
	return &todoHarness{store: todos.NewStore(t.TempDir())}
}

func (h *todoHarness) run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	env := todoEnv{
		store:   func() (*todos.Store, error) { return h.store, nil },
		publish: func(c todos.Change) { h.published = append(h.published, c) },
	}
	code = runTodo(args, &out, &errOut, env)
	return code, out.String(), errOut.String()
}

func decodeJSONObject(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, s)
	}
	if dec.More() {
		t.Fatalf("stdout has more than one JSON object:\n%s", s)
	}
	return m
}

func TestTodoCLITable(t *testing.T) {
	type step struct {
		args       []string
		wantCode   int
		wantStatus string // --json "status"; "" = not checked
		wantOut    string // substring of stdout
		wantPub    string // action published by this step; "" = none
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{name: "add then list", steps: []step{
			{args: []string{"add", "call", "the", "bank", "--json"}, wantStatus: "added", wantOut: `"text":"call the bank"`, wantPub: "add"},
			{args: []string{"add", "--source", "voice", "--session", "s-1", "buy milk", "--json"}, wantStatus: "added", wantOut: `"session_id":"s-1"`, wantPub: "add"},
			{args: []string{"list", "--json"}, wantOut: `"open":2`},
			{args: []string{"list"}, wantOut: "   2  buy milk"},
		}},
		{name: "done by words and by id", steps: []step{
			{args: []string{"add", "call the bank"}, wantOut: "Added 1: call the bank", wantPub: "add"},
			{args: []string{"add", "buy milk"}, wantPub: "add"},
			{args: []string{"done", "bank", "--json"}, wantStatus: "done", wantOut: `"open":1`, wantPub: "done"},
			{args: []string{"done", "1", "--json"}, wantStatus: "already_done"},
			{args: []string{"list", "--all", "--json"}, wantOut: `"done":1`},
			{args: []string{"done", "2"}, wantOut: "Done 2: buy milk", wantPub: "done"},
			{args: []string{"list"}, wantOut: "No open to-dos."},
		}},
		{name: "two close matches exit 3 and change nothing", steps: []step{
			{args: []string{"add", "email sam about the release"}, wantPub: "add"},
			{args: []string{"add", "email sam about the invoice"}, wantPub: "add"},
			{args: []string{"done", "email", "sam", "--json"}, wantCode: 3, wantStatus: "ambiguous", wantOut: `"candidates":[`},
			{args: []string{"done", "email sam"}, wantCode: 3, wantOut: "matches 2 to-dos"},
			{args: []string{"list", "--json"}, wantOut: `"open":2`},
		}},
		{name: "not found exits 2", steps: []step{
			{args: []string{"done", "dentist", "--json"}, wantCode: 2, wantStatus: "not_found", wantOut: `"query":"dentist"`},
			{args: []string{"undo", "9", "--json"}, wantCode: 2, wantStatus: "not_found"},
			{args: []string{"remove", "9"}, wantCode: 2},
		}},
		{name: "undo and remove", steps: []step{
			{args: []string{"add", "x"}, wantPub: "add"},
			{args: []string{"done", "1"}, wantPub: "done"},
			{args: []string{"undo", "1", "--json"}, wantStatus: "reopened", wantPub: "undo"},
			{args: []string{"undo", "1", "--json"}, wantStatus: "already_open"},
			{args: []string{"remove", "1", "--json"}, wantStatus: "removed", wantOut: `"open":0`, wantPub: "remove"},
		}},
		{name: "bad input", steps: []step{
			{args: []string{"add", "--json"}, wantCode: 1, wantStatus: "error"},
			{args: []string{"add", "x", "--source", "email", "--json"}, wantCode: 1, wantStatus: "error", wantOut: "source must be one of"},
			{args: []string{"undo", "bank", "--json"}, wantCode: 1, wantStatus: "error"},
			{args: []string{"list", "extra"}, wantCode: 1},
			{args: []string{"frobnicate"}, wantCode: 1},
			{args: []string{}, wantCode: 1},
		}},
		{name: "help", steps: []step{
			{args: []string{"--help"}, wantOut: "Usage: agent-deck todo"},
			{args: []string{"done", "--help"}, wantOut: "Usage: agent-deck todo done"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTodoHarness(t)
			for i, st := range tc.steps {
				before := len(h.published)
				code, out, errOut := h.run(st.args...)
				if code != st.wantCode {
					t.Fatalf("step %d %v: exit %d, want %d\nstdout: %s\nstderr: %s", i, st.args, code, st.wantCode, out, errOut)
				}
				if st.wantStatus != "" {
					if got := decodeJSONObject(t, out)["status"]; got != st.wantStatus {
						t.Fatalf("step %d %v: status %v, want %s\n%s", i, st.args, got, st.wantStatus, out)
					}
				}
				if st.wantOut != "" && !strings.Contains(out, st.wantOut) {
					t.Fatalf("step %d %v: stdout lacks %q:\n%s", i, st.args, st.wantOut, out)
				}
				pubs := h.published[before:]
				switch {
				case st.wantPub == "" && len(pubs) != 0:
					t.Fatalf("step %d %v published %+v, want nothing", i, st.args, pubs)
				case st.wantPub != "" && (len(pubs) != 1 || pubs[0].Action != st.wantPub):
					t.Fatalf("step %d %v published %+v, want one %q", i, st.args, pubs, st.wantPub)
				}
			}
		})
	}
}

func TestTodoListJSONShapeIsStable(t *testing.T) {
	h := newTodoHarness(t)
	h.run("add", "call the bank", "--source", "phone")
	_, out, _ := h.run("list", "--json")
	m := decodeJSONObject(t, out)
	for _, k := range []string{"todos", "open", "done"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("list json lacks %q: %s", k, out)
		}
	}
	item := m["todos"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "text", "created_at", "done_at", "source"} {
		if _, ok := item[k]; !ok {
			t.Fatalf("item json lacks %q: %s", k, out)
		}
	}
	if item["done_at"] != nil || item["source"] != "phone" || item["id"] != "1" {
		t.Fatalf("item json %v", item)
	}
	// Empty list is [] not null.
	h2 := newTodoHarness(t)
	_, out, _ = h2.run("list", "--json")
	if !strings.Contains(out, `"todos":[]`) {
		t.Fatalf("empty list json %s", out)
	}
}

func TestTodoCLIFlagErrorsStillPrintJSON(t *testing.T) {
	for _, args := range [][]string{
		{"done", "--json", "--bogus", "bank"},
		{"add", "x", "--json", "--nope"},
		{"list", "--json", "--all=maybe"},
	} {
		h := newTodoHarness(t)
		code, out, _ := h.run(args...)
		if code != 1 {
			t.Fatalf("%v: exit %d, want 1", args, code)
		}
		if got := decodeJSONObject(t, out)["status"]; got != "error" {
			t.Fatalf("%v: status %v, want error: %s", args, got, out)
		}
	}
}

func TestTodoCLIChangeSourceIsTheEventBy(t *testing.T) {
	h := newTodoHarness(t)
	h.run("add", "call the bank")
	h.run("add", "buy milk")
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"done", "bank", "--source", "voice"}, "voice"},
		{[]string{"undo", "1", "--source", "mac"}, "mac"},
		{[]string{"remove", "2"}, "cli"},
	}
	for _, st := range steps {
		before := len(h.published)
		if code, out, errOut := h.run(st.args...); code != 0 {
			t.Fatalf("%v: exit %d %s %s", st.args, code, out, errOut)
		}
		if len(h.published) != before+1 || h.published[before].By != st.want {
			t.Fatalf("%v: published %+v, want by %q", st.args, h.published[before:], st.want)
		}
	}
	if code, _, _ := h.run("done", "1", "--source", "fax"); code != 1 {
		t.Fatalf("bad --source on done: exit %d, want 1", code)
	}
}
