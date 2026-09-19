// Regression tests for issue #2079: a `launch --message-file` (or any other
// multi-line send) whose paste lands truncated must not have its fragment
// submitted with Enter, and the caller must learn about it instead of seeing
// a clean-looking success.
//
// SendKeysAndEnterChecked inserts a verification hook between the paste and
// the Enter — the only point after which the composer could hold a truncated
// fragment without it having been submitted yet. These tests exercise the
// hook in isolation, without a real tmux server, using the same
// recordKeySender seam issue #1264's tests use.
package tmux

import (
	"errors"
	"testing"
)

func TestSendKeysAndEnterChecked_FailedCheckWithholdsEnter(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "issue2079-withhold"}
	wantErr := errors.New("prompt truncated in transit")
	check := func(pane string, capErr error) (bool, error) {
		return false, wantErr
	}
	capture := func() (string, error) { return "❯ [Pasted text #1 +1 lines]\n", nil }

	err := s.SendKeysAndEnterChecked("line one\nline two\nline three", capture, check)
	if !errors.Is(err, wantErr) {
		t.Fatalf("want wrapped %v, got %v", wantErr, err)
	}

	for _, c := range *calls {
		if sentKey(c) == "Enter" {
			t.Fatalf("Enter must never be sent when the check fails; calls: %v", *calls)
		}
	}
}

func TestSendKeysAndEnterChecked_FailedCheckNoErrorStillWithholds(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "issue2079-withhold-generic"}
	check := func(pane string, capErr error) (bool, error) { return false, nil }
	capture := func() (string, error) { return "", nil }

	err := s.SendKeysAndEnterChecked("body", capture, check)
	if err == nil {
		t.Fatal("want a generic withheld-submit error when check fails without one of its own")
	}
	for _, c := range *calls {
		if sentKey(c) == "Enter" {
			t.Fatalf("Enter must never be sent when the check fails; calls: %v", *calls)
		}
	}
}

func TestSendKeysAndEnterChecked_PassedCheckSendsEnter(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "issue2079-pass"}
	check := func(pane string, capErr error) (bool, error) { return true, nil }
	capture := func() (string, error) { return "❯ [Pasted text #1 +3 lines]\n", nil }

	if err := s.SendKeysAndEnterChecked("line one\nline two\nline three", capture, check); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, c := range *calls {
		if sentKey(c) == "Enter" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Enter must be sent when the check passes; calls: %v", *calls)
	}
}

// TestSendKeysAndEnterChecked_NilCheckMatchesSendKeysAndEnter guards the
// existing-caller contract: every one of SendKeysAndEnter's pre-#2079 callers
// must see byte-identical behavior, so a nil check must never call capture
// and must always press Enter.
func TestSendKeysAndEnterChecked_NilCheckMatchesSendKeysAndEnter(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "issue2079-nil-check"}
	captureCalled := false
	capture := func() (string, error) {
		captureCalled = true
		return "", nil
	}

	if err := s.SendKeysAndEnterChecked("hello", capture, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captureCalled {
		t.Fatal("capture must not be called when check is nil")
	}
	found := false
	for _, c := range *calls {
		if sentKey(c) == "Enter" {
			found = true
		}
	}
	if !found {
		t.Fatal("Enter must still be sent when check is nil")
	}
}

// TestSendKeysAndEnterChecked_NilCaptureWithCheckIsHandled covers the caller
// mistake of passing a check without a capture function: it must surface as a
// (non-panicking) capture error routed through check rather than a nil
// dereference, and check still governs whether Enter is withheld.
func TestSendKeysAndEnterChecked_NilCaptureWithCheckIsHandled(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "issue2079-nil-capture"}
	var sawCaptureErr bool
	check := func(pane string, capErr error) (bool, error) {
		sawCaptureErr = capErr != nil
		return true, nil // treat capture failure as unknown, not unsafe
	}

	if err := s.SendKeysAndEnterChecked("hello", nil, check); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawCaptureErr {
		t.Fatal("check must observe a non-nil captureErr when no capture function was provided")
	}
	found := false
	for _, c := range *calls {
		if sentKey(c) == "Enter" {
			found = true
		}
	}
	if !found {
		t.Fatal("Enter must be sent: the check chose to proceed despite the capture error")
	}
}
