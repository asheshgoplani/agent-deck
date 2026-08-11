package session

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestDrainForStopHookRuntimeQueue(t *testing.T) {
	t.Run("runtime only", func(t *testing.T) {
		inboxTestHome(t)
		const id = "stop-runtime-only"
		if _, err := EnqueueRuntimeMessage(id, "run the verification"); err != nil {
			t.Fatal(err)
		}

		dec, blocked, err := DrainForStopHook(id, false)
		if err != nil {
			t.Fatal(err)
		}
		if !blocked || dec.Decision != "block" || runtimeAckToken(dec) == "" {
			t.Fatalf("decision = %+v, blocked = %v", dec, blocked)
		}
		if !strings.Contains(dec.Reason, "run the verification") {
			t.Fatalf("reason = %q", dec.Reason)
		}
		if strings.Contains(dec.Reason, "Child session(s) completed") {
			t.Fatalf("runtime-only reason contains empty inbox section: %q", dec.Reason)
		}
		if !RuntimeQueueHasPending(id) {
			t.Fatal("staging consumed the active runtime queue")
		}
	})

	t.Run("inbox and runtime share one decision and budget", func(t *testing.T) {
		inboxTestHome(t)
		const id = "stop-both"
		commitForStop(t, id, "child-one", "turn-one")
		if _, err := EnqueueRuntimeMessage(id, "runtime-one"); err != nil {
			t.Fatal(err)
		}

		dec, blocked, err := DrainForStopHook(id, false)
		if err != nil {
			t.Fatal(err)
		}
		if !blocked || runtimeAckToken(dec) == "" {
			t.Fatalf("decision = %+v, blocked = %v", dec, blocked)
		}
		inboxAt, runtimeAt := strings.Index(dec.Reason, "child-one"), strings.Index(dec.Reason, "runtime-one")
		if inboxAt < 0 || runtimeAt < 0 || inboxAt >= runtimeAt {
			t.Fatalf("sources not ordered inbox-first: %q", dec.Reason)
		}
		if !strings.Contains(dec.Reason, "\n\n## Queued runtime messages") {
			t.Fatalf("sources not separated by one blank line: %q", dec.Reason)
		}
		if got := loadStopBlockCountLocked(id); got != 1 {
			t.Fatalf("budget count = %d, want 1", got)
		}
	})
}

func runtimeAckToken(dec StopHookDecision) string {
	field := reflect.ValueOf(dec).FieldByName("RuntimeQueueAckToken")
	if !field.IsValid() || field.Kind() != reflect.String {
		return ""
	}
	return field.String()
}

func TestDrainForStopHookRuntimeQueueGuards(t *testing.T) {
	tests := []struct {
		name   string
		active bool
		setup  func(*testing.T, string)
	}{
		{name: "neither"},
		{name: "inactive hook without pending", active: false},
		{name: "discarded runtime data", setup: func(t *testing.T, id string) {
			if _, err := EnqueueRuntimeMessage(id, "discard me"); err != nil {
				t.Fatal(err)
			}
			if err := DiscardRuntimeQueue(id); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "exhausted budget", active: true, setup: func(t *testing.T, id string) {
			if _, err := EnqueueRuntimeMessage(id, "preserve me"); err != nil {
				t.Fatal(err)
			}
			if err := saveStopBlockCountLocked(id, MaxStopHookBlocks); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inboxTestHome(t)
			id := "guard-" + strings.ReplaceAll(tc.name, " ", "-")
			if tc.setup != nil {
				tc.setup(t, id)
			}
			dec, blocked, err := DrainForStopHook(id, tc.active)
			if err != nil {
				t.Fatal(err)
			}
			if blocked || dec.Decision != "" {
				t.Fatalf("decision = %+v, blocked = %v", dec, blocked)
			}
			if tc.name == "exhausted budget" && !RuntimeQueueHasPending(id) {
				t.Fatal("exhausted budget consumed runtime queue")
			}
			if tc.name != "exhausted budget" {
				if _, err := os.Stat(stopBlocksPathFor(id)); !os.IsNotExist(err) {
					t.Fatalf("fast path wrote budget ledger: %v", err)
				}
			}
		})
	}
}
