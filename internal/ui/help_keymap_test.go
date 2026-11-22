package ui

import (
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The help overlay is the only place the deck lists its keys, so a key that
// works but has no row there cannot be found. These tests hold the overlay to
// the keys the main screen really handles, in two halves:
//
//   - rebindable actions come from the hotkey table (hotkeyActionOrder with
//     the resolved default bindings);
//   - fixed keys are found by pressing every candidate key on a real Home and
//     seeing whether anything happens.
//
// Neither half reads source text: a new `case` in handleMainKey that does
// something on the fixture rows fails here until it gets a help row.

// helpKeyCells returns the key column of every row of the full key list.
func helpKeyCells(o *HelpOverlay) []string {
	var cells []string
	for _, section := range o.allKeySections() {
		for _, item := range section.items {
			cells = append(cells, item[0])
		}
	}
	return cells
}

// helpCellShowsKey reports whether a key cell is the key, or a "a/b" pair
// (keyPair) with the key as one half.
func helpCellShowsKey(cell, key string) bool {
	cell, key = strings.ToLower(cell), strings.ToLower(key)
	return cell == key || strings.HasPrefix(cell, key+"/") || strings.HasSuffix(cell, "/"+key)
}

func fullHelpOverlay(bindings map[string]string) *HelpOverlay {
	o := NewHelpOverlay()
	o.SetHotkeys(bindings)
	o.SetHasAgents(true)
	o.SetSize(100, 400)
	o.ShowAll()
	return o
}

func TestHelpOverlayListsEveryReboundableAction(t *testing.T) {
	bindings := resolveHotkeys(nil)
	cells := helpKeyCells(fullHelpOverlay(bindings))
	for _, action := range hotkeyActionOrder {
		key := strings.TrimSpace(bindings[action])
		if key == "" {
			continue // ships unbound (switch_session)
		}
		t.Run(action, func(t *testing.T) {
			if action == hotkeyDetach {
				key = DetachByteLabel(DetachByteFromBinding(key))
			}
			for _, cell := range cells {
				if helpCellShowsKey(cell, key) {
					return
				}
			}
			t.Fatalf("action %q is bound to %q but the help overlay (Tab: all keys) has no row for that key", action, key)
		})
	}
}

// fixedKeyHelpCell maps each fixed (not rebindable) main-screen key to the key
// cell of the help row that documents it. A key that the probe below finds
// live must be listed here, and the cell must exist in the overlay.
var fixedKeyHelpCell = map[string]string{
	"j": "j / Down", "down": "j / Down",
	"k": "k / Up", "up": "k / Up",
	"ctrl+p": "Ctrl+p/n", "ctrl+n": "Ctrl+p/n",
	"ctrl+u": "Ctrl+u/d", "ctrl+d": "Ctrl+u/d",
	"pgup": "PgUp / PgDn", "pgdown": "PgUp / PgDn",
	"ctrl+f": "Ctrl+f/b", "ctrl+b": "Ctrl+f/b",
	"home": "Home / End", "end": "Home / End",
	"G": "G",
	"h": "h / Left", "left": "h / Left",
	"l": "l / Right", "right": "l / Right",
	"tab": "Tab",
	"1":   "1-9", "2": "1-9", "3": "1-9", "4": "1-9", "5": "1-9", "6": "1-9", "7": "1-9", "8": "1-9", "9": "1-9",
	" ":     "Space",
	"enter": "Enter",
	"alt+j": "Alt+j / Alt+k", "alt+k": "Alt+j / Alt+k",
	"alt+1": "Alt+1 - Alt+9", "alt+2": "Alt+1 - Alt+9", "alt+3": "Alt+1 - Alt+9",
	"alt+4": "Alt+1 - Alt+9", "alt+5": "Alt+1 - Alt+9", "alt+6": "Alt+1 - Alt+9",
	"alt+7": "Alt+1 - Alt+9", "alt+8": "Alt+1 - Alt+9", "alt+9": "Alt+1 - Alt+9",
	"alt+g": "Alt+g / Alt+G", "alt+G": "Alt+g / Alt+G",
	"alt+/": "Alt+/",
	"+":     "+ / K / Shift+↑", "K": "+ / K / Shift+↑", "shift+up": "+ / K / Shift+↑", "ctrl+up": "+ / K / Shift+↑",
	"-": "- / J / Shift+↓", "J": "- / J / Shift+↓", "shift+down": "- / J / Shift+↓", "ctrl+down": "- / J / Shift+↓",
	"shift+left": "Shift+→/←", "shift+right": "Shift+→/←",
	",": ",",
	"O": "O",
	"<": "< / >", ">": "< / >",
	"Y":      "Y",
	"X":      "X",
	"ctrl+x": "Ctrl+X",
	"I":      "I",
	"z":      "z",
	"ctrl+g": "Ctrl+G",
	"ctrl+e": "Ctrl+E",
	"esc":    "Esc Esc",
	"0":      "0",
	"!":      "!",
	"@":      "@",
	"#":      "#",
	"$":      CostDashboardKey,
	"&":      FilterKeyError,
	"%":      FilterKeyActive,
}

// keyProbeCandidates is every key a terminal can plausibly send to the main
// screen: printable ASCII, Ctrl and Alt chords, and the named keys.
func keyProbeCandidates() map[string]tea.KeyMsg {
	out := map[string]tea.KeyMsg{}
	for r := rune(' '); r <= '~'; r++ {
		out[string(r)] = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r != ' ' {
			out["alt+"+string(r)] = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
		}
	}
	out[" "] = tea.KeyMsg{Type: tea.KeySpace}
	for _, named := range []tea.KeyType{
		tea.KeyCtrlA, tea.KeyCtrlB, tea.KeyCtrlD, tea.KeyCtrlE, tea.KeyCtrlF, tea.KeyCtrlG,
		tea.KeyCtrlH, tea.KeyCtrlJ, tea.KeyCtrlK, tea.KeyCtrlL, tea.KeyCtrlN, tea.KeyCtrlO,
		tea.KeyCtrlP, tea.KeyCtrlR, tea.KeyCtrlS, tea.KeyCtrlT, tea.KeyCtrlU, tea.KeyCtrlV,
		tea.KeyCtrlW, tea.KeyCtrlX, tea.KeyCtrlY, tea.KeyCtrlZ,
		tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight, tea.KeyHome, tea.KeyEnd,
		tea.KeyPgUp, tea.KeyPgDown, tea.KeyTab, tea.KeyShiftTab, tea.KeyEnter, tea.KeyEsc,
		tea.KeyBackspace, tea.KeyDelete, tea.KeyInsert,
		tea.KeyShiftUp, tea.KeyShiftDown, tea.KeyShiftLeft, tea.KeyShiftRight,
		tea.KeyCtrlUp, tea.KeyCtrlDown, tea.KeyCtrlLeft, tea.KeyCtrlRight,
		tea.KeyF1, tea.KeyF2, tea.KeyF5, tea.KeyF10,
	} {
		msg := tea.KeyMsg{Type: named}
		out[msg.String()] = msg
	}
	for _, named := range []tea.KeyType{tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight, tea.KeyEnter} {
		msg := tea.KeyMsg{Type: named, Alt: true}
		out[msg.String()] = msg
	}
	return out
}

// keyProbeHome is a deck with one group header and sessions in the states the
// status-gated keys look at. where picks the row under the cursor.
func keyProbeHome(t *testing.T, where string) *Home {
	t.Helper()
	home, idx := buildTwoGroupHome(t)
	home.instancesMu.Lock()
	home.instances[0].Status = session.StatusIdle
	home.instances[1].Status = session.StatusError
	home.instances[2].Status = session.StatusStopped
	home.instances[3].Status = session.StatusWaiting
	home.instances[3].Tool = "gemini"
	home.instances[4].Status = session.StatusRunning
	home.instancesMu.Unlock()
	home.rebuildFlatItems()
	switch where {
	case "group":
		home.cursor = 0
	default:
		home.cursor = idx[where]
	}
	return home
}

// keyProbeDoesSomething presses one key on a fresh deck and reports whether
// anything a user could notice changed: the screen, a command to run, quit,
// or the double-Esc timer.
func keyProbeDoesSomething(t *testing.T, where string, msg tea.KeyMsg) (did bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("key %q on %s panicked: %v", msg.String(), where, r)
			did = true
		}
	}()
	h := keyProbeHome(t, where)
	before := stripAnsi(h.View())
	model, cmd := h.handleMainKey(msg)
	after, ok := model.(*Home)
	if !ok {
		return true
	}
	if cmd != nil || after.isQuitting || !after.lastEscTime.IsZero() {
		return true
	}
	return stripAnsi(after.View()) != before
}

