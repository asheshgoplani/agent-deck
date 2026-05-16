// Package teatesthelper wraps charmbracelet/x/exp/teatest with the
// conventions our TUI tests need (TUI-TEST-PLAN.md §6.1):
//   - reasonable default term size + finalize timeout
//   - SendKey for the 90% case (a single rune)
//   - WaitForBytes that takes a substring instead of a closure
//   - Output / FinalModel / Quit pass-throughs
//
// The helper does NOT replace teatest — it just trims boilerplate.
// Tests that need a closure-based WaitFor or custom teatest options
// can drop down to teatest directly.
//
// Important: do NOT pass a model whose Init spawns workers / tickers
// straight into NewProgram — wrap it in a tea.Model adapter that
// returns nil from Init, mirroring the seamBTestWrapper pattern in
// internal/ui/tui_eval_seam_b_test.go.
package teatesthelper

import (
	"bytes"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// Default settings tuned for TUI smoke tests.
const (
	defaultWidth   = 140
	defaultHeight  = 50
	defaultTimeout = 2 * time.Second
)

// Option configures NewProgram.
type Option func(*config)

type config struct {
	width, height int
	timeout       time.Duration
}

// WithSize overrides the initial term width/height.
func WithSize(w, h int) Option {
	return func(c *config) { c.width, c.height = w, h }
}

// WithTimeout overrides the FinalOutput / FinalModel deadline.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// Program is the test handle returned by NewProgram.
type Program struct {
	tm  *teatest.TestModel
	cfg config
}

// NewProgram boots a teatest TestModel with default size + a synthetic
// WindowSizeMsg so layouts have real dimensions on first View.
func NewProgram(t *testing.T, m tea.Model, opts ...Option) *Program {
	t.Helper()
	cfg := config{width: defaultWidth, height: defaultHeight, timeout: defaultTimeout}
	for _, opt := range opts {
		opt(&cfg)
	}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(cfg.width, cfg.height))
	tm.Send(tea.WindowSizeMsg{Width: cfg.width, Height: cfg.height})
	return &Program{tm: tm, cfg: cfg}
}

// SendKey sends a single rune as a tea.KeyMsg{Type: tea.KeyRunes}.
// For special keys (Esc, Enter, arrows) use SendKeyType.
func (p *Program) SendKey(r rune) {
	p.tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

// SendKeyType sends a special-key tea.KeyMsg (e.g. tea.KeyEsc).
func (p *Program) SendKeyType(kt tea.KeyType) {
	p.tm.Send(tea.KeyMsg{Type: kt})
}

// SendMsg forwards an arbitrary tea.Msg.
func (p *Program) SendMsg(msg tea.Msg) { p.tm.Send(msg) }

// WaitForBytes polls Output until it contains substr or timeout elapses.
// Returns true on hit, false on timeout. Use this instead of teatest's
// closure-based WaitFor for the common substring case.
func (p *Program) WaitForBytes(substr []byte, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bytes.Contains(p.snapshot(), substr) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return bytes.Contains(p.snapshot(), substr)
}

// Output returns the cumulative captured rendered output. Safe to call
// before Quit (returns whatever has been written so far).
func (p *Program) Output(t *testing.T) []byte {
	t.Helper()
	r := p.tm.FinalOutput(t, teatest.WithFinalTimeout(p.cfg.timeout))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("teatesthelper: read output: %v", err)
	}
	return out
}

// snapshot returns the current rendered bytes without enforcing a
// finalize timeout — used by WaitForBytes which polls.
func (p *Program) snapshot() []byte {
	out := p.tm.Output()
	b, err := io.ReadAll(out)
	if err != nil {
		return nil
	}
	return b
}

// FinalModel returns the post-Quit model. Cast to your concrete type.
func (p *Program) FinalModel(t *testing.T) tea.Model {
	t.Helper()
	return p.tm.FinalModel(t, teatest.WithFinalTimeout(p.cfg.timeout))
}

// Quit asks the program to shut down. Idempotent for already-exited programs.
func (p *Program) Quit() error {
	p.tm.Send(tea.QuitMsg{})
	return p.tm.Quit()
}
