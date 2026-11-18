package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/todos"
)

type todosPanelHarness struct {
	panel *TodosPanel
	store *todos.Store
	pubs  []todos.Change
	profs []string
}

func newTodosPanelHarness(t *testing.T) *todosPanelHarness {
	t.Helper()
	h := &todosPanelHarness{store: todos.NewStore(t.TempDir())}
	h.panel = NewTodosPanel("team-alex")
	h.wire(h.panel)
	h.panel.SetSize(100, 30)
	return h
}

func (h *todosPanelHarness) wire(p *TodosPanel) {
	p.open = func() (*todos.Store, error) { return h.store, nil }
	p.publish = func(c todos.Change, profile string) {
		h.pubs = append(h.pubs, c)
		h.profs = append(h.profs, profile)
	}
}

func todoKey(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "alt+o":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}, Alt: true}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press sends keys and runs the commands of the keys that change the list
// (x and enter return the publish), so publishes run synchronously here.
// Other commands (the input's cursor blink) are not run: they sleep.
func (h *todosPanelHarness) press(keys ...string) {
	for _, k := range keys {
		var cmd tea.Cmd
		adding := h.panel.adding
		h.panel, cmd = h.panel.Update(todoKey(k))
		if k == "enter" || (k == "x" && !adding) {
			for _, msg := range runTodoCmd(cmd) {
				if res, ok := msg.(todosPanelResultMsg); ok {
					h.panel.HandleResult(res)
				}
			}
		}
	}
}

func runTodoCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, runTodoCmd(c)...)
		}
		return out
	case nil:
		return nil
	default:
		return []tea.Msg{msg}
	}
}

