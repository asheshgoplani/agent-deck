package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestExtractCodeBlocks_Basic(t *testing.T) {
	text := "Here is the fix:\n\n```sql\nSELECT * FROM users WHERE id = 1;\n```\n\nAnd run this:\n\n```bash\nls -la\nrm tmp.txt\n```\n"
	blocks := extractCodeBlocks(text)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Lang != "sql" {
		t.Errorf("block 0 lang: want sql, got %q", blocks[0].Lang)
	}
	if blocks[0].Content != "SELECT * FROM users WHERE id = 1;" {
		t.Errorf("block 0 content: %q", blocks[0].Content)
	}
	if blocks[1].Lang != "bash" {
		t.Errorf("block 1 lang: want bash, got %q", blocks[1].Lang)
	}
	if blocks[1].Content != "ls -la\nrm tmp.txt" {
		t.Errorf("block 1 content: %q", blocks[1].Content)
	}
	if blocks[1].LineCount() != 2 {
		t.Errorf("block 1 line count: want 2, got %d", blocks[1].LineCount())
	}
}

func TestExtractCodeBlocks_NoFences(t *testing.T) {
	if got := extractCodeBlocks("just some prose, no code at all"); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
	if got := extractCodeBlocks(""); got != nil {
		t.Errorf("expected nil for empty, got %+v", got)
	}
}

func TestExtractCodeBlocks_NoLang(t *testing.T) {
	blocks := extractCodeBlocks("```\nplain command\n```")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Lang != "" {
		t.Errorf("expected empty lang, got %q", blocks[0].Lang)
	}
	if blocks[0].Content != "plain command" {
		t.Errorf("content: %q", blocks[0].Content)
	}
}

func TestExtractCodeBlocks_SkipsEmptyBlocks(t *testing.T) {
	// An empty fenced block (no body) should be skipped.
	blocks := extractCodeBlocks("```\n```\n\n```sh\necho hi\n```")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 non-empty block, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Content != "echo hi" {
		t.Errorf("content: %q", blocks[0].Content)
	}
}

func TestExtractCodeBlocks_UnterminatedFenceClosesAtEOF(t *testing.T) {
	// A streaming/half-finished block (no closing fence) must still be copyable.
	blocks := extractCodeBlocks("```bash\ngit push origin main")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block from unterminated fence, got %d", len(blocks))
	}
	if blocks[0].Content != "git push origin main" {
		t.Errorf("content: %q", blocks[0].Content)
	}
}

func TestExtractCodeBlocks_IgnoresInlineBackticks(t *testing.T) {
	// Inline code like `run foo` must NOT be treated as a fence.
	blocks := extractCodeBlocks("Use the `foo` command, then:\n```\nfoo --bar\n```")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Content != "foo --bar" {
		t.Errorf("content: %q", blocks[0].Content)
	}
}

