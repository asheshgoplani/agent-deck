package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// PR 3123 must not change what the send queue retries or what the Codex
// acceptance marker treats as a definitive non-delivery. The second review
// proved that adding a "typed_withdrawn" verdict to the queue's not-sent set
// made a submitted message retryable (delivered twice). These tests pin the
// base (f7017710) behaviour with string literals, so they compile on any
// tree and fail if a new verdict is ever added to either set.

// baseNotSentDeliveries is notSentDeliveries at f7017710
// (`git show f7017710:cmd/agent-deck/session_send_queue_cmd.go`).
var baseNotSentDeliveries = []string{"acceptance_refused", "composer_blocked", "target_busy"}

// allDeliveryVerdicts is every delivery a `session send` can report, plus the
// removed "typed_withdrawn", which must never be retryable again.
var allDeliveryVerdicts = []string{
	"submitted", "unverified", "delivered", "line_too_long", "menu_open", "pane_gone",
	"typed_not_submitted", "no_evidence", "send_failed", "composer_blocked", "target_busy",
	"acceptance_refused", "queued", "queued_socket", "socket_write_failed", "typed_withdrawn",
}

func TestRetrySemantics_NotSentDeliveriesEqualBase(t *testing.T) {
	var got []string
	for k, v := range notSentDeliveries {
		if v {
			got = append(got, k)
		}
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(baseNotSentDeliveries) {
		t.Fatalf("notSentDeliveries = %v, want exactly the base set %v: a verdict that may follow typing must never be retried (PR 3123 second review, finding 1: double delivery)", got, baseNotSentDeliveries)
	}
}

func TestRetrySemantics_ClassifyChildPerDeliveryMatchesBase(t *testing.T) {
	base := map[string]bool{}
	for _, d := range baseNotSentDeliveries {
		base[d] = true
	}
	for _, d := range allDeliveryVerdicts {
		got, _ := classifyChild(map[string]interface{}{"success": false, "delivery": d, "error": "message not sent"}, 1)
		want := childUnknown
		if base[d] {
			want = childNotSent
		}
		if got != want {
			t.Errorf("classifyChild(delivery=%q) = %v, want %v (base behaviour)", d, got, want)
		}
	}
}

// The Codex acceptance marker is cleared only for the base's definitive
// non-deliveries; every other verdict, "typed_withdrawn" included, keeps it.
func TestRetrySemantics_CodexMarkerClearedOnlyForBaseVerdicts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	cleared := map[string]bool{"line_too_long": true, "composer_blocked": true, "target_busy": true}
	for n, d := range allDeliveryVerdicts {
		t.Run(d, func(t *testing.T) {
			sessionID := fmt.Sprintf("thread-retry-semantics-%d", n)
			path := filepath.Join(home, "codex", "sessions", "2026", "11", "12", "rollout-test-"+sessionID+".jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			inst := &session.Instance{ID: "instance-" + sessionID, Tool: "codex", CodexSessionID: sessionID}
			guard, err := acquireCodexAcceptanceGuard(inst, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := guard.Prepare(inst.ID, time.Now()); err != nil {
				guard.Release()
				t.Fatal(err)
			}
			if err := guard.RecordTransportOutcome(d, false, time.Now()); err != nil {
				guard.Release()
				t.Fatal(err)
			}
			guard.Release()
			_, reconcileErr := session.ReconcileCodexSubmissionMarker(inst.ID, sessionID, "", nil)
			if cleared[d] && reconcileErr != nil {
				t.Fatalf("base cleared the marker for %q; now retained: %v", d, reconcileErr)
			}
			if !cleared[d] && reconcileErr == nil {
				t.Fatalf("delivery %q cleared the Codex marker; the base kept it (ambiguous)", d)
			}
		})
	}
}