func TestTodosPanelTable(t *testing.T) {
	cases := []struct {
		name     string
		seed     []string
		keys     []string
		wantOpen []string // open item texts after the keys
		wantDone []string
		wantPubs []string
		wantView []string
	}{
		{name: "shows open items", seed: []string{"call the bank", "buy milk"},
			wantOpen: []string{"call the bank", "buy milk"}, wantView: []string{"To-dos", "2 open", "[ ]   1  call the bank", "buy milk"}},
		{name: "empty list is calm", wantView: []string{"Nothing on the list."}},
		{name: "x ticks off the selected item", seed: []string{"call the bank", "buy milk"}, keys: []string{"down", "x"},
			wantOpen: []string{"call the bank"}, wantDone: []string{"buy milk"}, wantPubs: []string{"done"}, wantView: []string{"Done 2"}},
		{name: "a adds with source tui", keys: []string{"a", "p", "a", "y", " ", "r", "e", "n", "t", "enter"},
			wantOpen: []string{"pay rent"}, wantPubs: []string{"add"}, wantView: []string{"Added 1", "pay rent"}},
		{name: "esc cancels an add", keys: []string{"a", "x", "esc"}, wantView: []string{"Nothing on the list."}},
		{name: "tab shows done items and x reopens", seed: []string{"call the bank"}, keys: []string{"x", "tab", "x"},
			wantOpen: []string{"call the bank"}, wantPubs: []string{"done", "undo"}, wantView: []string{"Reopened 1"}},
		{name: "done item shown with tab", seed: []string{"call the bank"}, keys: []string{"x", "tab"},
			wantDone: []string{"call the bank"}, wantPubs: []string{"done"}, wantView: []string{"[x]   1  call the bank", "0 open"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTodosPanelHarness(t)
			for _, s := range tc.seed {
				if _, err := h.store.Add(s, todos.AddOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if cmd := h.panel.Show(); cmd == nil {
				t.Fatal("Show returned no refresh tick")
			}
			h.press(tc.keys...)
			all, _ := h.store.List(true)
			var open, done []string
			for _, it := range all {
				if it.Done() {
					done = append(done, it.Text)
				} else {
					open = append(open, it.Text)
				}
			}
			if strings.Join(open, "|") != strings.Join(tc.wantOpen, "|") || strings.Join(done, "|") != strings.Join(tc.wantDone, "|") {
				t.Fatalf("open %q done %q, want %q %q", open, done, tc.wantOpen, tc.wantDone)
			}
			var pubs []string
			for i, c := range h.pubs {
				pubs = append(pubs, c.Action)
				if c.By != todos.SourceTUI || h.profs[i] != "team-alex" {
					t.Fatalf("publish %+v to %q, want by tui to team-alex", c, h.profs[i])
				}
			}
			if strings.Join(pubs, "|") != strings.Join(tc.wantPubs, "|") {
				t.Fatalf("published %v, want %v", pubs, tc.wantPubs)
			}
			for _, it := range all {
				if it.Text == "pay rent" && it.Source != todos.SourceTUI {
					t.Fatalf("added item source %q, want tui", it.Source)
				}
			}
			view := h.panel.View()
			for _, w := range tc.wantView {
				if !strings.Contains(view, w) {
					t.Fatalf("view lacks %q:\n%s", w, view)
				}
			}
		})
	}
}

func TestTodosPanelTickPicksUpOutsideChanges(t *testing.T) {
	h := newTodosPanelHarness(t)
	h.panel.Show()
	if _, err := h.store.Add("added by voice", todos.AddOptions{Source: todos.SourceVoice}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.panel.View(), "added by voice") {
		t.Fatal("panel showed the item before a tick")
	}
	if cmd := h.panel.HandleTick(todosPanelTickMsg{gen: h.panel.tickGen}); cmd == nil {
		t.Fatal("tick did not schedule the next one while open")
	}
	if !strings.Contains(h.panel.View(), "added by voice") {
		t.Fatalf("tick did not refresh:\n%s", h.panel.View())
	}
	// A stale tick from an earlier Show, or any tick once closed, stops.
	if cmd := h.panel.HandleTick(todosPanelTickMsg{gen: h.panel.tickGen - 1}); cmd != nil {
		t.Fatal("stale tick rescheduled")
	}
	h.press("esc")
	if h.panel.IsVisible() || h.panel.HandleTick(todosPanelTickMsg{gen: h.panel.tickGen}) != nil {
		t.Fatal("closed panel still ticks")
	}
}

func TestHomeAltOOpensTodosPanel(t *testing.T) {
	home := newTestHomeWithItems(120, 40, nil)
	h := &todosPanelHarness{store: todos.NewStore(t.TempDir())}
	h.wire(home.todosPanel)
	if _, err := h.store.Add("call the bank", todos.AddOptions{}); err != nil {
		t.Fatal(err)
	}
	model, _ := home.Update(todoKey("alt+o"))
	home = model.(*Home)
	if !home.todosPanel.IsVisible() || !home.hasModalVisible() {
		t.Fatal("alt+o did not open the to-dos panel")
	}
	if !strings.Contains(home.View(), "call the bank") {
		t.Fatalf("home view does not show the panel:\n%s", home.View())
	}
	model, _ = home.Update(todoKey("esc"))
	home = model.(*Home)
	if home.todosPanel.IsVisible() {
		t.Fatal("esc did not close the panel")
	}
}

// The store write (which may wait up to 10 s for the file lock) runs in a
// tea.Cmd, never inside Update: Update returns at once and the list only
// changes when the command runs.
func TestTodosPanelWritesRunOffTheUpdateLoop(t *testing.T) {
	h := newTodosPanelHarness(t)
	if _, err := h.store.Add("call the bank", todos.AddOptions{}); err != nil {
		t.Fatal(err)
	}
	h.panel.Show()
	var cmd tea.Cmd
	h.panel, cmd = h.panel.Update(todoKey("x"))
	if open, _, _ := h.store.Counts(); open != 1 {
		t.Fatal("x wrote the store inside Update")
	}
	if cmd == nil {
		t.Fatal("x returned no command")
	}
	for _, msg := range runTodoCmd(cmd) {
		if res, ok := msg.(todosPanelResultMsg); ok {
			h.panel.HandleResult(res)
		}
	}
	if open, _, _ := h.store.Counts(); open != 0 || len(h.pubs) != 1 {
		t.Fatalf("after the command: open=%d pubs=%v", open, h.pubs)
	}
	if !strings.Contains(h.panel.View(), "Done 1") {
		t.Fatalf("result not shown:\n%s", h.panel.View())
	}
}

// S4: a long list scrolls: the title and the selected row stay on screen
// and the view never grows past the terminal height.
func TestTodosPanelLongListScrolls(t *testing.T) {
	h := newTodosPanelHarness(t)
	h.panel.SetSize(120, 30)
	for i := 1; i <= 72; i++ {
		if _, err := h.store.Add(fmt.Sprintf("item number %d", i), todos.AddOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	h.panel.Show()
	for i := 0; i < 46; i++ {
		h.press("down")
	}
	view := h.panel.View()
	if n := strings.Count(view, "\n") + 1; n > 30 {
		t.Fatalf("view is %d lines on a 30-line terminal", n)
	}
	for _, w := range []string{"To-dos", "> [ ]  47  item number 47", "more above"} {
		if !strings.Contains(view, w) {
			t.Fatalf("view lacks %q:\n%s", w, view)
		}
	}
	h.press("x")
	if it, _ := h.store.List(true); !it[len(it)-1].Done() || it[len(it)-1].ID != "47" {
		t.Fatalf("x did not tick off the visible selected item 47: %+v", it[len(it)-1])
	}
	// Back at the top: the first row shows, the bottom says more below.
	for i := 0; i < 80; i++ {
		h.press("k")
	}
	view = h.panel.View()
	if !strings.Contains(view, "> [ ]   1  item number 1") || !strings.Contains(view, "more below") {
		t.Fatalf("top of the list:\n%s", view)
	}
}

// N1: the key the panel was opened with (a rebound [hotkeys] todos) also
// closes it.
func TestTodosPanelClosesWithItsOwnKey(t *testing.T) {
	h := newTodosPanelHarness(t)
	h.panel.SetCloseKey("ctrl+t")
	h.panel.Show()
	h.panel, _ = h.panel.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if h.panel.IsVisible() {
		t.Fatal("the rebound key did not close the panel")
	}
}
