package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// PR-B commit 3, review round 5 (Forge R4). Tests first. The round 4 inbox lease
// is replaced by peek-then-consume-on-receipt; the launch decision is persisted
// on every launch path (Restart included) and only for local launches.

// ---- inbox peek: nothing is stranded -----------------------------------------

// A pi injection peeks its inbox records and records their ids under the token
// in the consumer file. If the ledger is turned off or the parent's enrolment is
// removed before any receipt, nothing may be left waiting on a receipt that can
// no longer be applied: the plain inbox path drains the records, once.
func TestPiInboxPeek_NothingIsStrandedWhenTheLedgerOrTheEnrolmentGoes(t *testing.T) {
	for name, stop := range map[string]func(t *testing.T, f *deliverFixture){
		"the ledger is turned off": func(t *testing.T, f *deliverFixture) {
			restore := SetCommsLedgerForTest(false)
			t.Cleanup(restore)
		},
		"the enrolment is removed": func(t *testing.T, f *deliverFixture) {
			if err := os.Remove(commsEnrollmentPath(f.parent.ID)); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPiParentFixture(t)
			f.inboxOnly(t, "inbox-only status text")
			dl, ok := LedgerPromptContext(f.parent.ID, "carry on", "")
			if !ok || !strings.Contains(dl.Text, "inbox-only status text") {
				t.Fatalf("no injection (ok %v)", ok)
			}
			dl.Done(true) // printed; no receipt will ever come
			stop(t, f)
			if !InboxHasPending(f.parent.ID) {
				t.Fatal("the peeked record left the inbox without a receipt")
			}
			text, evs, err := DrainForPrompt(f.parent.ID)
			if err != nil || len(evs) != 1 || !strings.Contains(text, "inbox-only status text") {
				t.Fatalf("the plain inbox path did not deliver the record: %q %v %v", text, evs, err)
			}
			if InboxHasPending(f.parent.ID) {
				t.Error("the record is still pending after the plain drain delivered it")
			}
			if again, _, _ := DrainForPrompt(f.parent.ID); again != "" {
				t.Errorf("the record was delivered twice: %q", again)
			}
		})
	}
}

// ---- the receipt promotes the token's keys ------------------------------------

// Forge 2: an injection that carries only an inbox record (its ledger twin has
// not arrived) holds the turn key under its token. The receipt must promote that
// key even though no prompt attempt is in flight for it, so the ledger twin that
// lands afterwards is not injected a second time.
func TestPiInboxPeek_AnInboxReceiptPromotesItsKeysBeforeTheLedgerTwinArrives(t *testing.T) {
	f := newPiParentFixture(t)
	const uuid, text = "aaaaaaaa-0000-4000-8000-000000000f01", "which way?"
	twin := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "board-zero", ToStatus: "waiting", Tier: TurnTierInfo,
		Text: text, TurnUUID: uuid, LastOutputHash: (TurnFacts{UUID: uuid}).Signal(), Profile: "default"}
	if committed, _, _ := f.d.notifier.commitEventToInbox(twin); !committed {
		t.Fatal("commit of the inbox twin")
	}
	// 1. The inbox record is injected first (the ledger has nothing yet).
	dl, ok := LedgerPromptContext(f.parent.ID, "carry on", "")
	if !ok || strings.Count(dl.Text, text) != 1 {
		t.Fatalf("first injection (ok %v): %v", ok, dl)
	}
	token := tokenOf(t, dl)
	if !tokenRE.MatchString(token) {
		t.Fatalf("token %q is not a ULID", token)
	}
	dl.Done(true)
	// 2. The receipt arrives.
	f.injectedEdge(t, token)
	// 3. The ledger twin of the same turn lands last.
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: text, Key: comms.Key(comms.KindTurn, f.child.ID, uuid)})
	next, _ := LedgerPromptContext(f.parent.ID, "carry on again", "")
	if next != nil && strings.Contains(next.Text, text) {
		t.Errorf("the ledger twin of a received turn was injected again: %q", next.Text)
	}
	if next != nil {
		next.Done(true)
	}
	if InboxHasPending(f.parent.ID) {
		t.Error("the inbox copy of a received turn is still pending")
	}
}

