package session

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/procowner"
)

// Concurrency around the receipt, from the review of #1961.
//
// Both cases below are the same shape: a guard that is narrower than the
// invariant it claims. A cancellation that stops new work but leaves work
// already in flight armed is not cancellation, and a compare-and-swap held only
// by a process-local mutex is not a compare-and-swap — a second agent-deck
// process validates the same stale state and proceeds. Atomic replacement does
// not help with either: it makes the write indivisible, not the decision.

// ownershipChildEnv makes the test binary act as a second agent-deck process.
// The value is "<dir>|<instance>|<pid>|<mode>".
const ownershipChildEnv = "AGENTDECK_TEST_OWNERSHIP_CHILD"

// runOwnershipReceiptChild is dispatched from TestMain when the env var is set.
// It performs ONE receipt operation against the same store the parent uses and
// exits: a real second process, which is the only way to test a lock whose
// whole purpose is to span two of them.
func runOwnershipReceiptChild(payload string) int {
	parts := strings.Split(payload, "|")
	if len(parts) != 4 {
		fmt.Fprintf(os.Stderr, "ownership child: bad payload %q\n", payload)
		return 2
	}
	dir, instanceID, mode := parts[0], parts[1], parts[3]
	pid, err := strconv.Atoi(parts[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ownership child: bad pid %q\n", parts[2])
		return 2
	}
	store := ownershipStoreAt(dir)

	switch mode {
	case "hold":
		// Take the lock and never release it: the parent kills this process to
		// prove the kernel drops the flock when its holder dies. Holding it
		// here, in the process the parent kills, is deliberate — a holder that
		// spawns a grandchild would leak the fd (and the process) into it and
		// prove nothing.
		path, pathErr := store.Path(instanceID)
		if pathErr != nil {
			fmt.Fprintf(os.Stderr, "ownership child: %v\n", pathErr)
			return 2
		}
		release, lockErr := ownershipReceiptLock(path)
		if lockErr != nil {
			fmt.Fprintf(os.Stderr, "ownership child: %v\n", lockErr)
			return 2
		}
		defer release()
		fmt.Println("held")
		time.Sleep(60 * time.Second)
		return 0
	case "append":
		// Read-modify-write: add one member that only this child knows about.
		// The sleep widens the window between the read and the write, which is
		// precisely the window a process-local mutex does not cover.
		err = store.Update(instanceID, func(current *procowner.Receipt) error {
			time.Sleep(150 * time.Millisecond)
			current.Members = append(current.Members, procowner.Member{
				PID:     pid,
				StartID: strconv.Itoa(pid),
				UID:     os.Getuid(),
				Role:    procowner.RoleDescendant,
			})
			return nil
		})
	default:
		fmt.Fprintf(os.Stderr, "ownership child: unknown mode %q\n", mode)
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ownership child: %v\n", err)
		return 1
	}
	return 0
}

// seedRaceReceipt writes a receipt with no members and returns it.
func seedRaceReceipt(t *testing.T, store *procowner.Store, instanceID string) *procowner.Receipt {
	t.Helper()
	receipt, err := store.Commit(instanceID, func(*procowner.Receipt) (*procowner.Receipt, error) {
		return &procowner.Receipt{
			Version:    procowner.ReceiptVersion,
			InstanceID: instanceID,
			Generation: 1,
			State:      procowner.StateLive,
			Provider:   "session_fake",
			BootID:     "boot-1",
			CreatedAt:  time.Now().Unix(),
			Leader: procowner.Member{
				PID: 4242, StartID: "5000", UID: os.Getuid(), Role: procowner.RoleLeader,
			},
		}, nil
	})
	require.NoError(t, err)
	return receipt
}

// TestIssue1873_CrossProcessReceiptUpdatesDoNotLoseEachOther is the two-process
// compare-and-swap test. Two agent-deck processes each add a member to the same
// receipt; both must survive.
//
// Against a process-local mutex this fails: the second process reads a receipt
// that predates the first process's write, then writes its own copy back, and
// the first member is silently gone — the loser of the race quietly wins.
func TestIssue1873_CrossProcessReceiptUpdatesDoNotLoseEachOther(t *testing.T) {
	dir := t.TempDir()
	instanceID := "race-cross-process"
	store := ownershipStoreAt(dir)
	seedRaceReceipt(t, store, instanceID)

	const children = 2
	var wg sync.WaitGroup
	results := make([]error, children)
	pids := []int{9001, 9002}
	for idx := 0; idx < children; idx++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			results[n] = runOwnershipChildProcess(t, dir, instanceID, pids[n], "append")
		}(idx)
	}
	wg.Wait()

	for n, err := range results {
		require.NoError(t, err, "child %d failed", n)
	}

	loaded, err := store.Load(instanceID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	got := map[int]bool{}
	for _, m := range loaded.Members {
		got[m.PID] = true
	}
	for _, pid := range pids {
		assert.True(t, got[pid],
			"member %d was lost: a second process read state the first had already invalidated (members=%v)",
			pid, loaded.Members)
	}
}

