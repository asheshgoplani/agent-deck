package teatesthelper_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/testutil/teatesthelper"
)

// dummyModel is a minimal tea.Model whose Update reacts to runes by
// appending them to a buffer; View renders the buffer prefixed with
// "BUF:" so tests can WaitForBytes against a stable substring.
type dummyModel struct{ buf []rune }

func (m *dummyModel) Init() tea.Cmd { return nil }

func (m *dummyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyRunes {
		m.buf = append(m.buf, k.Runes...)
	}
	if _, ok := msg.(tea.QuitMsg); ok {
		return m, tea.Quit
	}
	return m, nil
}

func (m *dummyModel) View() string {
	return "BUF:" + string(m.buf)
}

func TestNewProgram_RunsAndCapturesOutput(t *testing.T) {
	p := teatesthelper.NewProgram(t, &dummyModel{})

	p.SendKey('a')
	p.SendKey('b')
	p.SendKey('c')

	if err := p.Quit(); err != nil {
		t.Logf("Quit: %v", err)
	}

	out := p.Output(t)
	if !bytes.Contains(out, []byte("BUF:abc")) {
		t.Fatalf("output did not contain BUF:abc; got: %q", string(out))
	}
}

func TestWaitForBytes_FindsSubstring(t *testing.T) {
	p := teatesthelper.NewProgram(t, &dummyModel{})

	p.SendKey('z')
	if !p.WaitForBytes([]byte("BUF:z"), 2*time.Second) {
		t.Fatalf("WaitForBytes timed out; output: %q", string(p.Output(t)))
	}
	_ = p.Quit()
}

func TestWaitForBytes_ReturnsFalseOnTimeout(t *testing.T) {
	p := teatesthelper.NewProgram(t, &dummyModel{})

	if p.WaitForBytes([]byte("never"), 100*time.Millisecond) {
		t.Fatal("WaitForBytes returned true for absent substring")
	}
	_ = p.Quit()
}

func TestSendKey_Special(t *testing.T) {
	p := teatesthelper.NewProgram(t, &dummyModel{})
	// Non-rune keys should not crash and should not append to buf.
	p.SendKeyType(tea.KeyEsc)
	p.SendKey('x')
	_ = p.Quit()

	out := p.Output(t)
	if !strings.Contains(string(out), "BUF:x") {
		t.Fatalf("output missing BUF:x: %q", string(out))
	}
}

func TestNewProgram_RespectsCustomSize(t *testing.T) {
	p := teatesthelper.NewProgram(t, &dummyModel{},
		teatesthelper.WithSize(80, 24),
	)
	p.SendKey('q')
	_ = p.Quit()
	// Size assertion via View truncation isn't meaningful for dummyModel;
	// this test just proves the option doesn't panic and is wired through.
	_ = p.Output(t)
}

func TestFinalModel_ReturnsTypedModel(t *testing.T) {
	p := teatesthelper.NewProgram(t, &dummyModel{})
	p.SendKey('h')
	p.SendKey('i')
	_ = p.Quit()

	final := p.FinalModel(t).(*dummyModel)
	if string(final.buf) != "hi" {
		t.Fatalf("final.buf=%q want hi", string(final.buf))
	}
}
