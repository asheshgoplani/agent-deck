package ui

import (
	"errors"
	"testing"
)

func TestClearConductorAndHeartbeat_StopsOnClearFailure(t *testing.T) {
	calls := 0
	err := clearConductorAndHeartbeat(func(body string) error {
		calls++
		if body != "/clear" {
			t.Fatalf("unexpected body %q", body)
		}
		return errors.New("clear not delivered")
	}, "heartbeat", 0)
	if err == nil || calls != 1 {
		t.Fatalf("clear failure allowed heartbeat: %d, %v", calls, err)
	}
}

func TestClearConductorAndHeartbeat_RechecksNewDraft(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "new draft"}[occupied], func(t *testing.T) {
			first := &guardedFakePane{draftPane: emptyComposer(), postSendCaptures: []string{emptyComposer()}}
			second := &guardedFakePane{draftPane: emptyComposer(), postSendCaptures: []string{emptyComposer()}}
			if occupied {
				second.draftPane = composerWith("new operator draft")
			}
			calls := 0
			err := clearConductorAndHeartbeat(func(body string) error {
				calls++
				p := first
				want := "/clear"
				if calls == 2 {
					p = second
					want = "heartbeat"
				}
				if body != want {
					t.Fatalf("stage%d body = %q", calls, body)
				}
				return deliverToConductorPaneGuarded(p, body, testConductorGuardOpts(), 4, 0)
			}, "heartbeat", 0)
			if calls != 2 || first.sendKeysCalls != 1 {
				t.Fatalf("clear sequence failed: calls%d first%+v", calls, first)
			}
			if occupied {
				if err == nil || second.sendKeysCalls != 0 || second.enterCalls != 0 || second.ctrlCCalls != 0 || second.chunkedCalls != 0 {
					t.Fatalf("new draft not preserved: %+v err%v", second, err)
				}
			} else if err != nil || second.sendKeysCalls != 1 {
				t.Fatalf("heartbeat not delivered: %+v %v", second, err)
			}
		})
	}
}