func TestExtractCodeBlocks_IndentedFence(t *testing.T) {
	// Agents sometimes emit fences with leading indentation (list items).
	blocks := extractCodeBlocks("  ```\n  some cmd\n  ```")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

func TestCodeBlock_FirstLine(t *testing.T) {
	b := CodeBlock{Content: "\n\n  SELECT 1;\nSELECT 2;"}
	if got := b.FirstLine(); got != "SELECT 1;" {
		t.Errorf("FirstLine: want %q, got %q", "SELECT 1;", got)
	}
	if got := (CodeBlock{}).FirstLine(); got != "" {
		t.Errorf("empty FirstLine should be empty, got %q", got)
	}
}

func TestCodeBlockDialog_ShowEmptyReturnsFalse(t *testing.T) {
	d := NewCodeBlockDialog()
	if d.Show("sess", nil) {
		t.Error("Show with no blocks should return false")
	}
	if d.IsVisible() {
		t.Error("dialog should not be visible with no blocks")
	}
}

func TestCodeBlockDialog_NavigationAndSelection(t *testing.T) {
	d := NewCodeBlockDialog()
	blocks := []CodeBlock{
		{Lang: "sql", Content: "SELECT 1;"},
		{Lang: "bash", Content: "ls"},
		{Lang: "", Content: "third"},
	}
	if !d.Show("mysession", blocks) {
		t.Fatal("Show with blocks should return true")
	}
	if !d.IsVisible() {
		t.Fatal("dialog should be visible")
	}
	if got := d.GetSelected(); got == nil || got.Content != "SELECT 1;" {
		t.Fatalf("initial selection should be first block, got %+v", got)
	}

	// Down twice -> third block.
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if got := d.GetSelected(); got == nil || got.Content != "third" {
		t.Fatalf("after 2x down, selection should be third, got %+v", got)
	}

	// Down again wraps to first.
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if got := d.GetSelected(); got == nil || got.Content != "SELECT 1;" {
		t.Fatalf("down should wrap to first, got %+v", got)
	}

	// Up wraps to last.
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if got := d.GetSelected(); got == nil || got.Content != "third" {
		t.Fatalf("up should wrap to last, got %+v", got)
	}

	// Esc hides.
	d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if d.IsVisible() {
		t.Error("esc should hide the dialog")
	}
}

func TestCodeBlockDialog_View(t *testing.T) {
	d := NewCodeBlockDialog()
	d.SetSize(120, 40)
	d.Show("mysession", []CodeBlock{{Lang: "sql", Content: "SELECT 1;"}})
	view := d.View()
	if view == "" {
		t.Fatal("visible dialog should render a non-empty view")
	}
	// Hidden dialog renders nothing.
	d.Hide()
	if d.View() != "" {
		t.Error("hidden dialog should render empty view")
	}
}

func TestExtractCodeBlocks_NestedFenceNotClosedByInner(t *testing.T) {
	// An outer 4-backtick fence wrapping an inner ``` fence: the inner triple
	// backticks are content, not a closer (CommonMark: closer must be >= opener
	// length and bare). The whole inner markdown is one block.
	text := "````md\n```sh\necho hi\n```\n````"
	blocks := extractCodeBlocks(text)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Lang != "md" {
		t.Errorf("lang: want md, got %q", blocks[0].Lang)
	}
	want := "```sh\necho hi\n```"
	if blocks[0].Content != want {
		t.Errorf("content: want %q, got %q", want, blocks[0].Content)
	}
}

func TestExtractCodeBlocks_InfoStringDoesNotClose(t *testing.T) {
	// A "```lang" line inside an open block must NOT close it (closers are bare).
	text := "```\nline1\n```python\nline2\n```"
	blocks := extractCodeBlocks(text)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (info-bearing fence is content), got %d: %+v", len(blocks), blocks)
	}
	want := "line1\n```python\nline2"
	if blocks[0].Content != want {
		t.Errorf("content: want %q, got %q", want, blocks[0].Content)
	}
}

func TestExtractCodeBlocks_LongerCloserAccepted(t *testing.T) {
	// Opening with 3, closing with 4 backticks: a bare closer >= opener closes.
	blocks := extractCodeBlocks("```\necho hi\n````")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Content != "echo hi" {
		t.Errorf("content: %q", blocks[0].Content)
	}
}

func TestWindowBounds(t *testing.T) {
	if s, e := windowBounds(0, 3, 10); s != 0 || e != 3 {
		t.Errorf("all-fit: got [%d,%d), want [0,3)", s, e)
	}
	if s, e := windowBounds(0, 100, 10); s != 0 || e != 10 {
		t.Errorf("top: got [%d,%d), want [0,10)", s, e)
	}
	if s, e := windowBounds(50, 100, 10); s != 45 || e != 55 {
		t.Errorf("middle: got [%d,%d), want [45,55)", s, e)
	}
	if s, e := windowBounds(99, 100, 10); s != 90 || e != 100 {
		t.Errorf("end: got [%d,%d), want [90,100)", s, e)
	}
}

func TestCodeBlockDialog_ViewWithManyBlocksFitsHeight(t *testing.T) {
	d := NewCodeBlockDialog()
	d.SetSize(120, 20) // small height forces windowing
	blocks := make([]CodeBlock, 50)
	for i := range blocks {
		blocks[i] = CodeBlock{Lang: "sh", Content: "cmd"}
	}
	d.Show("sess", blocks)
	view := d.View()
	if view == "" {
		t.Fatal("view should be non-empty")
	}
	rows := strings.Count(view, "\n") + 1
	if rows > 20 {
		t.Errorf("windowed view should fit in height 20, got %d rows", rows)
	}
}