// ---- the launch decision: every path, local only --------------------------------

// fakePiVersion puts a pi on PATH that answers --version and otherwise stays
// alive, with the current extension installed, so a launch can export the socket.
func fakePiVersion(t *testing.T, version string) {
	t.Helper()
	installPiExtension(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	writeFakePi(t, bin, `if [ "$1" = "--version" ]; then echo `+version+`; exit 0; fi; exec sleep 300`)
}

// Forge 1: Restart is a launch path with its own targeted UPDATE
// (restart_tmux_persist.go). A real restart that flips the decision, either way,
// must leave the new value in storage. The instance is not saved by the test
// after the restart: only what the restart itself wrote counts.
func TestPiRestart_PersistsTheSocketDecisionBothWays(t *testing.T) {
	skipIfNoTmuxBinary(t)
	for _, tc := range []struct {
		name         string
		version      string
		was, wantNow bool
	}{
		{"a pi that lost the socket is recorded false", "1.1.0", true, false},
		{"a pi that gained the socket is recorded true", "1.3.0", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			piSocketShortData(t)
			fakePiVersion(t, tc.version)
			storage, inst := restartPersistFixture(t, "_test_pi_restart_"+randomString(8), "agentdeck_pirestart_deadbeef")
			inst.Tool = "pi"
			inst.IdentityInjectionDisabled = true
			inst.PiSocketExported = tc.was
			instances := []*Instance{inst}
			if err := storage.SaveWithGroups(instances, NewGroupTree(instances)); err != nil { // the state before the restart
				t.Fatal(err)
			}
			if got := storedPiSocketExported(t, storage, inst.ID); got != tc.was {
				t.Fatalf("fixture: stored PiSocketExported = %v, want %v", got, tc.was)
			}
			if err := inst.Restart(); err != nil {
				t.Fatalf("Restart: %v", err)
			}
			t.Cleanup(func() {
				if sess := inst.GetTmuxSession(); sess != nil {
					_ = sess.Kill()
				}
				joinSpawnWatchers(t, inst)
			})
			if inst.PiSocketExported != tc.wantNow {
				t.Fatalf("test setup: the restart decided PiSocketExported = %v, want %v", inst.PiSocketExported, tc.wantNow)
			}
			if got := storedPiSocketExported(t, storage, inst.ID); got != tc.wantNow {
				t.Errorf("after the restart, storage holds PiSocketExported = %v, want %v: the restart's targeted UPDATE did not write the decision", got, tc.wantNow)
			}
		})
	}
}

// storedPiSocketExported reloads the instance from storage, as the daemon does.
func storedPiSocketExported(t *testing.T, storage *Storage, id string) bool {
	t.Helper()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range instances {
		if in.ID == id {
			return in.PiSocketExported
		}
	}
	t.Fatalf("instance %q missing from storage", id)
	return false
}

// Forge 5: the socket is for local launches. The extension of an SSH or sandbox
// pi runs in another filesystem and process namespace, so the host daemon can
// never reach a socket under the host's runtime directory. Such a launch exports
// nothing and records false, so the typed doorbell keeps working as today.
func TestBuildPiCommand_NeverExportsTheSocketForSSHOrSandboxLaunches(t *testing.T) {
	piSocketShortData(t)
	fakePiVersion(t, "1.3.0")

	local := &Instance{ID: "inst-local", Tool: "pi", IdentityInjectionDisabled: true}
	if cmd := local.buildPiCommand("pi"); !exportsPiSocket(cmd) || !local.PiSocketExported {
		t.Fatalf("test setup: a local pi 1.3.0 launch must export the socket (flag %v): %q", local.PiSocketExported, cmd)
	}
	for name, mutate := range map[string]func(*Instance){
		"ssh":     func(i *Instance) { i.SSHHost = "devbox" },
		"sandbox": func(i *Instance) { i.Sandbox = &SandboxConfig{Enabled: true} },
	} {
		t.Run(name, func(t *testing.T) {
			// The flag starts true: the launch must write false, explicitly.
			inst := &Instance{ID: "inst-" + name, Tool: "pi", IdentityInjectionDisabled: true, PiSocketExported: true}
			mutate(inst)
			cmd := inst.buildPiCommand("pi")
			if strings.Contains(cmd, "AGENTDECK_PI_SOCKET") {
				t.Errorf("a %s launch exports the host socket: %q", name, cmd)
			}
			if inst.PiSocketExported {
				t.Errorf("a %s launch recorded PiSocketExported = true: the daemon would never type its doorbell", name)
			}
		})
	}
}

