package main

import (
	"strings"
	"testing"
)

func TestApplyAssertDoneAppendsSentinel(t *testing.T) {
	got := applyAssertDone("do the thing", true)
	if !strings.Contains(got, "===AGENTDECK_DONE===") {
		t.Fatalf("expected sentinel instruction appended, got: %q", got)
	}
	if !strings.HasPrefix(got, "do the thing") {
		t.Fatalf("expected original message preserved, got: %q", got)
	}
}

func TestApplyAssertDoneDisabledIsNoop(t *testing.T) {
	if got := applyAssertDone("msg", false); got != "msg" {
		t.Fatalf("expected no-op when disabled, got: %q", got)
	}
}

func TestApplyAssertDoneEmptyMessageStaysEmpty(t *testing.T) {
	if got := applyAssertDone("", true); got != "" {
		t.Fatalf("expected empty message untouched (nothing to attach to), got: %q", got)
	}
}