func TestHelpOverlayListsEveryFixedKeyThatDoesSomething(t *testing.T) {
	// Several keys save view state (filter, view mode, pane size); keep that
	// out of the package's shared test HOME so later tests start clean.
	setXDGTestHome(t)
	bindings := resolveHotkeys(nil)
	lookup, _ := buildHotkeyLookup(bindings)
	cellSet := map[string]bool{}
	for _, cell := range helpKeyCells(fullHelpOverlay(bindings)) {
		cellSet[cell] = true
	}

	candidates := keyProbeCandidates()
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)

	rows := []string{"a1", "a2", "a3", "b1", "group"}
	live := 0
	for _, name := range names {
		if _, rebindable := lookup[name]; rebindable {
			continue // held by TestHelpOverlayListsEveryReboundableAction
		}
		// Enter attaches (a real tmux exec) and the layout twins of shifted
		// digits repeat the symbol rows; both are documented under their
		// canonical key.
		if name == "enter" {
			live++
			if !cellSet[fixedKeyHelpCell[name]] {
				t.Errorf("Enter has no help row %q", fixedKeyHelpCell[name])
			}
			continue
		}
		var where string
		for _, row := range rows {
			if keyProbeDoesSomething(t, row, candidates[name]) {
				where = row
				break
			}
		}
		if where == "" {
			continue
		}
		live++
		cell, listed := fixedKeyHelpCell[name]
		if !listed {
			t.Errorf("key %q does something on row %s but is not in fixedKeyHelpCell: give it a help row and list it here", name, where)
			continue
		}
		if !cellSet[cell] {
			t.Errorf("key %q does something on row %s but the help overlay (Tab: all keys) has no %q row", name, where, cell)
		}
	}
	// The probe must really be finding keys; a broken fixture would pass vacuously.
	if live < 40 {
		t.Fatalf("probe found only %d live fixed keys; the fixture is not exercising the key map", live)
	}
}

// Every documented status word works in the search box with and without the
// leading slash the help overlay and tui-reference.md show.
func TestSearchStatusWordsWorkWithLeadingSlash(t *testing.T) {
	setXDGTestHome(t)
	home := keyProbeHome(t, "a1")
	want := map[string]string{"waiting": "b1", "running": "b2", "idle": "a1", "error": "a2", "stopped": "a3"}
	for word, title := range want {
		for _, query := range []string{word, "/" + word} {
			t.Run(query, func(t *testing.T) {
				h, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
				hm := h.(*Home)
				if !hm.search.IsVisible() {
					t.Fatal("/ did not open search")
				}
				for _, r := range query {
					m, _ := hm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
					hm = m.(*Home)
				}
				got := hm.search.results
				if len(got) != 1 || got[0].Title != title {
					titles := []string{}
					for _, inst := range got {
						titles = append(titles, inst.Title)
					}
					t.Fatalf("search %q: want only %s (the %s session), got %v", query, title, word, titles)
				}
				hm.search.Hide()
			})
		}
	}
}