// ---- round 6: every launch path persists the decision -----------------------

// piStartFixture is a pi instance saved but not started, in its own profile
// registered as the process-wide StateDB (as main.go and NewHome do), with a pi
// on PATH that reports version. The stored flag starts as was.
func piStartFixture(t *testing.T, version string, was bool) (*Storage, *Instance) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	piSocketShortData(t)
	fakePiVersion(t, version)
	storage, err := NewStorageWithProfile("_test_pi_start_" + randomString(8))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	prev := statedb.GetGlobal()
	statedb.SetGlobal(storage.GetDB())
	t.Cleanup(func() { statedb.SetGlobal(prev) })
	inst := NewInstanceWithTool("pi-start", t.TempDir(), "pi")
	inst.Command = "pi"
	inst.IdentityInjectionDisabled = true
	inst.GroupPath = DefaultGroupPath
	inst.PiSocketExported = was
	instances := []*Instance{inst}
	if err := storage.SaveWithGroups(instances, NewGroupTree(instances)); err != nil {
		t.Fatal(err)
	}
	if got := storedPiSocketExported(t, storage, inst.ID); got != was {
		t.Fatalf("fixture: stored PiSocketExported = %v, want %v", got, was)
	}
	return storage, inst
}

// Forge R5: Start is a launch path too. The CLI start, the web start
// (WebMutator.StartSession calls inst.Start) and the TUI all reach it, and none
// saves the instance afterwards. A start that flips the decision, either way,
// must leave the new value in storage; the test does not save after the start.
func TestPiStart_PersistsTheSocketDecisionBothWays(t *testing.T) {
	for _, tc := range []struct {
		name         string
		version      string
		was, wantNow bool
	}{
		{"a start that gains the socket records true", "1.3.0", false, true},
		{"a start that loses the socket records false", "1.1.0", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage, inst := piStartFixture(t, tc.version, tc.was)
			if err := inst.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() {
				if sess := inst.GetTmuxSession(); sess != nil {
					_ = sess.Kill()
				}
				joinSpawnWatchers(t, inst)
			})
			if inst.PiSocketExported != tc.wantNow {
				t.Fatalf("test setup: the start decided PiSocketExported = %v, want %v", inst.PiSocketExported, tc.wantNow)
			}
			if got := storedPiSocketExported(t, storage, inst.ID); got != tc.wantNow {
				t.Errorf("after Start, storage holds PiSocketExported = %v, want %v: nothing wrote the decision", got, tc.wantNow)
			}
		})
	}
}

