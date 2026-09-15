package procowner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An unverified-spawn marker is what a spawn leaves behind when the pane process
// existed but its identity could not be read. It owns nothing, it can never be
// signalled, and it blocks the next spawn until an operator resolves it — the
// alternative is a session that may have left a tree behind with no record of
// it, which is exactly the shape #1873 reports.

func unverifiedMarker(pid int, boot string) *Receipt {
	return &Receipt{
		Version:    ReceiptVersion,
		InstanceID: "i",
		Generation: 1,
		State:      StateUnverifiedSpawn,
		Provider:   ProviderLinuxProc,
		BootID:     boot,
		CreatedAt:  1,
		Leader:     Member{PID: pid, Role: RoleLeader},
		Note:       "identity unreadable at spawn",
	}
}

func TestUnverifiedSpawn_ValidatesWithoutAStartIdentity(t *testing.T) {
	require.NoError(t, unverifiedMarker(4242, "boot-1").Validate())

	data, err := Encode(unverifiedMarker(4242, "boot-1"))
	require.NoError(t, err)
	decoded, err := Decode(data)
	require.NoError(t, err)
	assert.Equal(t, StateUnverifiedSpawn, decoded.State)
	assert.Equal(t, 4242, decoded.Leader.PID)

	// pid 0 is the "pane pid itself unreadable" marker and is valid; pid 1 or a
	// negative pid is corrupt.
	require.NoError(t, unverifiedMarker(0, "boot-1").Validate())
	assert.Equal(t, VerdictUnknown, Verify(newFakeProber(), unverifiedMarker(0, "boot-1")).Verdict)
	require.ErrorIs(t, unverifiedMarker(1, "boot-1").Validate(), ErrCorruptReceipt)
	require.ErrorIs(t, unverifiedMarker(-4, "boot-1").Validate(), ErrCorruptReceipt)
	// And a marker never carries members: nothing under it is attributable.
	withMembers := unverifiedMarker(4242, "boot-1")
	withMembers.Members = []Member{{PID: 5, StartID: "1", UID: 1}}
	require.ErrorIs(t, withMembers.Validate(), ErrCorruptReceipt)
	// A live receipt still needs the full identity.
	live := unverifiedMarker(4242, "boot-1")
	live.State = StateLive
	require.ErrorIs(t, live.Validate(), ErrCorruptReceipt)
}

func TestUnverifiedSpawn_VerifiesUnknownAndReapsNothing(t *testing.T) {
	p := newFakeProber()
	p.add(4242, 1, "5000", 1000) // whoever holds the pid now is not provably ours
	sig := newRecordingSignaler()

	report := Verify(p, unverifiedMarker(4242, "boot-1"))
	assert.Equal(t, VerdictUnknown, report.Verdict)
	assert.Contains(t, report.Reason, "4242")
	require.Len(t, report.Members, 1)
	assert.Equal(t, StateUnknown, report.Members[0].State)
	assert.Empty(t, report.Owned(), "a marker owns nothing")

	reap := Reap(p, sig, unverifiedMarker(4242, "boot-1"), fastReapOptions())
	assert.Equal(t, VerdictUnknown, reap.Verdict)
	assert.Empty(t, sig.calls(), "a marker authorises no signal")
}

func TestUnverifiedSpawn_FromAPreviousBootIsProvablyDead(t *testing.T) {
	p := newFakeProber()
	p.add(4242, 1, "5000", 1000)
	report := Verify(p, unverifiedMarker(4242, "boot-before-reboot"))
	assert.Equal(t, VerdictClear, report.Verdict, "nothing spawned before a reboot can still be running")

	// A marker whose boot could not be read stays unknown: it cannot be proven
	// dead, so it waits for the operator.
	report = Verify(p, unverifiedMarker(4242, ""))
	assert.Equal(t, VerdictUnknown, report.Verdict)
}
