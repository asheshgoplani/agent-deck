package main

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The source is archived (superseded) before the final journal write of a
// ready cross-harness target. When that write fails the operation is
// recovery-required, but the archive HAS happened: every JSON shape the
// command emits must say so, including the failure payload.
func TestCrossHarnessSwitchPayloads_ReportCommittedSourceArchive(t *testing.T) {
	source := &session.Instance{ID: "src", SupersededBy: "tgt"}
	ready := &session.CrossHarnessSwitchResult{Target: &session.Instance{ID: "tgt"}, TargetCreated: true, TargetReady: true}
	pending := &session.CrossHarnessSwitchResult{Target: &session.Instance{ID: "tgt"}, TargetCreated: true, Pending: true}

	failed := crossHarnessFailurePayload(source, ready, session.ErrCrossHarnessRecoveryRequired)
	if failed["source_archived"] != true || failed["source_superseded_by"] != "tgt" || failed["recovery_required"] != true {
		t.Fatalf("recovery-required failure must report the committed archive: %v", failed)
	}
	failedBefore := crossHarnessFailurePayload(source, pending, errors.New("launch failed"))
	if failedBefore["source_archived"] != false || failedBefore["source_superseded_by"] != "" {
		t.Fatalf("a failure before readiness leaves the source unchanged: %v", failedBefore)
	}
	if p := crossHarnessPendingPayload(source, pending); p["source_archived"] != false {
		t.Fatalf("pending payload: %v", p)
	}
	if p := crossHarnessSuccessPayload(source, ready); p["source_archived"] != true || p["source_superseded_by"] != "tgt" {
		t.Fatalf("success payload: %v", p)
	}
}
