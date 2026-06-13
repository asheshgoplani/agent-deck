package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestGenerateJumpHints(t *testing.T) {
	t.Run("zero", func(t *testing.T) {
		got := generateJumpHints(0)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("one", func(t *testing.T) {
		got := generateJumpHints(1)
		if len(got) != 1 {
			t.Fatalf("expected 1 hint, got %d", len(got))
		}
		if len(got[0]) != 1 {
			t.Errorf("expected single-char hint, got %q", got[0])
		}
	})

	t.Run("count matches", func(t *testing.T) {
		for _, n := range []int{1, 5, 14, 15, 30, 60, 200} {
			got := generateJumpHints(n)
			if len(got) != n {
				t.Errorf("generateJumpHints(%d) returned %d hints", n, len(got))
			}
		}
	})

	t.Run("all single char up to charset minus one", func(t *testing.T) {
		// With 14 hint characters, up to 13 items should all be single-char
		hints := generateJumpHints(len(hintCharacters) - 1)
		for i, h := range hints {
			if len(h) != 1 {
				t.Errorf("hint[%d] = %q, expected single char for %d items", i, h, len(hints))
			}
		}
	})

	t.Run("uses only hint characters", func(t *testing.T) {
		hints := generateJumpHints(50)
		allowed := make(map[rune]bool)
		for _, ch := range hintCharacters {
			allowed[ch] = true
		}
		for i, h := range hints {
			for _, ch := range h {
				if !allowed[ch] {
					t.Errorf("hint[%d] = %q contains char %c not in hintCharacters", i, h, ch)
				}
			}
		}
	})

	t.Run("sorted distribution", func(t *testing.T) {
		// Hints should be sorted so two-char hints interleave with single-char
		hints := generateJumpHints(20)
		hasSingleChar := false
		hasTwoChar := false
		for _, h := range hints {
			if len(h) == 1 {
				hasSingleChar = true
			}
			if len(h) == 2 {
				hasTwoChar = true
			}
		}
		if !hasSingleChar || !hasTwoChar {
			t.Error("expected mix of single and two-char hints for 20 items")
		}
	})
}

func TestGenerateJumpHintsUnique(t *testing.T) {
	for _, n := range []int{14, 30, 60, 200} {
		hints := generateJumpHints(n)
		seen := make(map[string]bool)
		for i, h := range hints {
			if seen[h] {
				t.Errorf("duplicate hint %q at index %d (count=%d)", h, i, n)
			}
			seen[h] = true
		}
	}
}

func TestJumpHintMatch(t *testing.T) {
	hints := generateJumpHints(20)

	t.Run("empty buffer is prefix", func(t *testing.T) {
		got := matchJumpHint(hints, "")
		if got.matched || !got.isPrefix {
			t.Errorf("expected prefix-only for empty buffer, got %+v", got)
		}
	})

	t.Run("single char exact match", func(t *testing.T) {
		// Find a single-char hint
		for i, h := range hints {
			if len(h) == 1 {
				// Check it's not a prefix of any two-char hint
				isPrefix := false
				for j, other := range hints {
					if j != i && len(other) > 1 && other[0] == h[0] {
						isPrefix = true
						break
					}
				}
				if !isPrefix {
					got := matchJumpHint(hints, h)
					if !got.matched || got.index != i {
						t.Errorf("expected match at %d for %q, got %+v", i, h, got)
					}
					return
				}
			}
		}
	})

	t.Run("two char exact match", func(t *testing.T) {
		for i, h := range hints {
			if len(h) == 2 {
				got := matchJumpHint(hints, h)
				if !got.matched || got.index != i {
					t.Errorf("expected match at %d for %q, got %+v", i, h, got)
				}
				return
			}
		}
	})

	t.Run("no match", func(t *testing.T) {
		got := matchJumpHint(hints, "zz")
		if got.matched || got.isPrefix {
			t.Errorf("expected no match for 'zz', got %+v", got)
		}
	})

	t.Run("prefix of two-char hint", func(t *testing.T) {
		// Find the prefix character used by two-char hints
		for _, h := range hints {
			if len(h) == 2 {
				prefix := string(h[0])
				got := matchJumpHint(hints, prefix)
				if got.matched {
					t.Errorf("prefix %q should not match directly, got %+v", prefix, got)
				}
				if !got.isPrefix {
					t.Errorf("prefix %q should be a valid prefix, got %+v", prefix, got)
				}
				return
			}
		}
	})
}

func TestJumpHintMatchAllSingleChar(t *testing.T) {
	// With few items, all hints are single chars — every one should match exactly
	hints := generateJumpHints(5)
	for i, h := range hints {
		got := matchJumpHint(hints, h)
		if !got.matched || got.index != i {
			t.Errorf("matchJumpHint(%q) with 5 hints = %+v, want match at %d", h, got, i)
		}
	}
}

func TestJumpModeRenderDoesNotSpendHintOnDivider(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 20
	home.initialLoading = false
	home.jumpMode = true
	home.flatItems = []session.Item{
		{Type: session.ItemTypeGroup, Group: &session.Group{Name: "alpha", Path: "alpha"}, Path: "alpha", Level: 0},
		{Type: session.ItemTypeDivider, DividerLabel: "idle / done"},
		{Type: session.ItemTypeGroup, Group: &session.Group{Name: "beta", Path: "beta"}, Path: "beta", Level: 0},
	}

	rendered := home.renderSessionList(120, 20)
	selectableHints := generateJumpHints(2)
	allRowHints := generateJumpHints(3)

	if !strings.Contains(rendered, selectableHints[1]) {
		t.Fatalf("rendered jump hints should include second selectable hint %q\n--- render ---\n%s", selectableHints[1], rendered)
	}
	if selectableHints[1] != allRowHints[2] && strings.Contains(rendered, allRowHints[2]) {
		t.Fatalf("rendered jump hints consumed divider row hint %q\n--- render ---\n%s", allRowHints[2], rendered)
	}
}

func TestJumpKeyDoesNotSpendHintOnDivider(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 20
	home.initialLoading = false
	home.jumpMode = true
	home.flatItems = []session.Item{
		{Type: session.ItemTypeGroup, Group: &session.Group{Name: "alpha", Path: "alpha"}, Path: "alpha", Level: 0},
		{Type: session.ItemTypeDivider, DividerLabel: "idle / done"},
		{Type: session.ItemTypeGroup, Group: &session.Group{Name: "beta", Path: "beta"}, Path: "beta", Level: 0},
	}
	hint := generateJumpHints(2)[1]

	home.handleJumpKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(hint)})

	if home.cursor != 2 {
		t.Fatalf("second selectable jump hint should land on beta at index 2, got cursor=%d", home.cursor)
	}
}

