package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakePaneCapturer feeds canned pane snapshots back to the auto-handler.
// Each Capture pulls the next snapshot from frames; once exhausted the last
// frame is reused so a polling loop sees a steady-state pane.
type fakePaneCapturer struct {
	frames []string
	calls  atomic.Int32
	err    error
}

func (f *fakePaneCapturer) CapturePaneFresh() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	idx := int(f.calls.Add(1)) - 1
	if idx >= len(f.frames) {
		idx = len(f.frames) - 1
	}
	return f.frames[idx], nil
}

type fakeEnterSender struct {
	enters atomic.Int32
	err    error
}

func (f *fakeEnterSender) SendEnter() error {
	f.enters.Add(1)
	return f.err
}

// pickerSnapshot is the exact text the long-running session displays when
// claude --resume is invoked above the ~250k-token threshold.
const pickerSnapshot = `Welcome back!

  Choose how to continue this conversation:
> 1. Resume from summary (recommended)
  2. Resume full session as-is
  3. Don't ask me again

Enter to confirm · Esc to cancel
`

func TestAutoResolveClaudeResumePicker_DetectsAndPressesEnterOnce(t *testing.T) {
	cap := &fakePaneCapturer{frames: []string{pickerSnapshot}}
	send := &fakeEnterSender{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resolved, err := autoResolveClaudeResumePicker(ctx, cap, send, autoResumeOptions{
		PollInterval: 5 * time.Millisecond,
		Timeout:      200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resolved {
		t.Fatalf("expected resolved=true, got false")
	}
	if got := send.enters.Load(); got != 1 {
		t.Fatalf("expected exactly one Enter, got %d", got)
	}
}

func TestAutoResolveClaudeResumePicker_NoPickerNoEnter(t *testing.T) {
	cap := &fakePaneCapturer{frames: []string{"Just a normal claude prompt, no picker here.\n> "}}
	send := &fakeEnterSender{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resolved, err := autoResolveClaudeResumePicker(ctx, cap, send, autoResumeOptions{
		PollInterval: 5 * time.Millisecond,
		Timeout:      80 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved {
		t.Fatalf("expected resolved=false when picker absent")
	}
	if got := send.enters.Load(); got != 0 {
		t.Fatalf("expected no Enter, got %d", got)
	}
}

func TestAutoResolveClaudeResumePicker_PickerAppearsAfterDelay(t *testing.T) {
	// First three frames: still spawning, then picker, then post-Enter normal pane.
	cap := &fakePaneCapturer{frames: []string{
		"loading...",
		"loading...",
		pickerSnapshot,
		"Resumed. Ready.\n> ",
	}}
	send := &fakeEnterSender{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resolved, err := autoResolveClaudeResumePicker(ctx, cap, send, autoResumeOptions{
		PollInterval: 5 * time.Millisecond,
		Timeout:      300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resolved {
		t.Fatalf("expected resolved=true, got false")
	}
	if got := send.enters.Load(); got != 1 {
		t.Fatalf("expected exactly one Enter, got %d", got)
	}
}

func TestAutoResolveClaudeResumePicker_PartialMatchIsNotEnough(t *testing.T) {
	// A user message that mentions "Resume from summary" alone must NOT trigger.
	cap := &fakePaneCapturer{frames: []string{
		"> Hey claude, can you Resume from summary mode?\n",
	}}
	send := &fakeEnterSender{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resolved, _ := autoResolveClaudeResumePicker(ctx, cap, send, autoResumeOptions{
		PollInterval: 5 * time.Millisecond,
		Timeout:      80 * time.Millisecond,
	})
	if resolved {
		t.Fatalf("expected resolved=false on single-marker false positive")
	}
	if got := send.enters.Load(); got != 0 {
		t.Fatalf("expected no Enter, got %d", got)
	}
}

func TestAutoResolveClaudeResumePicker_CaptureErrorPropagates(t *testing.T) {
	cap := &fakePaneCapturer{err: errors.New("boom")}
	send := &fakeEnterSender{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resolved, err := autoResolveClaudeResumePicker(ctx, cap, send, autoResumeOptions{
		PollInterval: 5 * time.Millisecond,
		Timeout:      40 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("expected capture error to propagate")
	}
	if resolved {
		t.Fatalf("expected resolved=false on capture error")
	}
	if got := send.enters.Load(); got != 0 {
		t.Fatalf("expected no Enter on capture error, got %d", got)
	}
}

func TestPickerMarkers_MatchEvenWithANSIWrapping(t *testing.T) {
	// CapturePane returns ANSI-rich content (-e flag). The matcher must tolerate
	// SGR sequences interleaved with the marker text.
	ansi := "\x1b[1m> 1. \x1b[0mResume from summary \x1b[2m(recommended)\x1b[0m\n" +
		"  2. \x1b[1mResume full session\x1b[0m as-is\n"
	cap := &fakePaneCapturer{frames: []string{ansi}}
	send := &fakeEnterSender{}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resolved, err := autoResolveClaudeResumePicker(ctx, cap, send, autoResumeOptions{
		PollInterval: 5 * time.Millisecond,
		Timeout:      80 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resolved {
		t.Fatalf("expected ANSI-wrapped picker to match")
	}
	if got := send.enters.Load(); got != 1 {
		t.Fatalf("expected exactly one Enter, got %d", got)
	}
}
