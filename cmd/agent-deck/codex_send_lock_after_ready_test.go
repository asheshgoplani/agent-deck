package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A send to a mid-turn Codex waits for the turn to end. It used to take the
// per-session acceptance lock first and hold it for that whole wait (up to
// --timeout), so every other send to the session failed after 5s with
// "Codex acceptance lock timed out" until the turn ended.
func TestCodexSendDoesNotHoldAcceptanceLockWhileWaitingForReady(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	const codexID = "thread-lock-after-ready"

	inReadyWait := make(chan struct{})
	turnEnded := make(chan struct{})
	waitReady := func(time.Duration) error {
		close(inReadyWait)
		<-turnEnded
		return nil
	}
	acquire := func(d time.Duration) (*codexAcceptanceGuard, error) {
		lock, err := session.AcquireCodexAcceptanceLock(codexID, d)
		if err != nil {
			return nil, err
		}
		return &codexAcceptanceGuard{lock: lock, fence: codexAcceptanceFence{priorTurnGeneration: "g1", available: true}}, nil
	}
	generation := func() (string, error) { return "g1", nil }

	done := make(chan error, 1)
	go func() {
		guard, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second, waitReady, generation, acquire)
		if guard != nil {
			guard.Release()
		}
		done <- errors.Join(readyErr, guardErr)
	}()

	<-inReadyWait
	other, err := session.AcquireCodexAcceptanceLock(codexID, 200*time.Millisecond)
	if err != nil {
		close(turnEnded)
		t.Fatalf("a send waiting for the turn to end held the acceptance lock: %v", err)
	}
	other.Release()
	close(turnEnded)
	if err := <-done; err != nil {
		t.Fatalf("send did not acquire after the turn ended: %v", err)
	}
}

// Both ways another send can win the race at turn end send this one back to
// waiting for readiness instead of refusing it.
func TestCodexAcceptanceWhenReadyWaitsAgainOnContention(t *testing.T) {
	for _, tc := range []struct {
		name string
		// first is the first acquisition's outcome; the second always succeeds.
		lockHeld, turnBegan bool
	}{
		{name: "lock held by a submitting send", lockHeld: true},
		{name: "a turn began while queued for the lock", turnBegan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			gen, readyCalls, acquireCalls := "g1", 0, 0
			acquire := func(d time.Duration) (*codexAcceptanceGuard, error) {
				acquireCalls++
				if acquireCalls == 1 && tc.lockHeld {
					return nil, fmt.Errorf("%w after %s", session.ErrCodexAcceptanceLockTimeout, d)
				}
				if acquireCalls == 1 && tc.turnBegan {
					gen = "g2" // another send's turn started after readiness
				}
				lock, err := session.AcquireCodexAcceptanceLock("thread-contention", d)
				if err != nil {
					return nil, err
				}
				return &codexAcceptanceGuard{lock: lock, fence: codexAcceptanceFence{priorTurnGeneration: gen, available: true}}, nil
			}
			guard, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
				func(time.Duration) error { readyCalls++; return nil },
				func() (string, error) { return gen, nil },
				acquire)
			if readyErr != nil || guardErr != nil || guard == nil {
				t.Fatalf("guard=%v readyErr=%v guardErr=%v", guard, readyErr, guardErr)
			}
			guard.Release()
			if readyCalls != 2 || acquireCalls != 2 {
				t.Fatalf("readiness waits = %d, acquisitions = %d; want 2 and 2", readyCalls, acquireCalls)
			}
			if guard.fence.priorTurnGeneration != gen {
				t.Fatalf("fence %q is not the generation seen at readiness %q", guard.fence.priorTurnGeneration, gen)
			}
		})
	}
}

func TestCodexAcceptanceWhenReadyRefusesOtherGuardErrorsAtOnce(t *testing.T) {
	refusal := errors.New("unresolved earlier submission")
	readyCalls := 0
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error { readyCalls++; return nil },
		func() (string, error) { return "g1", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) { return nil, refusal })
	if readyErr != nil || !errors.Is(guardErr, refusal) || readyCalls != 1 {
		t.Fatalf("readyErr=%v guardErr=%v readyCalls=%d", readyErr, guardErr, readyCalls)
	}
}