func TestReplaceVisibleRange(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		start       int
		n           int
		replacement string
		want        string
	}{
		{"prefix plain", "hello world", 0, 2, "XX", "XXllo world"},
		{"prefix single", "abc", 0, 1, "X", "Xbc"},
		{"prefix full", "ab", 0, 2, "XY", "XY"},
		{"prefix multibyte", "▶├─ session", 0, 1, "a", "a├─ session"},
		{"prefix multibyte two", "▾▸ group", 0, 2, "ab", "ab group"},
		{"prefix ansi multibyte", "\x1b[31m▶\x1b[0m rest", 0, 1, "a", "a rest"},
		{"at name offset", "├─ ○ myapp claude", 5, 2, "AB", "├─ ○ ABapp claude"},
		{"at name with ansi", "\x1b[31m├─\x1b[0m ○ hello world", 5, 2, "XY", "\x1b[31m├─\x1b[0m ○ XYllo world"},
		{"middle of string", "abcdefgh", 3, 2, "XY", "abcXYfgh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replaceVisibleRange(tt.input, tt.start, tt.n, tt.replacement)
			if got != tt.want {
				t.Errorf("replaceVisibleRange(%q, %d, %d, %q) = %q, want %q",
					tt.input, tt.start, tt.n, tt.replacement, got, tt.want)
			}
		})
	}
}

func TestFindNameOffset(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		target string
		want   int
	}{
		{"plain", "├─ ○ myapp claude", "myapp", 5},
		{"with ansi", "\x1b[31m├─\x1b[0m ○ hello world", "hello", 5},
		{"group", "1·▾ conductor (1)", "conductor", 4},
		{"not found", "some text", "missing", 0},
		{"empty name", "some text", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findNameOffset(tt.line, tt.target)
			if got != tt.want {
				t.Errorf("findNameOffset(%q, %q) = %d, want %d", tt.line, tt.target, got, tt.want)
			}
		})
	}
}

func TestJumpItemName_Window(t *testing.T) {
	item := session.Item{
		Type:       session.ItemTypeWindow,
		WindowName: "build-logs",
	}

	if got := jumpItemName(item); got != "build-logs" {
		t.Fatalf("jumpItemName(window) = %q, want %q", got, "build-logs")
	}
}

func TestStripAnsi(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ansi", "hello", "hello"},
		{"with color", "\x1b[31mhello\x1b[0m", "hello"},
		{"bold", "\x1b[1mbold\x1b[0m text", "bold text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripAnsi(tt.input)
			if got != tt.want {
				t.Errorf("stripAnsi(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
