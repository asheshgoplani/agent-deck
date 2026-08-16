//go:build linux

package procowner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseProcStat is where a whole class of silent misidentification lives: get
// the comm split wrong and every later field shifts, so the number compared as
// a "start time" is somebody else's field entirely.
func TestParseProcStat_CommWithSpacesAndParens(t *testing.T) {
	fields := make([]string, 0, 52)
	fields = append(fields, "S", "4242", "777", "777", "0", "-1", "4194304")
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, "987654321") // field 22: starttime
	line := "1234 (weird (name) with spaces) "
	for _, f := range fields {
		line += f + " "
	}

	info, err := parseProcStat([]byte(line))
	require.NoError(t, err)
	assert.Equal(t, 1234, info.PID)
	assert.Equal(t, "weird (name) with spaces", info.Comm)
	assert.Equal(t, 4242, info.PPID)
	assert.Equal(t, 777, info.PGID)
	assert.Equal(t, "987654321", info.StartID)
}

func TestParseProcStat_RejectsTruncatedAndMalformed(t *testing.T) {
	for name, line := range map[string]string{
		"empty":               "",
		"no parens":           "1234 sh S 1 1 1",
		"truncated":           "1234 (sh) S 1 1",
		"bad pid":             statLine("nope", "5000"),
		"non numeric start":   statLine("1234", "not-a-number"),
		"negative field slot": statLine("1234", ""),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseProcStat([]byte(line))
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrUnreadable))
		})
	}
}

// An unreadable /proc entry must be "unknown", never "gone": the difference
// decides whether a receipt is cleared or a restart is refused.
func TestLinuxProber_UnreadableEntryIsNotDeath(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "4242"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "4242", "stat"), []byte("garbage"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sys", "kernel", "random"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sys", "kernel", "random", "boot_id"),
		[]byte("fixture-boot\n"), 0o600))

	restore := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = restore })

	p := LinuxProber{}
	_, err := p.Inspect(4242)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnreadable))
	assert.False(t, errors.Is(err, ErrNoProcess))

	// An absent entry, by contrast, is proof of death.
	_, err = p.Inspect(4243)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoProcess))

	boot, err := p.BootID()
	require.NoError(t, err)
	assert.Equal(t, "fixture-boot", boot)
}

func TestLinuxProber_MissingBootIDIsUnreadable(t *testing.T) {
	restore := procRoot
	procRoot = t.TempDir()
	t.Cleanup(func() { procRoot = restore })

	_, err := LinuxProber{}.BootID()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnreadable))
}

// The end-to-end reality check: claim a receipt for a real process, verify it,
// reap it by identity, and confirm the receipt clears. Every pid this test
// creates is its own, and it kills only those.
func TestLinuxProber_RealProcessLifecycle(t *testing.T) {
	p := LinuxProber{}
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	receipt, err := Claim(p, ClaimInput{InstanceID: "inst-real", Generation: 1, PanePID: pid})
	require.NoError(t, err)
	assert.Equal(t, os.Getuid(), receipt.Leader.UID)
	assert.NotEmpty(t, receipt.Leader.StartID)

	report := Verify(p, receipt)
	require.Equal(t, VerdictOwned, report.Verdict, report.Describe())

	// A receipt with the right pid but a wrong start identity is a stranger:
	// this is the recycled-pid case against a genuinely live process.
	impostor := liveReceipt("inst-real", Member{
		PID:     pid,
		StartID: bumpStart(t, receipt.Leader.StartID),
		UID:     receipt.Leader.UID,
		Role:    RoleLeader,
	})
	impostor.BootID = receipt.BootID
	sig := newRecordingSignaler()
	impostorReport := Reap(p, sig, impostor, ReapOptions{TermGrace: 100 * time.Millisecond, KillGrace: 100 * time.Millisecond})
	assert.Empty(t, sig.calls(), "a live process with a different start identity is never signalled")
	assert.Equal(t, VerdictClear, impostorReport.Verdict)
	require.NoError(t, syscall.Kill(pid, 0), "the impostor check must not have killed the real process")

	// Now reap the real receipt.
	realReport := Reap(p, OSSignaler{}, receipt, ReapOptions{TermGrace: 3 * time.Second, KillGrace: 3 * time.Second})
	require.Equal(t, VerdictClear, realReport.Verdict, realReport.Describe())
	reaped = true
	_ = cmd.Wait()

	after := Verify(p, receipt)
	assert.Equal(t, VerdictClear, after.Verdict)
}

// A real parent/child tree: attribution must record the child while it is a
// live descendant, and the child must remain identifiable after its parent has
// gone — which is the whole point of recording it at spawn.
func TestLinuxProber_AttributesRealDescendants(t *testing.T) {
	p := LinuxProber{}
	// A shell that forks a grandchild and stays alive.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	receipt, err := Claim(p, ClaimInput{InstanceID: "inst-tree", Generation: 1, PanePID: pid})
	require.NoError(t, err)

	var added []Member
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		added, err = Attribute(p, receipt, nil)
		require.NoError(t, err)
		if len(receipt.Members) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.NotEmpty(t, receipt.Members, "the forked child must be attributable while its parent lives")

	for _, m := range receipt.Members {
		assert.NotEmpty(t, m.StartID, "every member carries its own start identity")
		start, cmpErr := CompareStart(p, m.StartID, receipt.Leader.StartID)
		require.NoError(t, cmpErr)
		assert.GreaterOrEqual(t, start, 0, "a descendant cannot predate its leader")
	}

	// Reap the whole recorded tree by identity, then confirm nothing is left.
	report := Reap(p, OSSignaler{}, receipt, ReapOptions{TermGrace: 3 * time.Second, KillGrace: 3 * time.Second})
	require.Equal(t, VerdictClear, report.Verdict, report.Describe())
	_ = cmd.Wait()
	for _, m := range receipt.All() {
		assert.NotEqual(t, StateOwned, VerifyMember(p, m).State, "member %s survived the reap", m)
	}
	_ = added
}

func bumpStart(t *testing.T, start string) string {
	t.Helper()
	v, err := strconv.ParseUint(start, 10, 64)
	require.NoError(t, err)
	return fmt.Sprint(v + 1)
}

// statLine builds a /proc/<pid>/stat line with `start` in field 22, so a test
// fixture cannot silently drift out of alignment by miscounting spaces.
func statLine(pid, start string) string {
	fields := make([]string, 20) // fields 3..22
	for i := range fields {
		fields[i] = "1"
	}
	fields[0] = "S"
	fields[19] = start
	return pid + " (sh) " + strings.Join(fields, " ")
}
