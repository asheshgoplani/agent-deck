package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Status-light audit defect B (2026-09-17): the hook file still said
// "running" while the pane showed a completed turn ("✻ Sautéed for 3m 4s ·
// done 9:08 PM") at an empty prompt; why the Stop was late is unknown (see
// hook_lag.go). UpdateStatus took the UserPromptSubmit "running" at face
// value and the CLI paired status=running with substate=idle-at-empty-prompt
// — a pair the substate's own contract forbids.
//
// Rules under test:
//   - two independent samples of a completed turn at an idle prompt, taken
//     by the pane reads the status/substate path already makes, flip the
//     light to waiting and the substate says hook-lag; never on one sample;
//     never against a live spinner;
//   - the running hook fast path itself never captures the pane (review
//     P2-5): its tmux subprocess count is zero;
//   - the samples are persisted on the instance record, so a one-pass CLI
//     caller in a fresh process continues from them (review P2-4).

// auditConductorIdlePane is the captured conductor tail (account text
// replaced). The idle footer also carries the defect-F trap ("… +N lines" and
// "/clear to save Nk tokens" on separate lines).
const auditConductorIdlePane = "⏺ Bash(git -C ~/agent-deck log --oneline -3)\n" +
	"     … +24 lines (ctrl+o to expand)\n" +
	"⏺ Both children reported back; nothing else is pending.\n" +
	"✻ Sautéed for 3m 4s · done 9:08 PM\n" +
	"──────────────────────────────────────────────── conductor-agent-deck ─\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"  [profile] user@host:~/.agent-deck/conductor/agent-deck | [Opus 5 (1M context)] ctx:38% in:380.8k out:1.2k\n" +
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
	"                                                new task? /clear to save 380.8k tokens\n"

const auditConductorBusyPane = "⏺ Bash(git -C ~/agent-deck log --oneline -3)\n" +
	"     … +24 lines (ctrl+o to expand)\n" +
	"✻ Sautéing… (2m 58s · ↓ 4.1k tokens · ctrl+c to interrupt)\n" +
	"                                                new task? /clear to save 380.8k tokens\n"