// The decision is persisted once the pane has spawned, not when StartWithMessage
// returns. A message that cannot be delivered makes the call fail later (it waits
// up to send.DefaultAgentReadyTimeout, ten minutes, for a prompt), yet the pi was
// launched with its socket the moment the pane existed: a flag that waits for the
// return is lost to a failure, a crash or a long wait. The fake pi draws no
// prompt until the test lets it, so the call is still inside its post-spawn wait
// while the test reads storage; that wait is the post-spawn failure path,
// observed before it ends.
func TestPiStartWithMessage_PersistsTheDecisionAtSpawnNotAtReturn(t *testing.T) {
	storage, inst := piStartFixture(t, "1.3.0", false)
	t.Cleanup(func() { killTmuxSessionsNamed(t, "pi-start") })
	// The wait polls tmux for up to ten minutes and has no cancel, so a call left
	// waiting outlives the test and its tmux polls land in later tests' counts.
	// The fake pi draws a shell prompt once the release file exists, and the test
	// does not end before the call has returned.
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(t.TempDir(), "draw-prompt")
	writeFakePi(t, filepath.Dir(pi), `if [ "$1" = "--version" ]; then echo 1.3.0; exit 0; fi; while [ ! -e `+release+` ]; do sleep 0.1; done; printf '$ '; exec sleep 300`)
	done := make(chan error, 1)
	t.Cleanup(func() {
		if err := os.WriteFile(release, nil, 0o644); err != nil {
			t.Error(err)
			return
		}
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Error("StartWithMessage was still waiting for a prompt 60 s after the fake pi drew one")
		}
		killTmuxSessionsNamed(t, "pi-start")
		joinSpawnWatchers(t, inst)
	})
	go func() { done <- inst.StartWithMessage("get started") }()
	deadline := time.Now().Add(20 * time.Second)
	for !tmuxHasSessionNamed("pi-start") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !tmuxHasSessionNamed("pi-start") {
		t.Fatal("test setup: the pane never spawned")
	}
	// Spawned. The call is now waiting for a prompt that will not come.
	for storedPiSocketExported(t, storage, inst.ID) == false && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !storedPiSocketExported(t, storage, inst.ID) {
		t.Error("the pane spawned with the socket exported, but storage still holds false: the decision waits for StartWithMessage to return")
	}
	select {
	case err := <-done:
		t.Fatalf("test setup: StartWithMessage returned (%v) instead of waiting for a prompt", err)
	default:
	}
}

// joinSpawnWatchers ends the goroutines a spawn left behind (the fast-death
// watcher and the ownership pass) before the test returns, as
// stopShellTestInstance does: they poll tmux for the spawn window, and a poll
// after the test lands in a later test's tmux start count.
func joinSpawnWatchers(t *testing.T, inst *Instance) {
	t.Helper()
	inst.bumpSpawnGenAndBarrier()
	joined := make(chan struct{})
	go func() {
		inst.waitForFastDeathWatchers()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(shellTestJoinDeadline):
		t.Errorf("spawn goroutines of %s still running %v after teardown", inst.ID, shellTestJoinDeadline)
	}
}

// tmuxHasSessionNamed reports whether the isolated tmux server holds a session
// whose name contains part.
func tmuxHasSessionNamed(part string) bool {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	return err == nil && strings.Contains(string(out), part)
}

// killTmuxSessionsNamed ends the test's tmux sessions whose name contains part,
// on the package's isolated tmux server (TestMain's TMUX_TMPDIR).
func killTmuxSessionsNamed(t *testing.T, part string) {
	t.Helper()
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(out)) {
		if strings.Contains(name, part) {
			_ = exec.Command("tmux", "kill-session", "-t", name).Run()
		}
	}
}

// A failure before the pane spawned launched nothing: storage keeps what it had.
// The tmux start fails here because there is no tmux binary; the launch
// had already decided to drop the socket (pi 1.1.0) in memory.
func TestPiStart_APreSpawnFailureLeavesStorageUnchanged(t *testing.T) {
	storage, inst := piStartFixture(t, "1.1.0", true)
	// With only the fake pi on PATH there is no tmux to spawn the pane with.
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(pi))
	if err := inst.Start(); err == nil {
		t.Fatal("test setup: the start succeeded without a tmux binary")
	}
	t.Cleanup(func() {
		if sess := inst.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})
	if inst.PiSocketExported {
		t.Fatal("test setup: the failed launch never reached the decision, so the test proves nothing")
	}
	if got := storedPiSocketExported(t, storage, inst.ID); !got {
		t.Error("a failed spawn overwrote the stored decision: nothing was launched")
	}
}

// A start that loses the spawn race (another process spawned while this one
// waited for the lock) returns nil without launching, and must not write its own
// stale view of the flag over the winner's.
func TestPiStart_ALostSpawnRaceDoesNotOverwriteTheWinnersFlag(t *testing.T) {
	storage, inst := piStartFixture(t, "1.3.0", false) // this instance's view: false
	release, err := acquireInstanceSpawnLock(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- inst.Start() }() // waits for the lock behind us
	time.Sleep(300 * time.Millisecond)
	// The winner spawns and records that its launch exported the socket.
	recordInstanceSpawn(inst.ID)
	if err := storage.GetDB().WritePiSocketExported(inst.ID, true); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("the loser of the race returned %v, want nil", err)
	}
	if got := storedPiSocketExported(t, storage, inst.ID); !got {
		t.Error("the loser overwrote the winner's PiSocketExported = true with its stale false")
	}
}