// TestIssue1873_LateUpdateAfterClearIsANoOp is the resurrection guard. An
// update that was already in flight when the receipt was cleared must not
// recreate it.
//
// A receipt that comes back from the dead is worse than no receipt: every later
// decision — may this restart be admitted, which pids may be signalled — trusts
// it, and it now describes processes that were reaped.
func TestIssue1873_LateUpdateAfterClearIsANoOp(t *testing.T) {
	dir := t.TempDir()
	instanceID := "race-late-update"
	store := ownershipStoreAt(dir)
	receipt := seedRaceReceipt(t, store, instanceID)

	// The teardown wins the race and clears the receipt.
	require.NoError(t, store.Clear(receipt))

	// The in-flight writer arrives afterwards holding the receipt it was
	// working on.
	err := store.Update(instanceID, func(current *procowner.Receipt) error {
		current.Members = append(current.Members, procowner.Member{
			PID: 9100, StartID: "9100", UID: os.Getuid(), Role: procowner.RoleDescendant,
		})
		return nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, procowner.ErrReceiptGone)

	loaded, loadErr := store.Load(instanceID)
	require.NoError(t, loadErr)
	assert.Nil(t, loaded, "a cleared receipt must stay cleared")
}

// TestIssue1873_CancellationDisarmsAnInFlightUpdate covers the ordering the
// previous test does not: cancellation landing WHILE the update is queued for
// the lock, rather than before it starts.
//
// The mutator's window check runs after the lock is won, so the write is
// dropped. Checking only before enqueueing would let a cancelled writer apply
// its work after the teardown that cancelled it.
func TestIssue1873_CancellationDisarmsAnInFlightUpdate(t *testing.T) {
	dir := t.TempDir()
	instanceID := "race-cancel-in-flight"
	store := ownershipStoreAt(dir)
	seedRaceReceipt(t, store, instanceID)

	cancelled := make(chan struct{})
	blocked := make(chan struct{})
	released := make(chan struct{})

	// Hold the lock so the update below has to queue for it.
	go func() {
		_ = store.Update(instanceID, func(*procowner.Receipt) error {
			close(blocked)
			<-released
			return procowner.ErrNoChange
		})
	}()
	<-blocked

	var updateErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		updateErr = store.Update(instanceID, func(current *procowner.Receipt) error {
			// By the time this runs, the cancellation below has already fired.
			select {
			case <-cancelled:
				return procowner.ErrWindowClosed
			default:
			}
			current.Members = append(current.Members, procowner.Member{
				PID: 9200, StartID: "9200", UID: os.Getuid(), Role: procowner.RoleDescendant,
			})
			return nil
		})
	}()

	// Cancel while the second update is parked on the lock, then let it through.
	time.Sleep(50 * time.Millisecond)
	close(cancelled)
	close(released)
	<-done

	require.Error(t, updateErr)
	assert.ErrorIs(t, updateErr, procowner.ErrWindowClosed)
	loaded, err := store.Load(instanceID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Empty(t, loaded.Members, "a cancelled writer must not apply its work after the fact")
}

// TestIssue1873_LockSurvivesAProcessThatDiesHoldingIt: a lock that a crashed
// holder keeps forever is a deadlock dressed as safety. The advisory flock is
// released by the kernel when the holding process dies, so the next acquirer
// proceeds rather than waiting on a corpse.
func TestIssue1873_LockSurvivesAProcessThatDiesHoldingIt(t *testing.T) {
	dir := t.TempDir()
	instanceID := "race-dead-holder"
	store := ownershipStoreAt(dir)
	seedRaceReceipt(t, store, instanceID)

	// A second process that takes the receipt lock and is then killed without
	// releasing it.
	holder := exec.Command(os.Args[0], "-test.run", "TestIssue1873_OwnershipChildEntrypoint")
	holder.Env = append(os.Environ(),
		fmt.Sprintf("%s=%s|%s|%d|hold", ownershipChildEnv, dir, instanceID, os.Getpid()))
	stdout, err := holder.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, holder.Start())
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = holder.Process.Kill()
		}
		_ = holder.Wait()
	})

	// Wait until the child reports it holds the lock, so the kill below is a
	// kill of a holder rather than a race with one.
	buf := make([]byte, len("held\n"))
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(string(buf), "held") && time.Now().Before(deadline) {
		n, readErr := stdout.Read(buf)
		if readErr != nil || n == 0 {
			break
		}
	}
	require.Contains(t, string(buf), "held", "child never reported holding the lock")

	// Kill the holder, then prove the store can still take the lock. If the
	// kernel did not release the flock on exit this would block until the
	// deadline below.
	require.NoError(t, holder.Process.Kill())
	killed = true
	_ = holder.Wait()

	done := make(chan error, 1)
	go func() {
		done <- store.Update(instanceID, func(current *procowner.Receipt) error {
			current.Note = "acquired after the holder died"
			return nil
		})
	}()
	select {
	case updateErr := <-done:
		require.NoError(t, updateErr)
	case <-time.After(10 * time.Second):
		t.Fatal("the receipt lock was never released after its holder died")
	}
}