// writeHookLagStopFile writes the Stop event for the same hook session that
// writeHookFile bound (a different session_id would be rejected as a foreign
// ephemeral by UpdateHookStatus's ownership check).
func writeHookLagStopFile(t *testing.T, instanceID string) {
	t.Helper()
	body := fmt.Sprintf(`{"status":"waiting","session_id":"sess-610","event":"Stop","ts":%d}`,
		time.Now().Add(-time.Second).Unix())
	path := filepath.Join(GetHooksDir(), instanceID+".json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}

// startHookLagInstance starts a real tmux pane rendering paneText and a fresh
// "running" hook file for it, then returns the instance ready for UpdateStatus.
func startHookLagInstance(t *testing.T, name, paneText string) (*Instance, func()) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	panePath := filepath.Join(tmpHome, "pane.txt")
	if err := os.WriteFile(panePath, []byte(paneText), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("hook-lag-"+name, tmpHome, "claude")
	if err := inst.tmuxSession.Start(fmt.Sprintf("sh -c 'cat %q; exec sleep 3600'", panePath)); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	cleanup := func() { _ = inst.tmuxSession.Kill() }
	// The pane is a plain shell rendering a captured Claude frame; tell the
	// tmux layer whose renderings it is reading.
	inst.Command = "claude"
	inst.tmuxSession.Command = "claude"
	// Past UpdateStatus's 1.5s tmux grace window.
	time.Sleep(2 * time.Second)

	writeHookFile(t, inst.ID, "running", 10)
	RefreshInstancesForCLIStatus([]*Instance{inst})
	return inst, cleanup
}

// cliPass is one `list --json` / `session children --json` pass over inst:
// hook cold-load, UpdateStatus, then the substate read (which is the pass's
// single pane capture).
func cliPass(t *testing.T, inst *Instance) (Status, Substate) {
	t.Helper()
	RefreshInstancesForCLIStatus([]*Instance{inst})
	var pass StatusUpdatePass
	if err := pass.UpdateStatusOnly(inst); err != nil {
		t.Fatalf("update status: %v", err)
	}
	sub := inst.Substate()
	return inst.GetStatusThreadSafe(), sub
}

func TestAudit_B_HookLagFlipsToWaitingAfterTwoSamples(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "idle", auditConductorIdlePane)
	defer cleanup()

	// Pass 1: the hook is fresh and says running. One pane sample is not
	// enough to overrule it — the light stays running — but the substate
	// already names the disagreement instead of claiming idle-at-empty-prompt.
	if status, sub := cliPass(t, inst); status != StatusRunning || sub != SubstateHookLag {
		t.Fatalf("pass 1 = %q/%q, want running/%q (never flip on a single sample)", status, sub, SubstateHookLag)
	}

	// Pass 2: a second independent sample of the same finished frame, taken
	// by the same substate read — no capture of the fast path's own.
	time.Sleep(tmux.CompletedTurnSampleInterval + 200*time.Millisecond)
	if status, sub := cliPass(t, inst); status != StatusWaiting || sub != SubstateHookLag {
		t.Fatalf("pass 2 = %q/%q, want waiting/%q (hook lag confirmed)", status, sub, SubstateHookLag)
	}
	if got := inst.CachedSubstate(); got != SubstateHookLag {
		t.Fatalf("pass 2 cached substate = %q, want %q (TUI rows read the cache)", got, SubstateHookLag)
	}
	// The verdict holds on the next status pass without any new sample.
	if err := inst.UpdateStatus(); err != nil {
		t.Fatal(err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusWaiting {
		t.Fatalf("status after confirmation = %q, want waiting", got)
	}

	// The Stop hook finally lands: the lag is over and the ordinary hook
	// verdict applies again (waiting, idle-at-empty-prompt).
	writeHookLagStopFile(t, inst.ID)
	if status, sub := cliPass(t, inst); status != StatusWaiting || sub != SubstateIdleAtEmptyPrompt {
		t.Fatalf("pass 3 = %q/%q, want waiting/%q (lag cleared by the new hook event)", status, sub, SubstateIdleAtEmptyPrompt)
	}
}

func TestAudit_B_LiveSpinnerNeverContradicted(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "busy", auditConductorBusyPane)
	defer cleanup()

	for pass := 1; pass <= 3; pass++ {
		if status, sub := cliPass(t, inst); status != StatusRunning || sub != SubstateRunning {
			t.Fatalf("pass %d = %q/%q, want running/%q (live spinner on screen)", pass, status, sub, SubstateRunning)
		}
		time.Sleep(tmux.CompletedTurnSampleInterval + 200*time.Millisecond)
	}
}

// Review P2-5: the running hook fast path reads no pane. This is the same
// counter the health sampler reports as tmux_calls per status pass; round 1
// added one capture-pane per running Claude session every 3s here, which
// this test fails on (delta 1 per spaced pass) and the current tree passes
// (delta 0).
func TestAudit_B_RunningFastPathMakesNoTmuxCalls(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "quiet", auditConductorIdlePane)
	defer cleanup()

	for pass := 1; pass <= 2; pass++ {
		tmux.RefreshExistingSessions() // the once-per-tick cache the TUI warms
		before := tmux.SubprocessStarts()
		if err := inst.UpdateStatus(); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if got := tmux.SubprocessStarts() - before; got != 0 {
			t.Fatalf("pass %d: running fast path started %d tmux subprocesses, want 0", pass, got)
		}
		if got := inst.GetStatusThreadSafe(); got != StatusRunning {
			t.Fatalf("pass %d: status = %q, want running (no sample was ever taken)", pass, got)
		}
		time.Sleep(tmux.CompletedTurnSampleInterval + 200*time.Millisecond)
	}
}

// Review P2-4: the samples are persisted on the instance record, so the
// verdict does not depend on which process took them. Three "processes"
// (fresh instance objects loaded from the same profile DB) mirror three
// one-pass CLI invocations: the first samples, the second samples again and
// flips, the third reports the flip from the record alone.
func TestAudit_B_HookLagPersistsAcrossCLIProcesses(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "persist", auditConductorIdlePane)
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-hook-lag")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	load := func() *Instance {
		t.Helper()
		instances, _, err := storage.LoadWithGroups()
		if err != nil || len(instances) != 1 {
			t.Fatalf("load: %v (%d instances)", err, len(instances))
		}
		return instances[0]
	}

	// Process 1: one sample, persisted.
	p1 := load()
	if status, sub := cliPass(t, p1); status != StatusRunning || sub != SubstateHookLag {
		t.Fatalf("process 1 = %q/%q, want running/%q", status, sub, SubstateHookLag)
	}
	if rec := load().hookLag; rec.FirstIdleAt == 0 {
		t.Fatal("process 1's sample was not persisted to tool_data.hook_lag")
	}

	// Process 2: a fresh object, hydrated with process 1's sample; its own
	// sample is the second one, so it reports the flip.
	time.Sleep(tmux.CompletedTurnSampleInterval + 200*time.Millisecond)
	p2 := load()
	if status, sub := cliPass(t, p2); status != StatusWaiting || sub != SubstateHookLag {
		t.Fatalf("process 2 = %q/%q, want waiting/%q", status, sub, SubstateHookLag)
	}

	// Process 3: no sample of its own needed — the status pass alone reads
	// the confirmed record, so every one-pass caller agrees.
	p3 := load()
	RefreshInstancesForCLIStatus([]*Instance{p3})
	if err := p3.UpdateStatus(); err != nil {
		t.Fatal(err)
	}
	if got := p3.GetStatusThreadSafe(); got != StatusWaiting {
		t.Fatalf("process 3 status = %q, want waiting from the persisted record", got)
	}
	if got := p3.CachedSubstate(); got != SubstateHookLag && got != SubstateNone {
		// CachedSubstate has no frame of its own yet; the reconciled label is
		// the only thing it can add.
		t.Fatalf("process 3 cached substate = %q", got)
	}

	// A later hook event supersedes the record: the ordinary hook verdict
	// applies (waiting, or idle for a row loaded as already acknowledged).
	writeHookLagStopFile(t, inst.ID)
	p4 := load()
	if status, sub := cliPass(t, p4); status == StatusRunning || sub != SubstateIdleAtEmptyPrompt {
		t.Fatalf("after Stop = %q/%q, want not running/%q", status, sub, SubstateIdleAtEmptyPrompt)
	}
}

