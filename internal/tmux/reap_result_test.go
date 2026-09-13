package tmux

import (
	"errors"
	"strings"
	"testing"
)

func TestKillAndWait_UnverifiedDeathRemainsFailureAfterSessionDisappears(t *testing.T) {
	reapErr := processReapResult([]int{123})
	if reapErr == nil || !strings.Contains(reapErr.Error(), "death unverified") {
		t.Fatalf("expected explicit unverified-death failure, got %v", reapErr)
	}
	for _, exists := range []bool{false, true} {
		if err := killAndWaitResult(errors.New("kill failed"), reapErr, exists); !errors.Is(err, reapErr) {
			t.Fatalf("exists=%v: lost reap failure: %v", exists, err)
		}
	}
	if err := killAndWaitResult(nil, reapErr, true); !errors.Is(err, reapErr) {
		t.Fatalf("successful tmux kill hid reap failure: %v", err)
	}
}

func TestKillAndWait_ConfirmedDeathAllowsAlreadyAbsentSession(t *testing.T) {
	if err := processReapResult(nil); err != nil {
		t.Fatal(err)
	}
	if err := killAndWaitResult(errors.New("session absent"), nil, false); err != nil {
		t.Fatalf("confirmed death and absent session should succeed: %v", err)
	}
	killErr := errors.New("session still exists")
	if err := killAndWaitResult(killErr, nil, true); !errors.Is(err, killErr) {
		t.Fatalf("live-session kill failure lost: %v", err)
	}
}
