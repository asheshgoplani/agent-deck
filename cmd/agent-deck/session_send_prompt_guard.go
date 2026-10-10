package main

import (
	"errors"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var errInputPromptUnavailable = errors.New("input prompt unavailable")

// promptGuardTarget is used only while performSend holds the target send lock.
// Each transport call checks the current pane, including Enter after a paste.
type promptGuardTarget struct {
	sendRetryTarget
	tool         string
	typedBatches int
	refused      bool
	menuOpen     bool
	refusalPane  string
}

func (g *promptGuardTarget) check(raw string, captureErr error) error {
	if captureErr != nil {
		g.refused = true
		return fmt.Errorf("%w: pane unreadable: %v", errInputPromptUnavailable, captureErr)
	}
	tool := g.tool
	if session.IsClaudeCompatible(tool) {
		tool = "claude"
	} else if session.IsCodexCompatible(tool) {
		tool = "codex"
	}
	detector := tmux.NewPromptDetector(tool)
	content := tmux.StripANSI(raw)
	if detector.ClassifySubstate(content) == tmux.SubstateInteractiveMenu {
		g.menuOpen = true
		g.refused = true
		if g.typedBatches > 0 {
			g.refusalPane = content
		}
		return errInputPromptUnavailable
	}
	if !detector.HasPrompt(content) {
		g.refused = true
		return errInputPromptUnavailable
	}
	return nil
}

// Once a batch has been typed, a redraw or missing prompt cannot safely
// revoke submission. Only a visible picker may withhold Enter.
func (g *promptGuardTarget) checkAfterTyping(raw string, captureErr error) error {
	if captureErr != nil {
		return nil
	}
	tool := g.tool
	if session.IsClaudeCompatible(tool) {
		tool = "claude"
	} else if session.IsCodexCompatible(tool) {
		tool = "codex"
	}
	content := tmux.StripANSI(raw)
	if tmux.NewPromptDetector(tool).ClassifySubstate(content) != tmux.SubstateInteractiveMenu {
		return nil
	}
	g.menuOpen = true
	g.refused = true
	g.refusalPane = content
	return errInputPromptUnavailable
}

func (g *promptGuardTarget) beforeBatch() error {
	raw, err := g.CapturePaneFresh()
	return g.check(raw, err)
}

func (g *promptGuardTarget) authorizeBatch() error {
	if g.typedBatches == 0 {
		if err := g.beforeBatch(); err != nil {
			return err
		}
	} else {
		raw, err := g.CapturePaneFresh()
		if guardErr := g.checkAfterTyping(raw, err); guardErr != nil {
			return guardErr
		}
	}
	g.typedBatches++
	return nil
}

func (g *promptGuardTarget) SendKeysAndEnter(keys string) error {
	return g.SendKeysAndEnterChecked(keys, nil, nil)
}

func (g *promptGuardTarget) SendKeysAndEnterChecked(keys string, _ func() (string, error), check tmux.PostPasteCheck) error {
	afterPaste := func(raw string, err error) (bool, error) {
		if guardErr := g.checkAfterTyping(raw, err); guardErr != nil {
			return false, guardErr
		}
		if check != nil {
			ok, checkErr := check(raw, err)
			if !ok {
				return false, checkErr
			}
		}
		return true, nil
	}
	if sender, ok := g.sendRetryTarget.(interface {
		SendKeysAndEnterCheckedGuarded(string, func() (string, error), tmux.PostPasteCheck, func() error) error
	}); ok {
		return sender.SendKeysAndEnterCheckedGuarded(keys, g.CapturePaneFresh, afterPaste, g.authorizeBatch)
	}
	if err := g.authorizeBatch(); err != nil {
		return err
	}
	return g.sendRetryTarget.SendKeysAndEnterChecked(keys, g.CapturePaneFresh, func(raw string, err error) (bool, error) {
		ok, checkErr := afterPaste(raw, err)
		if !ok {
			return false, checkErr
		}
		if guardErr := g.authorizeBatch(); guardErr != nil {
			return false, guardErr
		}
		return true, nil
	})
}

func (g *promptGuardTarget) SendEnter() error {
	if sender, ok := g.sendRetryTarget.(interface{ SendEnterChecked(func() error) error }); ok {
		return sender.SendEnterChecked(g.authorizeBatch)
	}
	if err := g.authorizeBatch(); err != nil {
		return err
	}
	return g.sendRetryTarget.SendEnter()
}

func (g *promptGuardTarget) SendCtrlC() error {
	if err := g.authorizeBatch(); err != nil {
		return err
	}
	return g.sendRetryTarget.SendCtrlC()
}

func (g *promptGuardTarget) SendKeysChunked(keys string) error {
	if sender, ok := g.sendRetryTarget.(interface {
		SendKeysChunkedChecked(string, func() error) error
	}); ok {
		return sender.SendKeysChunkedChecked(keys, g.authorizeBatch)
	}
	if err := g.authorizeBatch(); err != nil {
		return err
	}
	return g.sendRetryTarget.SendKeysChunked(keys)
}

func (g *promptGuardTarget) refusal() (string, error) {
	if g.typedBatches > 0 {
		pane := []rune(g.refusalPane)
		if len(pane) > 2048 {
			pane = pane[len(pane)-2048:]
		}
		return deliveryTypedNotSubmitted, fmt.Errorf("typed, not submitted: %d keystroke batch(es) typed before refusal; pane=%q", g.typedBatches, string(pane))
	}
	if g.menuOpen {
		return deliveryMenuOpen, fmt.Errorf("input prompt unavailable; no keys typed")
	}
	return deliveryComposerBlocked, fmt.Errorf("input prompt unavailable; no keys typed")
}
