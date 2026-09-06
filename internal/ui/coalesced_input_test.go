package ui

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func TestCoalescedInputOpensSearchAndPreservesText(t *testing.T) {
	for _, burst := range []bool{false, true} {
		name := "sequential"
		if burst {
			name = "coalesced"
		}
		t.Run(name, func(t *testing.T) {
			home := NewHome()
			home.width, home.height = 100, 30
			home.globalSearchIndex = nil
			text := "/Betaé界"
			if burst {
				home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
			} else {
				for _, r := range text {
					home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				}
			}
			if !home.search.IsVisible() {
				t.Fatal("ordered slash plus query did not open search")
			}
			if got := home.search.input.Value(); got != "Betaé界" {
				t.Fatalf("query = %q, want exact Unicode query", got)
			}
		})
	}
}

func TestCoalescedInputPreservesModalTextAndFlags(t *testing.T) {
	for _, mode := range []string{"search", "dialog", "alt", "paste"} {
		t.Run(mode, func(t *testing.T) {
			h := NewHome()
			h.width, h.height = 100, 30
			key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Betaé界")}
			switch mode {
			case "search":
				h.search.Show()
			case "dialog":
				h.groupDialog.ShowCreateSubgroup("", "")
			case "alt":
				key.Alt = true
				key.Runes = []rune("/Beta")
			case "paste":
				key.Paste = true
				key.Runes = []rune("/Beta")
			}
			h.Update(key)
			switch mode {
			case "search":
				if h.search.input.Value() != "Betaé界" {
					t.Fatal("search text changed")
				}
			case "dialog":
				if h.groupDialog.nameInput.Value() != "Betaé界" {
					t.Fatal("dialog text changed")
				}
			default:
				if h.search.IsVisible() {
					t.Fatal("Alt or bracketed paste incorrectly became shortcut sequence")
				}
			}
		})
	}
}

func TestCoalescedInputQuitStopsTailAndRetainsCommand(t *testing.T) {
	h := NewHome()
	_, cmd := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q/Beta")})
	if !h.isQuitting || h.search.IsVisible() {
		t.Fatal("quit must stop remaining shortcut/query")
	}
	if cmd == nil {
		t.Fatal("quit command lost")
	}
	if _, ok := cmd().(quitMsg); !ok {
		t.Fatalf("quit command changed: %T", cmd())
	}
}

func TestCoalescedInputConfirmationContinues(t *testing.T) {
	h := NewHome()
	h.confirmDialog.ShowQuitWithPool(1)
	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("lh")})
	if h.isQuitting || !h.confirmDialog.IsVisible() || h.confirmDialog.GetFocusedButton() != 0 {
		t.Fatal("confirmation navigation lost or reordered")
	}
}

func TestCoalescedInputNavigationRetainsFocusAndRepaintCommands(t *testing.T) {
	items := scrollTestItems()
	for i := range items {
		items[i].Session = session.NewInstance(fmt.Sprintf("focus%d", i), t.TempDir())
	}
	h := newTestHomeWithItems(100, 30, items)
	h.fullRepaint = true
	_, cmd := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jjk")})
	if h.cursor != 1 {
		t.Fatalf("ordered navigation cursor=%d, want1", h.cursor)
	}
	if cmd == nil {
		t.Fatal("repaint commands lost")
	}
	// Bubble Tea's Sequence message is private. Inspect its command collection
	// only in this test, then execute owned redraw/preview commands as its loop does.
	v := reflect.ValueOf(cmd())
	if v.Kind() != reflect.Slice || v.Len() != 3 {
		t.Fatalf("each key's command must be retained, got %v", v)
	}
	for i := 0; i < v.Len(); i++ {
		c, ok := v.Index(i).Interface().(tea.Cmd)
		if !ok || !containsClearScreen(c) {
			t.Fatalf("key%d lost repaint (%s)", i, fmt.Sprint(v.Index(i)))
		}
	}
	h.focusMu.Lock()
	name := h.focusedSessionName
	h.focusMu.Unlock()
	if want := items[1].Session.GetTmuxSession().Name; name != want {
		t.Fatalf("focused session = %q, want %q", name, want)
	}
}

// A remote row deliberately has no local Instance. Both search and a subsequent
// creation dialog must retain that routing context while processing a burst.
func TestCoalescedInputRemoteRowPreservesSearchAndCreationTarget(t *testing.T) {
	for _, burst := range []bool{false, true} {
		name := "sequential"
		if burst {
			name = "coalesced"
		}
		t.Run(name, func(t *testing.T) {
			setXDGTestHome(t)
			h := NewHome()
			h.width, h.height = 100, 30
			h.globalSearchIndex = nil
			remote := session.RemoteSessionInfo{ID: "remote-proof", Title: "Remoteé界", RemoteName: "shared", Tool: "shell"}
			h.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "shared"}}
			h.cursor = 0
			input := func(text string) {
				if burst {
					h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
				} else {
					for _, r := range text {
						h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
					}
				}
			}
			input("/Remoteé界")
			if !h.search.IsVisible() || h.search.input.Value() != "Remoteé界" {
				t.Fatal("remote-row search lost shortcut or Unicode query")
			}
			h.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if h.search.IsVisible() || len(h.flatItems) != 1 || h.flatItems[h.cursor].RemoteSession != &remote {
				t.Fatal("search escape lost remote selection")
			}
			input("nDrafté界")
			if !h.newDialog.IsVisible() || h.pendingRemoteName != "shared" {
				t.Fatalf("creation lost remote routing: visible=%v remote=%q", h.newDialog.IsVisible(), h.pendingRemoteName)
			}
			if got := h.newDialog.nameInput.Value(); got != "Drafté界" {
				t.Fatalf("remote dialog title=%q", got)
			}
			if len(h.instances) != 0 || remote.Title != "Remoteé界" {
				t.Fatal("remote input mutated a local instance or existing remote title")
			}
		})
	}
}