// StartWithMessage mirrors Start: a failure before the pane spawned launched
// nothing, so storage keeps what it had.
func TestPiStartWithMessage_APreSpawnFailureLeavesStorageUnchanged(t *testing.T) {
	storage, inst := piStartFixture(t, "1.1.0", true)
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(pi)) // no tmux to spawn the pane with
	if err := inst.StartWithMessage("get started"); err == nil {
		t.Fatal("test setup: the start succeeded without a tmux binary")
	}
	if inst.PiSocketExported {
		t.Fatal("test setup: the failed launch never reached the decision, so the test proves nothing")
	}
	if got := storedPiSocketExported(t, storage, inst.ID); !got {
		t.Error("a failed spawn overwrote the stored decision: nothing was launched")
	}
}

// A StartWithMessage that loses the spawn race returns nil without launching and
// must not write its stale view of the flag over the winner's.
func TestPiStartWithMessage_ALostSpawnRaceDoesNotOverwriteTheWinnersFlag(t *testing.T) {
	storage, inst := piStartFixture(t, "1.3.0", false) // this instance's view: false
	release, err := acquireInstanceSpawnLock(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- inst.StartWithMessage("get started") }() // waits for the lock behind us
	time.Sleep(300 * time.Millisecond)
	recordInstanceSpawn(inst.ID)
	if err := storage.GetDB().WritePiSocketExported(inst.ID, true); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("the loser of the race returned %v, want nil", err)
	}
	if got := storedPiSocketExported(t, storage, inst.ID); !got {
		t.Error("the loser overwrote the winner's PiSocketExported = true with its stale false")
	}
}

// ---- round 6: a bounded duplicate, pinned -------------------------------------

// The consumer file remembers the last few injections. A receipt for one that
// has been forgotten still accepts the ledger attempt bound to its token, but it
// cannot name the inbox records, so they are not consumed: they ride the next run
// start once more (at-least-once, bounded). Pinned so a change to the bound is a
// decision, not an accident.
func TestPiInboxPeek_AnEvictedTokensReceiptAcceptsTheAttemptAndReShowsTheInboxRecordOnce(t *testing.T) {
	f := newPiParentFixture(t)
	rec := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "ledger result"})
	f.inboxOnly(t, "inbox record R")
	first, ok := LedgerPromptContext(f.parent.ID, "carry on", "")
	if !ok || !strings.Contains(first.Text, "ledger result") || !strings.Contains(first.Text, "inbox record R") {
		t.Fatalf("first injection (ok %v): %v", ok, first)
	}
	evicted := tokenOf(t, first)
	first.Done(true)
	// Enough later injections, each carrying a new ledger record (the inbox
	// collapses records of one child, so R must stay the only inbox record), to
	// push the first token out of the remembered set.
	for i := 0; i < 9; i++ {
		f.clock = f.clock.Add(time.Second) // eviction drops the oldest by time
		f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "later ledger record " + strings.Repeat("x", i+1)})
		dl, _ := LedgerPromptContext(f.parent.ID, "carry on", "")
		if dl == nil {
			t.Fatal("no injection")
		}
		dl.Done(true)
	}
	f.injectedEdge(t, evicted)
	if in := inflightFor(f.inflight(t), rec.ID); in == nil || in["state"] != comms.ReceiptTransportAccepted {
		t.Errorf("the evicted token's receipt did not accept the ledger attempt: %v", f.inflight(t))
	}
	next, _ := LedgerPromptContext(f.parent.ID, "carry on again", "")
	if next == nil || strings.Count(next.Text, "inbox record R") != 1 {
		t.Fatalf("the inbox record must be shown once more after an evicted receipt: %+v", next)
	}
	if strings.Contains(next.Text, "ledger result") {
		t.Errorf("the accepted ledger record was injected again: %q", next.Text)
	}
	next.Done(true)
}
