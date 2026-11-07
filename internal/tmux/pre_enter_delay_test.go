// Tests for Session.preEnterDelay: the body→Enter pause is a per-session
// override because composers differ in how wide an input-burst window they
// coalesce before honouring a following Enter as submit. Devin CLI's editor
// treats an Enter arriving within ~100–150ms of the body as part of the burst
// and inserts it as a newline; the historical 100ms default sits inside that
// window, so Devin sessions carry a larger delay.
package tmux

import (
	"testing"
	"time"
)

func TestPreEnterDelay_DefaultWhenUnset(t *testing.T) {
	s := NewSession("preenter-default", "/tmp")
	if got := s.effectivePreEnterDelay(); got != preEnterDelayDefault {
		t.Fatalf("unset preEnterDelay = %v, want %v", got, preEnterDelayDefault)
	}
}

func TestPreEnterDelay_OverrideAndRestore(t *testing.T) {
	s := NewSession("preenter-override", "/tmp")
	s.SetPreEnterDelay(300 * time.Millisecond)
	if got := s.effectivePreEnterDelay(); got != 300*time.Millisecond {
		t.Fatalf("preEnterDelay after Set = %v, want 300ms", got)
	}
	s.SetPreEnterDelay(0)
	if got := s.effectivePreEnterDelay(); got != preEnterDelayDefault {
		t.Fatalf("preEnterDelay after reset to 0 = %v, want %v", got, preEnterDelayDefault)
	}
}

// TestSendKeysAndEnter_PreEnterDelaySpacesBodyFromEnter pins the property the
// field exists for: the configured pause actually sits between the body
// delivery and the Enter keystroke. With keySenderExec stubbed the only wait
// inside SendKeysAndEnter is that sleep, so elapsed wall time bounds it from
// below; the recorded argv pins the Enter as the last call.
func TestSendKeysAndEnter_PreEnterDelaySpacesBodyFromEnter(t *testing.T) {
	calls := recordTransport(t)
	s := NewSession("preenter-send", "/tmp")
	s.SetPreEnterDelay(80 * time.Millisecond)

	start := time.Now()
	if err := s.SendKeysAndEnter("probe-body"); err != nil {
		t.Fatalf("SendKeysAndEnter: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("SendKeysAndEnter returned after %v — Enter was not delayed by the configured 80ms", elapsed)
	}

	got := *calls
	if len(got) < 2 {
		t.Fatalf("recorded %d tmux calls, want at least body + Enter", len(got))
	}
	last := got[len(got)-1].argv
	if last[0] != "send-keys" || hasFlag(last, "-l") || last[len(last)-1] != "Enter" {
		t.Fatalf("last tmux call = %v, want `send-keys Enter` after the body", last)
	}
}