// runOwnershipChildProcess re-executes this test binary as a second agent-deck
// process performing one receipt operation.
func runOwnershipChildProcess(t *testing.T, dir, instanceID string, pid int, mode string) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "TestIssue1873_OwnershipChildEntrypoint")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("%s=%s|%s|%d|%s", ownershipChildEnv, dir, instanceID, pid, mode))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("child %d: %w: %s", pid, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TestIssue1873_OwnershipChildEntrypoint exists so the child invocation has a
// test to name; the work happens in TestMain before any test runs.
func TestIssue1873_OwnershipChildEntrypoint(t *testing.T) {
	if os.Getenv(ownershipChildEnv) == "" {
		t.Skip("child entrypoint: only meaningful when re-executed by the race tests")
	}
}

// TestIssue1873_GenerationResetDoesNotLetAStaleWriterWin covers the shape the
// two required tests do not: the generation counter RESETTING rather than
// advancing.
//
// A verifiably cleared receipt is an absent file, so the next spawn starts at
// generation 1 again — and a stale writer from the previous generation 1 finds
// a number that matches its own. The leader identity is what separates them,
// and it cannot collide: a different process cannot hold the same pid with the
// same start time.
func TestIssue1873_GenerationResetDoesNotLetAStaleWriterWin(t *testing.T) {
	dir := t.TempDir()
	instanceID := "race-generation-reset"
	store := ownershipStoreAt(dir)
	stale := seedRaceReceipt(t, store, instanceID) // generation 1, leader pid 4242

	// Teardown reaps and clears it; the next spawn claims generation 1 afresh.
	require.NoError(t, store.Clear(stale))
	replacement, err := store.Commit(instanceID, func(existing *procowner.Receipt) (*procowner.Receipt, error) {
		require.Nil(t, existing)
		next := *stale
		next.Leader = procowner.Member{PID: 5555, StartID: "6000", UID: os.Getuid(), Role: procowner.RoleLeader}
		next.Members = nil
		return &next, nil
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), replacement.Generation, "generations restart after a verified clear")

	// The stale writer's generation number matches. Its leader does not.
	updateErr := store.Update(instanceID, func(current *procowner.Receipt) error {
		return procowner.RequireGeneration(current, stale.Generation, stale.Leader)
	})
	require.Error(t, updateErr)
	assert.ErrorIs(t, updateErr, procowner.ErrGenerationConflict)

	loaded, err := store.Load(instanceID)
	require.NoError(t, err)
	assert.Equal(t, 5555, loaded.Leader.PID, "the replacement's receipt is untouched")
}

// TestIssue1873_UpdateReadsAfterAcquiringTheLock covers the last shape on the
// list: the receipt changing between the lock being acquired and the state
// being read.
//
// It cannot happen, and this is why: the load is INSIDE the critical section,
// so a writer queued behind another sees that writer's result rather than the
// state it observed before queueing. A load before the lock — the natural place
// to put it — would reintroduce exactly the stale-decision race the lock exists
// to close.
func TestIssue1873_UpdateReadsAfterAcquiringTheLock(t *testing.T) {
	dir := t.TempDir()
	instanceID := "race-read-after-lock"
	store := ownershipStoreAt(dir)
	seedRaceReceipt(t, store, instanceID)

	firstInside := make(chan struct{})
	letFirstFinish := make(chan struct{})
	go func() {
		_ = store.Update(instanceID, func(current *procowner.Receipt) error {
			close(firstInside)
			<-letFirstFinish
			current.Note = "written by the first writer"
			return nil
		})
	}()
	<-firstInside

	var observed string
	done := make(chan error, 1)
	go func() {
		done <- store.Update(instanceID, func(current *procowner.Receipt) error {
			observed = current.Note
			return procowner.ErrNoChange
		})
	}()

	// The second update is queued for the lock. Release the first.
	time.Sleep(50 * time.Millisecond)
	close(letFirstFinish)
	require.NoError(t, <-done)

	assert.Equal(t, "written by the first writer", observed,
		"the queued writer must read state as of the moment it won the lock, not as of the moment it queued")
}
