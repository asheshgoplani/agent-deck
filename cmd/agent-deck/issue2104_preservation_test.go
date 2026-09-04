package main

import (
	"errors"
	"testing"
)

func TestIssue2104RefusalNeverTypesOrInterrupts(t *testing.T) {
	for _, noWait := range []bool{false, true} {
		mock := &guardedSendMock{draftPane: claudeComposer("user draft")}
		res, err := executeSend(mock, "claude", "automation", noWait, testGuardTuning(sendRetryOptions{maxRetries: 40, verifyDelivery: true}))
		if err == nil || res.jsonFields()["submitted"] != false {
			t.Fatalf("expected unsent refusal: %+v %v", res, err)
		}
		if mock.ctrlCCalls != 0 || mock.sendKeysCalls != 0 || mock.enterCalls != 0 || mock.chunkedCalls != 0 {
			t.Fatalf("refusal mutated pane: %+v", mock)
		}
		if _, ok := res.jsonFields()["saved_draft"]; ok {
			t.Fatal("preserved draft should not be copied into response")
		}
	}
}

func TestIssue2104FailedCaptureRefusesBeforeTyping(t *testing.T) {
	mock := &mockSendRetryTarget{panes: []string{""}, paneErrs: []error{errors.New("capture unavailable")}}
	res, err := executeSend(mock, "claude", "automation", false, testGuardTuning(sendRetryOptions{verifyDelivery: true}))
	if err == nil || res.jsonFields()["submitted"] != false {
		t.Fatalf("capture failure authorized delivery: %+v %v", res, err)
	}
	if mock.sendKeysCalls != 0 || mock.sendEnterCalls != 0 || mock.sendCtrlCCalls != 0 {
		t.Fatalf("capture failure mutated pane: %+v", mock)
	}
}
