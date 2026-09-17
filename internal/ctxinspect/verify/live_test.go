package verify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedPane returns a scripted sequence of pane captures and records what
// was typed. It never touches tmux, so the whole live path is exercised without
// a session, a terminal or a harness.
type scriptedPane struct {
	frames     []string
	captures   int
	sent       []string
	sendErr    error
	captureErr error
	history    string
	historyErr error
}

func (p *scriptedPane) CapturePaneFresh() (string, error) {
	if p.captureErr != nil {
		return "", p.captureErr
	}
	i := p.captures
	p.captures++
	if i >= len(p.frames) {
		i = len(p.frames) - 1
	}
	if i < 0 {
		return "", nil
	}
	return p.frames[i], nil
}

func (p *scriptedPane) SendKeysAndEnter(keys string) error {
	if p.sendErr != nil {
		return p.sendErr
	}
	p.sent = append(p.sent, keys)
	return nil
}

// scrollbackPane adds history capture, which *tmux.Session also provides.
type scrollbackPane struct{ *scriptedPane }

func (p scrollbackPane) CaptureHistoryLines(int) (string, error) {
	if p.historyErr != nil {
		return "", p.historyErr
	}
	return p.history, nil
}

// noSleep advances nothing: the poll loop is driven by the scripted frames, not
// by real time.
func noSleep(time.Duration) {}

func liveOpts() LiveOptions {
	return LiveOptions{PollInterval: time.Millisecond, Timeout: time.Second, Settle: 0, Sleep: noSleep}
}

func TestRunLiveSendsTheCommandAndReadsThePanel(t *testing.T) {
	pane := &scriptedPane{frames: []string{"still thinking…", "still thinking…", claudeContextPane}}
	spec := claudeSpec(t)

	h, err := RunLive(context.Background(), spec, pane, pane, liveOpts())
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	if len(pane.sent) != 1 || pane.sent[0] != ClaudeCommand {
		t.Fatalf("sent = %v, want exactly [%s]", pane.sent, ClaudeCommand)
	}
	if _, ok := h.Figure("memory files"); !ok {
		t.Fatal("the parsed panel is missing its rows")
	}
}

// TestRunLiveSendsExactlyOnce: the command becomes part of the user's
// conversation, so a retry loop that re-sends it would spam their session.
func TestRunLiveSendsExactlyOnce(t *testing.T) {
	pane := &scriptedPane{frames: []string{"", "", "", claudeContextPane}}
	if _, err := RunLive(context.Background(), claudeSpec(t), pane, pane, liveOpts()); err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	if len(pane.sent) != 1 {
		t.Fatalf("the command was sent %d times, want 1", len(pane.sent))
	}
}

func TestRunLiveTimesOutWhenThePanelNeverRenders(t *testing.T) {
	pane := &scriptedPane{frames: []string{"working…"}}
	opts := liveOpts()
	opts.Timeout = time.Millisecond

	_, err := RunLive(context.Background(), claudeSpec(t), pane, pane, opts)
	if !errors.Is(err, ErrPanelTimeout) {
		t.Fatalf("err = %v, want ErrPanelTimeout", err)
	}
}

// TestRunLiveNeverReturnsAnEmptyReportOnFailure is the honesty gate on the live
// path: a failure must be an error, because an empty report renders as a table
// of zeroes that reads like agreement.
func TestRunLiveNeverReturnsAnEmptyReportOnFailure(t *testing.T) {
	pane := &scriptedPane{frames: []string{"working…"}}
	opts := liveOpts()
	opts.Timeout = time.Millisecond

	h, err := RunLive(context.Background(), claudeSpec(t), pane, pane, opts)
	if err == nil {
		t.Fatal("a panel that never rendered must be an error")
	}
	if h != nil {
		t.Fatal("a failed capture must not return a report")
	}
}

func TestRunLivePropagatesSendAndCaptureFailures(t *testing.T) {
	sendFail := &scriptedPane{sendErr: errors.New("pane is dead")}
	if _, err := RunLive(context.Background(), claudeSpec(t), sendFail, sendFail, liveOpts()); err == nil ||
		!strings.Contains(err.Error(), "pane is dead") {
		t.Fatalf("send failure = %v, want it propagated", err)
	}

	captureFail := &scriptedPane{captureErr: errors.New("capture refused")}
	if _, err := RunLive(context.Background(), claudeSpec(t), captureFail, captureFail, liveOpts()); err == nil ||
		!strings.Contains(err.Error(), "capture refused") {
		t.Fatalf("capture failure = %v, want it propagated", err)
	}
}

// TestRunLivePrefersScrollbackWhenItHoldsMoreRows: a panel taller than the
// visible pane is truncated by a plain capture, and a truncated panel parses
// into fewer rows — which would silently narrow the comparison.
func TestRunLivePrefersScrollbackWhenItHoldsMoreRows(t *testing.T) {
	truncated := strings.Join(strings.Split(claudeContextPane, "\n")[:8], "\n")
	base := &scriptedPane{frames: []string{truncated}, history: claudeContextPane}
	pane := scrollbackPane{base}

	h, err := RunLive(context.Background(), claudeSpec(t), pane, pane, liveOpts())
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	if _, ok := h.Figure("messages"); !ok {
		t.Fatalf("the scrollback capture should have supplied the full panel, got %s", describeFigures(h.Figures))
	}
}

// TestRunLiveKeepsThePaneReadWhenScrollbackIsWorse: scrollback is best-effort,
// so a failing or shorter history read must not lose the panel we already have.
func TestRunLiveKeepsThePaneReadWhenScrollbackIsWorse(t *testing.T) {
	base := &scriptedPane{frames: []string{claudeContextPane}, historyErr: errors.New("no history")}
	pane := scrollbackPane{base}

	h, err := RunLive(context.Background(), claudeSpec(t), pane, pane, liveOpts())
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	if _, ok := h.Figure("memory files"); !ok {
		t.Fatal("a failed scrollback read must leave the pane capture intact")
	}
}

func TestRunLiveRefusesAnAdapterWithNoSpec(t *testing.T) {
	pane := &scriptedPane{frames: []string{claudeContextPane}}
	var unverifiable ErrUnverifiable
	_, err := RunLive(context.Background(), Spec{Harness: "generic"}, pane, pane, liveOpts())
	if !errors.As(err, &unverifiable) {
		t.Fatalf("err = %v, want ErrUnverifiable", err)
	}
	if len(pane.sent) != 0 {
		t.Fatal("nothing may be typed into a session that cannot be verified")
	}
}

func TestRunLiveHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pane := &scriptedPane{frames: []string{"working…"}}

	if _, err := RunLive(ctx, claudeSpec(t), pane, pane, liveOpts()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(pane.sent) != 0 {
		t.Fatal("an already-cancelled run must not leave a stray command in the conversation")
	}
}

func TestLiveOptionsDefaults(t *testing.T) {
	o := LiveOptions{}.withDefaults()
	if o.PollInterval <= 0 || o.Timeout <= 0 || o.Settle <= 0 || o.Sleep == nil {
		t.Fatalf("defaults left a field unusable: %+v", o)
	}
}