func TestCodexAcceptanceWhenReadyEndsAtReadinessDeadline(t *testing.T) {
	notReady := errors.New("agent not ready after 1s")
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error { return notReady },
		func() (string, error) { return "", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			t.Fatal("acquired the lock for a target that never became ready")
			return nil, nil
		})
	if !errors.Is(readyErr, notReady) || guardErr != nil {
		t.Fatalf("readyErr=%v guardErr=%v", readyErr, guardErr)
	}

	// Contention that outlasts the budget is a refusal (nothing was typed, so
	// the queue may retry), not a spin and not a readiness timeout.
	_, readyErr, guardErr = acquireCodexAcceptanceWhenReady(time.Now().Add(50*time.Millisecond), time.Second,
		func(time.Duration) error { time.Sleep(10 * time.Millisecond); return nil },
		func() (string, error) { return "", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			return nil, session.ErrCodexAcceptanceLockTimeout
		})
	if readyErr != nil || !errors.Is(guardErr, session.ErrCodexAcceptanceLockTimeout) {
		t.Fatalf("readyErr=%v guardErr=%v", readyErr, guardErr)
	}
}

// Readiness comes from a pane sample taken before waitReady returns. A turn
// that starts in that gap must not become this send's baseline, or the fence
// matches it and the send types into a running turn.
func TestCodexAcceptanceWhenReadyBaselinePrecedesTheWait(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	gen, readyCalls := "g1", 0
	guard, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error {
			readyCalls++
			if readyCalls == 1 {
				gen = "g2" // another sender's turn starts as readiness returns
			}
			return nil
		},
		func() (string, error) { return gen, nil },
		func(d time.Duration) (*codexAcceptanceGuard, error) {
			lock, err := session.AcquireCodexAcceptanceLock("thread-baseline", d)
			if err != nil {
				return nil, err
			}
			return &codexAcceptanceGuard{lock: lock, fence: codexAcceptanceFence{priorTurnGeneration: gen, available: true}}, nil
		})
	if readyErr != nil || guardErr != nil || guard == nil {
		t.Fatalf("guard=%v readyErr=%v guardErr=%v", guard, readyErr, guardErr)
	}
	guard.Release()
	if readyCalls != 2 {
		t.Fatalf("readiness waits = %d, want 2: the send accepted a turn that began during its wait", readyCalls)
	}
}

// The real waiter needs a few seconds on an idle pane, so with contention the
// budget usually runs out inside waitReady. That is still contention: nothing
// was typed, and the queue retries an acceptance refusal but not a readiness
// timeout.
func TestCodexAcceptanceWhenReadyContentionThenReadinessTimeoutRefuses(t *testing.T) {
	readyCalls := 0
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error {
			readyCalls++
			if readyCalls > 1 {
				return errors.New("agent not ready after 1s")
			}
			return nil
		},
		func() (string, error) { return "g1", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			return nil, session.ErrCodexAcceptanceLockTimeout
		})
	if readyErr != nil || !errors.Is(guardErr, session.ErrCodexAcceptanceLockTimeout) {
		t.Fatalf("readyErr=%v guardErr=%v", readyErr, guardErr)
	}
}

// A generation that cannot be read refuses before the wait, as it did when the
// fence was captured first, instead of surfacing as a readiness timeout.
func TestCodexAcceptanceWhenReadyUnavailableGenerationRefusesBeforeWaiting(t *testing.T) {
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second,
		func(time.Duration) error {
			t.Fatal("waited for a target whose rollout generation is unavailable")
			return nil
		},
		func() (string, error) { return "", errors.New("no rollout") },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			t.Fatal("took the lock without a generation")
			return nil, nil
		})
	if readyErr != nil || !errors.Is(guardErr, errCodexGenerationUnavailable) {
		t.Fatalf("readyErr=%v guardErr=%v", readyErr, guardErr)
	}
}

func TestCodexAcceptanceWhenReadyNoWaitAcquiresOnce(t *testing.T) {
	calls := 0
	_, readyErr, guardErr := acquireCodexAcceptanceWhenReady(time.Now().Add(time.Minute), time.Second, nil,
		func() (string, error) { return "", nil },
		func(time.Duration) (*codexAcceptanceGuard, error) {
			calls++
			return nil, session.ErrCodexAcceptanceLockTimeout
		})
	if readyErr != nil || !errors.Is(guardErr, session.ErrCodexAcceptanceLockTimeout) || calls != 1 {
		t.Fatalf("--no-wait must keep the single bounded lock attempt: readyErr=%v guardErr=%v calls=%d", readyErr, guardErr, calls)
	}
}

