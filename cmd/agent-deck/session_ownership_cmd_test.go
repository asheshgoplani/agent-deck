package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/procowner"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionOwnership_RemoteSessionRunsOnOwningHost(t *testing.T) {
	t.Skip("ownership receipts are host-local; remote recovery must execute on the remote agent, not inspect controller PIDs")
}

// The recovery surface is the only thing an operator sees when a restart is
// refused, so it has to name the processes and the exact command that resolves
// them. A verdict with no pids is a dead end.

func TestRenderOwnershipStatus_NamesSurvivorsAndTheRecoveryCommand(t *testing.T) {
	inst := session.NewInstance("owner-render", t.TempDir())
	survivor := procowner.Member{PID: 4242, StartID: "99887766", UID: 1000, Role: procowner.RoleDescendant}
	status := session.OwnershipStatus{
		InstanceID: inst.ID,
		Receipt: &procowner.Receipt{
			Version:    procowner.ReceiptVersion,
			InstanceID: inst.ID,
			Generation: 2,
			State:      procowner.StateLive,
			Provider:   procowner.ProviderLinuxProc,
			BootID:     "boot",
			Leader:     procowner.Member{PID: 4240, StartID: "99887700", UID: 1000, Role: procowner.RoleLeader},
			Members:    []procowner.Member{survivor},
		},
		Report: procowner.Report{
			Verdict: procowner.VerdictOwned,
			Reason:  "1 owned process(es) are still alive",
			Members: []procowner.MemberStatus{{Member: survivor, State: procowner.StateOwned}},
		},
		Survivors: []procowner.Member{survivor},
	}

	out := renderOwnershipStatus(inst, status)
	assert.Contains(t, out, "generation 2")
	assert.Contains(t, out, "pid=4242")
	assert.Contains(t, out, "no live pane accounts for this receipt")
	assert.Contains(t, out, "agent-deck session ownership reconcile "+inst.ID)
	assert.False(t, status.Admissible())
}

func TestRenderOwnershipStatus_UnreadableReceiptSaysSo(t *testing.T) {
	inst := session.NewInstance("owner-render-corrupt", t.TempDir())
	status := session.OwnershipStatus{
		InstanceID: inst.ID,
		LoadErr:    errors.New("ownership receipt is unreadable or incomplete: unexpected end of JSON input"),
	}

	out := renderOwnershipStatus(inst, status)
	assert.Contains(t, out, "UNREADABLE")
	assert.Contains(t, out, "nothing will be signalled")
	assert.False(t, status.Admissible(), "an unreadable receipt is never admissible")
}

func TestRenderOwnershipStatus_NoReceipt(t *testing.T) {
	inst := session.NewInstance("owner-render-none", t.TempDir())
	status := session.OwnershipStatus{InstanceID: inst.ID}

	out := renderOwnershipStatus(inst, status)
	assert.Contains(t, out, "owns no recorded processes")
	assert.True(t, status.Admissible())
	assert.NotContains(t, strings.ToLower(out), "reconcile")
}

func TestOwnershipPayload_CarriesTheMachineReadableFacts(t *testing.T) {
	leader := procowner.Member{PID: 4240, StartID: "99887700", UID: 1000, Role: procowner.RoleLeader}
	status := session.OwnershipStatus{
		InstanceID: "inst-json",
		Receipt: &procowner.Receipt{
			Version:    procowner.ReceiptVersion,
			InstanceID: "inst-json",
			Generation: 7,
			State:      procowner.StateLive,
			Provider:   procowner.ProviderLinuxProc,
			BootID:     "boot",
			Leader:     leader,
		},
		Report: procowner.Report{Verdict: procowner.VerdictClear, Reason: "every recorded process is gone"},
	}

	payload := ownershipPayload(status)
	assert.Equal(t, "inst-json", payload["instance_id"])
	assert.Equal(t, uint64(7), payload["generation"])
	assert.Equal(t, string(procowner.VerdictClear), payload["verdict"])
	assert.Equal(t, true, payload["admissible"])
	leaderPayload, ok := payload["leader"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 4240, leaderPayload["pid"])
	assert.Equal(t, "99887700", leaderPayload["start_id"])
}

func TestReapOutcomePayload(t *testing.T) {
	report := procowner.ReapReport{
		Verdict: procowner.VerdictClear,
		Reason:  "every recorded process is gone",
		Outcomes: []procowner.ReapOutcome{{
			Member:  procowner.Member{PID: 4242, StartID: "99887766"},
			Outcome: procowner.OutcomeReaped,
			Detail:  "exited after terminated",
		}},
	}
	payload := reapOutcomePayload(report)
	require.Len(t, payload, 1)
	assert.Equal(t, 4242, payload[0]["pid"])
	assert.Equal(t, procowner.OutcomeReaped, payload[0]["outcome"])
}

// The confirmation gate on reconcile is scoped to the one case where it is a
// confirmation and not a speed bump: the receipt's leader is the live pane, so
// reconciling stops a running session. The escaped-tree case — the one the
// refusal message tells the operator to run — has no live pane by definition,
// and requiring a flag there would train people to pass --yes without reading.
func TestReconcileConfirmation_ScopedToALivePane(t *testing.T) {
	live := session.OwnershipStatus{
		InstanceID:   "inst-live",
		PaneAttached: true,
		Receipt:      &procowner.Receipt{Version: procowner.ReceiptVersion, InstanceID: "inst-live"},
	}
	escaped := session.OwnershipStatus{
		InstanceID:   "inst-escaped",
		PaneAttached: false,
		Survivors:    []procowner.Member{{PID: 4242, StartID: "5000"}},
		Receipt:      &procowner.Receipt{Version: procowner.ReceiptVersion, InstanceID: "inst-escaped"},
	}

	assert.True(t, live.PaneAttached, "a live pane must require --yes")
	assert.False(t, escaped.PaneAttached, "an escaped tree must not")
	assert.False(t, escaped.Admissible(), "and it must still block a restart until reconciled")
}