// Review round 3 P2-3: the pass's own live evidence wins over a persisted
// record. Another process confirmed the lag (record persisted); by the time
// this one-pass CLI caller runs, the pane is busy again under the SAME hook
// event (a continuation turn that wrote no new UserPromptSubmit). The status
// pass sets waiting from the record alone, then the pass's single capture
// sees the spinner: the same pass must report running/running, never
// waiting + running, and persist the reset for the next caller.
func TestAudit_B_BusyCaptureRevertsRecordDrivenWaitingInSamePass(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "revert", auditConductorBusyPane)
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-hook-lag-revert")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// The record another process left: confirmed under the current hook
	// event, sampled before this pass runs.
	hookTS := inst.hookLastUpdate.Unix()
	rec := hookLagRecord{HookTS: hookTS, FirstIdleAt: hookTS + 2, LastIdleAt: hookTS + 6, LastSampleAt: hookTS + 6}
	if !rec.confirmed(inst.hookLastUpdate) {
		t.Fatalf("test record must be confirmed: %+v", rec)
	}
	raw, _ := json.Marshal(rec)
	if err := storage.db.WriteToolDataExtra(inst.ID, toolDataHookLagKey, raw); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	load := func() *Instance {
		t.Helper()
		instances, _, err := storage.LoadWithGroups()
		if err != nil || len(instances) != 1 {
			t.Fatalf("load: %v (%d instances)", err, len(instances))
		}
		return instances[0]
	}
	p := load()
	if !p.hookLag.confirmed(inst.hookLastUpdate) {
		t.Fatalf("loaded instance did not hydrate the confirmed record: %+v", p.hookLag)
	}
	// The status pass alone (no capture) reads waiting from the record.
	RefreshInstancesForCLIStatus([]*Instance{p})
	var pass StatusUpdatePass
	if err := pass.UpdateStatusOnly(p); err != nil {
		t.Fatal(err)
	}
	if got := p.GetStatusThreadSafe(); got != StatusWaiting {
		t.Fatalf("status from the record alone = %q, want waiting (the pass has not captured yet)", got)
	}
	// The pass's single capture is busy: live evidence wins, same pass.
	sub := p.Substate()
	if status := p.GetStatusThreadSafe(); status != StatusRunning || sub != SubstateRunning {
		t.Fatalf("one pass = %q/%q, want running/%q (busy capture reverts the record-driven waiting)", status, sub, SubstateRunning)
	}
	// The reset is persisted, so the next one-pass caller does not flip again.
	if rec := load().hookLag; rec.FirstIdleAt != 0 || rec.HookTS != hookTS {
		t.Fatalf("persisted record after the busy sample = %+v, want cleared run under hook %d", rec, hookTS)
	}
	if status, sub := cliPass(t, load()); status != StatusRunning || sub != SubstateRunning {
		t.Fatalf("next pass = %q/%q, want running/%q", status, sub, SubstateRunning)
	}
}