// The readiness baseline and the fence must read the turn generation with the
// same budget. A turn that has been emitting output for a long time pushes its
// own turn start past the 8 MiB default ceiling; the default reader then gave
// a "" baseline while the 64 MiB fence read the real turn, so every pass
// looked like a turn that began after the baseline and an idle target refused
// the send at the deadline.
func TestCodexSendBaselineReadsPastTheDefaultCeiling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	codexHome := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codexHome)
	const thread = "01a0cc41-3ddb-7170-98bc-27b021e69142"
	path := filepath.Join(codexHome, "sessions", "2026", "11", "12", "rollout-2026-11-12T05-13-42-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// The newest (only) turn start, then 10 MiB of output before EOF.
	rollout := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-long"}}` + "\n" +
		strings.Repeat(strings.Repeat("x", 998)+"\n", 10500) +
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-long"}}` + "\n"
	if err := os.WriteFile(path, []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := &session.Instance{ID: "long-turn-baseline", Tool: "codex", CodexSessionID: thread}
	if narrow, err := inst.LatestCodexTurnGeneration(); err != nil || narrow != "" {
		t.Fatalf("fixture: default reader got %q, %v; want \"\" (turn start beyond 8 MiB)", narrow, err)
	}

	waits := 0
	guard, readyErr, guardErr := acquireCodexAcceptanceForSend(inst, time.Now().Add(2*time.Second), time.Second,
		func(time.Duration) error { waits++; return nil })
	if guard != nil {
		defer guard.Release()
	}
	if readyErr != nil || guardErr != nil {
		t.Fatalf("idle target with a long last turn refused: readyErr=%v guardErr=%v (waits=%d)", readyErr, guardErr, waits)
	}
	if want := thread + ":turn-long"; guard.fence.priorTurnGeneration != want || waits != 1 {
		t.Fatalf("fence=%q waits=%d; want %q on the first pass", guard.fence.priorTurnGeneration, waits, want)
	}
}

// A send that finds an earlier attempt's marker on the same running turn may
// clear it only when that turn took up input after the attempt began. The
// fence reads the turn with the 64 MiB acceptance budget, so the input check
// must too: with the 8 MiB default it never reached a turn start 10 MiB back,
// reported no input, and kept refusing every later send until an operator
// removed the marker.
func TestCodexSendReconcileReadsRunningTurnInputPastTheDefaultCeiling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	codexHome := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codexHome)
	const thread = "01a0cc41-3ddb-7170-98bc-27b021e69143"
	path := filepath.Join(codexHome, "sessions", "2026", "11", "12", "rollout-2026-11-12T05-13-42-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := func(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }
	userInput := func(at time.Time) string {
		return `{"timestamp":"` + stamp(at) + `","type":"event_msg","payload":{"type":"item_completed","turn_id":"turn-long","item":{"type":"UserMessage","id":"item"}}}` + "\n"
	}
	start := time.Date(2026, 11, 12, 5, 13, 42, 0, time.UTC)
	createdAt := start.Add(50 * time.Minute) // the earlier send's marker
	// The running turn: its start and opening input, 10 MiB of output, then
	// the earlier send's input taken into the same turn. No task_complete.
	rollout := `{"timestamp":"` + stamp(start) + `","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-long"}}` + "\n" +
		userInput(start.Add(time.Second)) +
		strings.Repeat(strings.Repeat("x", 998)+"\n", 10500) +
		userInput(createdAt.Add(2*time.Second))
	if err := os.WriteFile(path, []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := &session.Instance{ID: "long-turn-reconcile", Tool: "codex", CodexSessionID: thread}
	generation := thread + ":turn-long"
	if took, err := inst.CodexTurnTookInputSince(generation, createdAt); err != nil || took {
		t.Fatalf("fixture: default-budget check got %v, %v; want false (turn start beyond 8 MiB)", took, err)
	}
	marker, err := session.PrepareCodexSubmissionMarker(inst.ID, thread, generation, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := marker.MarkTransportAmbiguous(createdAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	guard, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatalf("marker whose input the running turn took was not cleared: %v", err)
	}
	defer guard.Release()
	if guard.fence.priorTurnGeneration != generation {
		t.Fatalf("fence=%q; want %q", guard.fence.priorTurnGeneration, generation)
	}
	if err := session.ClearCodexSubmissionMarker(marker); err == nil {
		t.Fatal("marker is still on disk after the guard reconciled it")
	}
}

// The budgeted input check still refuses when the running turn took no input
// after the marker: reading further back must not turn a missing input into
// evidence.
func TestCodexSendReconcileStillRefusesWithoutLaterInputPastTheDefaultCeiling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	codexHome := filepath.Join(home, "codex")
	t.Setenv("CODEX_HOME", codexHome)
	const thread = "01a0cc41-3ddb-7170-98bc-27b021e69144"
	path := filepath.Join(codexHome, "sessions", "2026", "11", "12", "rollout-2026-11-12T05-13-42-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 12, 5, 13, 42, 0, time.UTC)
	createdAt := start.Add(50 * time.Minute)
	rollout := `{"timestamp":"` + start.Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-long"}}` + "\n" +
		`{"timestamp":"` + start.Add(time.Second).Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"item_completed","turn_id":"turn-long","item":{"type":"UserMessage","id":"item"}}}` + "\n" +
		strings.Repeat(strings.Repeat("x", 998)+"\n", 10500)
	if err := os.WriteFile(path, []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "long-turn-refuse", Tool: "codex", CodexSessionID: thread}
	generation := thread + ":turn-long"
	marker, err := session.PrepareCodexSubmissionMarker(inst.ID, thread, generation, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if guard, err := acquireCodexAcceptanceGuard(inst, time.Second); err == nil {
		guard.Release()
		t.Fatal("marker with no later input in the running turn was cleared")
	}
	if err := session.ClearCodexSubmissionMarker(marker); err != nil {
		t.Fatalf("refused marker should stay on disk: %v", err)
	}
}

// The receipt poll rereads the rollout only when it changed. Each read can
// scan up to 64 MiB backward when the newest turn start is far from EOF, and
// the poll used to repeat it every 50 ms for 2 s on an unchanged file.
func TestCodexAcceptedTurnPollSkipsUnchangedRollout(t *testing.T) {
	fence := codexAcceptanceFence{codexSessionID: "thread-poll", priorTurnGeneration: "thread-poll:t1", available: true}
	inst := &session.Instance{ID: "poll", Tool: "codex", CodexSessionID: "thread-poll"}

	reads := 0
	receipt := pollAcceptedCodexTurn(inst, deliverySubmitted, time.Now(), fence,
		func() string { return "v1" },
		func() (string, error) { reads++; return "thread-poll:t1", nil })
	if receipt != nil || reads != 1 {
		t.Fatalf("unchanged rollout: receipt=%v reads=%d; want nil after one read", receipt, reads)
	}

	// A changed version is read again and yields the new turn.
	reads, polls := 0, 0
	receipt = pollAcceptedCodexTurn(inst, deliverySubmitted, time.Now(), fence,
		func() string {
			polls++
			if polls < 3 {
				return "v1"
			}
			return "v2"
		},
		func() (string, error) {
			reads++
			if polls < 3 {
				return "thread-poll:t1", nil
			}
			return "thread-poll:t2", nil
		})
	if receipt == nil || receipt.TurnGeneration != "thread-poll:t2" || reads != 2 {
		t.Fatalf("changed rollout: receipt=%v reads=%d; want t2 on the second read", receipt, reads)
	}

	// No version (nothing to compare) or a failed read always reads again.
	for name, read := range map[string]func() (string, error){
		"unversioned": func() (string, error) { return "thread-poll:t1", nil },
		"failed read": func() (string, error) { return "", errors.New("rollout busy") },
	} {
		version := func() string { return "v1" }
		if name == "unversioned" {
			version = func() string { return "" }
		}
		reads = 0
		_ = pollAcceptedCodexTurn(inst, deliverySubmitted, time.Now(), fence, version,
			func() (string, error) { reads++; return read() })
		if reads < 10 {
			t.Fatalf("%s: read %d times in the poll window; want a read every poll", name, reads)
		}
	}
}
