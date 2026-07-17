package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestIssue1595CopyPaneHotkey(t *testing.T) {
	bindings := resolveHotkeys(nil)
	if got := bindings[hotkeyCopyPane]; got != "V" {
		t.Fatalf("default copy_pane binding = %q, want V", got)
	}
	if got := bindings[hotkeyCopyOutput]; got != "c" {
		t.Fatalf("copy_output binding changed to %q", got)
	}
	if got := bindings[hotkeyToggleYolo]; got != "y" {
		t.Fatalf("toggle_yolo binding changed to %q", got)
	}

	overridden := resolveHotkeys(map[string]string{"copy_pane": "ctrl+p"})
	if got := overridden[hotkeyCopyPane]; got != "ctrl+p" {
		t.Fatalf("overridden copy_pane binding = %q, want ctrl+p", got)
	}

	unbound := resolveHotkeys(map[string]string{"copy_pane": ""})
	if _, ok := unbound[hotkeyCopyPane]; ok {
		t.Fatal("copy_pane should be absent when explicitly unbound")
	}
}

func TestIssue1595CaptureVisiblePaneDedicatedSocket(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("agentdeck-copy-pane-%d-%d", os.Getpid(), time.Now().UnixNano())
	const name = "copy-pane"
	start := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", name,
		"printf '\\033[31mhttps://example.test/path\\033[0m\\n'; sleep 10")
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start dedicated tmux session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-session", "-t", name).Run()
	})

	sess := &tmux.Session{Name: name, SocketName: socket}
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err := sess.CapturePaneFresh()
		if err == nil && strings.Contains(normalizeVisiblePane(raw), "https://example.test/path") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("URL was not captured before deadline: raw=%q err=%v", raw, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestIssue1595CopyPaneHelpAndFooter(t *testing.T) {
	overlay := NewHelpOverlay()
	overlay.SetSize(120, 80)
	overlay.Show()
	help := tmux.StripANSI(overlay.View())
	if !strings.Contains(help, "V") || !strings.Contains(help, "Copy visible terminal text, including links") {
		t.Fatalf("help does not advertise visible-pane copy:\n%s", help)
	}

	home := NewHome()
	t.Cleanup(func() {
		home.cancel()
		if home.storage != nil {
			_ = home.storage.Close()
		}
	})
	home.width = 120
	home.height = 40
	home.flatItems = []session.Item{{
		Type:    session.ItemTypeSession,
		Session: &session.Instance{ID: "copy-pane", Title: "Copy Me"},
	}}
	footer := tmux.StripANSI(home.renderHelpBarFull())
	if !strings.Contains(footer, "V") || !strings.Contains(footer, "Copy pane") {
		t.Fatalf("footer does not advertise visible-pane copy:\n%s", footer)
	}
}

func TestIssue1595CopyPaneDispatchesConfiguredAction(t *testing.T) {
	home := NewHome()
	t.Cleanup(func() {
		home.cancel()
		if home.storage != nil {
			_ = home.storage.Close()
		}
	})
	inst := &session.Instance{ID: "copy-pane", Title: "Copy Me"}
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0

	captured := false
	copied := ""
	home.paneCapture = func(got *session.Instance) (string, error) {
		captured = true
		if got != inst {
			t.Fatalf("capture instance = %p, want %p", got, inst)
		}
		return "\x1b[31mhttps://example.test\x1b[0m  \nsecond\n", nil
	}
	home.paneClipboard = func(text string, _ bool) (*clipboard.CopyResult, error) {
		copied = text
		return &clipboard.CopyResult{LineCount: 2}, nil
	}

	_, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	if cmd == nil {
		t.Fatal("V did not dispatch copy_pane")
	}
	msg := cmd()
	if !captured || copied != "https://example.test\nsecond" {
		t.Fatalf("captured=%v copied=%q", captured, copied)
	}
	model, _ := home.Update(msg)
	gotHome := model.(*Home)
	if got := gotHome.err.Error(); got != "Copied visible terminal text to clipboard (2 lines, Copy Me)" {
		t.Fatalf("success status = %q", got)
	}

	home.setHotkeys(resolveHotkeys(map[string]string{"copy_pane": "!"}))
	_, cmd = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}})
	if cmd == nil {
		t.Fatal("configured copy_pane key did not dispatch")
	}

	home.setHotkeys(resolveHotkeys(map[string]string{"copy_pane": ""}))
	_, cmd = home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	if cmd != nil {
		t.Fatal("unbound copy_pane still dispatched")
	}
}

func TestIssue1595CopyPaneResultMessages(t *testing.T) {
	inst := &session.Instance{ID: "copy-pane", Title: "Copy Me"}
	tests := []struct {
		name      string
		capture   func(*session.Instance) (string, error)
		clipboard func(string, bool) (*clipboard.CopyResult, error)
		want      string
	}{
		{
			name:    "empty",
			capture: func(*session.Instance) (string, error) { return " \n\n", nil },
			want:    "Nothing visible to copy (Copy Me)",
		},
		{
			name:    "capture failure",
			capture: func(*session.Instance) (string, error) { return "", errors.New("pane gone") },
			want:    "Could not copy visible terminal text: pane gone",
		},
		{
			name:    "clipboard failure",
			capture: func(*session.Instance) (string, error) { return "text", nil },
			clipboard: func(string, bool) (*clipboard.CopyResult, error) {
				return nil, errors.New("clipboard unavailable")
			},
			want: "Could not copy visible terminal text: clipboard unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := NewHome()
			t.Cleanup(func() {
				home.cancel()
				if home.storage != nil {
					_ = home.storage.Close()
				}
			})
			home.paneCapture = tt.capture
			home.paneClipboard = tt.clipboard
			msg := home.copyVisiblePane(inst)()
			model, _ := home.Update(msg)
			gotHome := model.(*Home)
			if gotHome.err == nil || gotHome.err.Error() != tt.want {
				t.Fatalf("status = %v, want %q", gotHome.err, tt.want)
			}
		})
	}
}

func TestIssue1595NormalizeVisiblePane(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "ansi URL and tmux padding",
			raw:  "\x1b[31mhttps://example.test/path\x1b[0m   \nsecond\tvalue  \n   \n",
			want: "https://example.test/path\nsecond\tvalue",
		},
		{
			name: "OSC title and CRLF",
			raw:  "\x1b]0;secret title\x07first\r\n\r\nthird\r\n",
			want: "first\n\nthird",
		},
		{
			name: "unsafe controls",
			raw:  "a\x00b\x07c\x7f",
			want: "abc",
		},
		{
			name: "bracketed paste CSI",
			raw:  "\x1b[200~https://example.test\x1b[201~",
			want: "https://example.test",
		},
		{
			name: "DCS APC PM and SOS strings",
			raw:  "\x1bPprivate-dcs\x1b\\\x1b_private-apc\x1b\\\x1b^private-pm\x1b\\\x1bXprivate-sos\x1b\\visible",
			want: "visible",
		},
		{
			name: "OSC terminated by ST",
			raw:  "\x1b]0;private title\x1b\\visible",
			want: "visible",
		},
		{
			name: "C1 CSI and string controls",
			raw:  "\x9b31mred\x9b0m\x90private\x9cvisible",
			want: "redvisible",
		},
		{
			name: "truncated string control",
			raw:  "visible\x1bPprivate",
			want: "visible",
		},
		{name: "empty", raw: " \t\n\n", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeVisiblePane(tt.raw); got != tt.want {
				t.Fatalf("normalizeVisiblePane() = %q, want %q", got, tt.want)
			}
		})
	}
}