// Pure rule tests: sample accounting is by whole seconds, keyed by the hook
// event, and a busy sample clears the run.
func TestHookLagRecord(t *testing.T) {
	hook := time.Unix(1_000_000, 0)
	var r hookLagRecord
	r.note(true, hook, hook) // same second as the hook: not attributable
	if r.observed(hook) {
		t.Fatal("a sample in the hook's own second must not count")
	}
	r.note(true, hook.Add(2*time.Second), hook)
	if !r.observed(hook) || r.confirmed(hook) {
		t.Fatalf("one sample: observed but not confirmed, got %+v", r)
	}
	r.note(true, hook.Add(2*time.Second), hook) // the same cached frame again
	if r.confirmed(hook) {
		t.Fatal("re-applying the same frame must not count as a second sample")
	}
	r.note(true, hook.Add(4*time.Second), hook)
	if r.confirmed(hook) {
		t.Fatal("two samples 2s apart are not independent")
	}
	r.note(true, hook.Add(5*time.Second), hook)
	if !r.confirmed(hook) {
		t.Fatalf("samples 3s apart must confirm, got %+v", r)
	}
	r.note(false, hook.Add(6*time.Second), hook)
	if r.observed(hook) {
		t.Fatal("a busy sample clears the evidence")
	}
	later := hook.Add(30 * time.Second)
	r.note(true, hook.Add(7*time.Second), later)
	if r.HookTS != later.Unix() || r.observed(later) {
		t.Fatalf("a new hook event starts over and predating samples are ignored, got %+v", r)
	}
	if !(hookLagRecord{HookTS: 2}).newerThan(hookLagRecord{HookTS: 1}) ||
		!(hookLagRecord{HookTS: 1, LastSampleAt: 5}).newerThan(hookLagRecord{HookTS: 1, LastSampleAt: 4}) ||
		(hookLagRecord{HookTS: 1, LastSampleAt: 4}).newerThan(hookLagRecord{HookTS: 1, LastSampleAt: 4}) {
		t.Fatal("newerThan ordering")
	}
}

// The contradictory pair is closed at the accessor too: whatever produced a
// running status, the substate never claims idle-at-empty-prompt beside it.
func TestReconcileSubstateWithStatus(t *testing.T) {
	cases := []struct {
		name   string
		status Status
		sub    Substate
		lagged bool
		want   Substate
	}{
		{"running + idle prompt, lag armed", StatusRunning, SubstateIdleAtEmptyPrompt, true, SubstateHookLag},
		{"running + idle prompt, no lag evidence", StatusRunning, SubstateIdleAtEmptyPrompt, false, SubstateNone},
		{"waiting + idle prompt", StatusWaiting, SubstateIdleAtEmptyPrompt, false, SubstateIdleAtEmptyPrompt},
		{"waiting after confirmed lag", StatusWaiting, SubstateIdleAtEmptyPrompt, true, SubstateHookLag},
		{"running + running", StatusRunning, SubstateRunning, false, SubstateRunning},
		{"running + running, stale lag evidence", StatusRunning, SubstateRunning, true, SubstateRunning},
		{"error + auth", StatusError, SubstateAuth401, false, SubstateAuth401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconcileSubstateWithStatus(tc.status, tc.sub, tc.lagged); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
