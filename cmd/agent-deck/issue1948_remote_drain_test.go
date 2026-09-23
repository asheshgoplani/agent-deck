package main

// Issue #1948 — `agent-deck remote drain <remote>`: the conductor-side PULL.
//
// What these pin:
//   - a remote completion lands in THIS machine's inbox (the bug: it never did);
//   - draining twice does not double-write — the inbox's existing fingerprint
//     dedup decides, this command only reports the answer;
//   - an unknown remote, an unreachable remote and a reachable-but-empty remote
//     produce three distinguishable messages and exit codes, so "nothing to
//     report" can never be confused with "I never asked" or "no answer".

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type issue1952FailWriter struct{}

func (issue1952FailWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestIssue1952_OutputFailuresAreNotSuccess(t *testing.T) {
	if err := runInbox(issue1952FailWriter{}, nil); err == nil {
		t.Fatal("inbox usage write failure reported success")
	}
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	registerDrainTarget(t, "conductor-output-failure")
	fetch, _ := stubFetch(nil, nil)
	if code := runRemoteDrain(issue1952FailWriter{}, &bytes.Buffer{}, []string{"--into", "conductor-output-failure", "boxb"}, fetch); code == 0 {
		t.Fatal("remote drain output failure reported success")
	}
}

func drainTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	session.ResetInboxFingerprintCacheForTest()
	oldProbe := remoteWriterProbe
	remoteWriterProbe = func(context.Context, session.RemoteConfig, string) (session.WriterStatus, error) {
		return session.WriterStatus{Running: true}, nil
	}
	t.Cleanup(func() { remoteWriterProbe = oldProbe })
}

// registerDrainTarget keeps these remote-drain integration tests aligned with
// the destructive drain resolver: a drain may only target a session that is
// present in the registry. The fail-closed resolver added by #2038 deliberately
// rejects the arbitrary IDs these tests used before that contract existed.
func registerDrainTarget(t *testing.T, id string) {
	t.Helper()
	inst := session.NewInstance(id, t.TempDir())
	inst.ID = id
	saveInboxResolutionSessions(t, "default", inst)
}

// configureRemote writes a user config with one remote, as `remote add` would.
func configureRemote(t *testing.T, name, host string) {
	t.Helper()
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		cfg = &session.UserConfig{}
	}
	if cfg.Remotes == nil {
		cfg.Remotes = map[string]session.RemoteConfig{}
	}
	cfg.Remotes[name] = session.RemoteConfig{Host: host}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func remoteCompletion(child, summary string, at time.Time) session.TransitionNotificationEvent {
	return session.TransitionNotificationEvent{
		ChildSessionID: child,
		ChildTitle:     "remote-worker",
		Profile:        "default",
		Kind:           "finished",
		DoneStatus:     "ok",
		DoneSummary:    summary,
		Timestamp:      at,
	}
}

func stubFetch(records []session.TransitionNotificationEvent, err error) (remoteRecordFetcher, *int) {
	calls := 0
	return func(ctx context.Context, name string, rc session.RemoteConfig) ([]session.TransitionNotificationEvent, error) {
		calls++
		return records, err
	}, &calls
}

func TestIssue2038_RemoteDrainRefusesTargetBeforeRemoteIO(t *testing.T) {
	for _, tc := range []struct {
		name     string
		target   string
		wantCode int
		setup    func(*testing.T, string)
	}{
		{name: "missing", target: "missing-target", wantCode: 2},
		{name: "ambiguous", target: "abcdef", wantCode: 3, setup: func(t *testing.T, _ string) {
			a := session.NewInstance("alpha", t.TempDir())
			b := session.NewInstance("beta", t.TempDir())
			a.ID, b.ID = "abcdef01-1777000200", "abcdef02-1777000201"
			saveInboxResolutionSessions(t, "default", a, b)
		}},
		{name: "corrupt-profile", target: "healthy-target", wantCode: 1, setup: func(t *testing.T, target string) {
			inst := session.NewInstance(target, t.TempDir())
			inst.ID = target
			saveInboxResolutionSessions(t, "default", inst)
			dbPath, err := session.GetDBPathForProfile("aaa-corrupt")
			if err != nil {
				t.Fatalf("corrupt profile path: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
				t.Fatalf("mkdir corrupt profile: %v", err)
			}
			if err := os.WriteFile(dbPath, []byte("not sqlite"), 0o600); err != nil {
				t.Fatalf("write corrupt profile: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drainTestHome(t)
			t.Setenv("AGENTDECK_PROFILE", "default")
			configureRemote(t, "boxb", "worker@box-b")
			if tc.setup != nil {
				tc.setup(t, tc.target)
			}
			if err := session.CommitToInbox(tc.target, session.TransitionNotificationEvent{ChildSessionID: "must-survive"}); err != nil {
				t.Fatalf("seed inbox: %v", err)
			}
			fetch, calls := stubFetch([]session.TransitionNotificationEvent{
				remoteCompletion("remote-child", "must-not-land", time.Now()),
			}, nil)
			probeCalls := 0
			oldProbe := remoteWriterProbe
			remoteWriterProbe = func(context.Context, session.RemoteConfig, string) (session.WriterStatus, error) {
				probeCalls++
				return session.WriterStatus{Running: true}, nil
			}
			t.Cleanup(func() { remoteWriterProbe = oldProbe })

			var stdout, stderr bytes.Buffer
			code := runRemoteDrain(&stdout, &stderr, []string{"--into", tc.target, "boxb"}, fetch)
			if code != tc.wantCode {
				t.Fatalf("exit=%d, want %d; stderr=%s", code, tc.wantCode, stderr.String())
			}
			if *calls != 0 || probeCalls != 0 {
				t.Fatalf("refusal performed remote I/O: fetches=%d probes=%d", *calls, probeCalls)
			}
			events, err := session.ReadInboxEvents(tc.target)
			if err != nil || len(events) != 1 || events[0].ChildSessionID != "must-survive" {
				t.Fatalf("refusal mutated inbox: events=%+v err=%v", events, err)
			}
		})
	}
}

func TestIssue1952_RemoteDrainStalledWriterNeverReportsFinished(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	const conductor = "conductor-stalled"
	registerDrainTarget(t, conductor)
	fetch, calls := stubFetch([]session.TransitionNotificationEvent{
		remoteCompletion("remote-child", "looks complete in a partial reply", time.Now()),
	}, nil)
	oldProbe := remoteWriterProbe
	remoteWriterProbe = func(context.Context, session.RemoteConfig, string) (session.WriterStatus, error) {
		return session.WriterStatus{Detail: "fake remote stopped responding mid-drain"}, nil
	}
	t.Cleanup(func() { remoteWriterProbe = oldProbe })

	var stdout, stderr bytes.Buffer
	code := runRemoteDrain(&stdout, &stderr, []string{"--into", conductor, "boxb"}, fetch)
	if code == 0 {
		t.Fatal("stalled remote drain returned success")
	}
	if *calls != 1 {
		t.Fatalf("fake remote fetch calls=%d, want 1", *calls)
	}
	if !strings.Contains(stderr.String(), "STALLED") || !strings.Contains(stderr.String(), "stopped responding mid-drain") {
		t.Fatalf("stalled status is not explicit: %s", stderr.String())
	}
	if strings.Contains(stdout.String(), "finished") || session.InboxHasPending(conductor) {
		t.Fatalf("stalled partial reply was classified or written as finished: stdout=%s", stdout.String())
	}
}

func TestIssue1952_RemoteDrainFailedWriterProbeNeverReportsFinished(t *testing.T) {
	for _, failure := range []struct{ name, message string }{
		{name: "command error", message: "writer-status command failed: ssh disconnected"},
		{name: "empty output", message: "writer-status command returned no output"},
		{name: "corrupt JSON", message: "writer-status command returned corrupt JSON"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			drainTestHome(t)
			configureRemote(t, "boxb", "worker@box-b")
			const conductor = "conductor-unverified"
			registerDrainTarget(t, conductor)
			fetch, calls := stubFetch([]session.TransitionNotificationEvent{
				remoteCompletion("remote-child", "looks complete in a partial reply", time.Now()),
			}, nil)
			remoteWriterProbe = func(context.Context, session.RemoteConfig, string) (session.WriterStatus, error) {
				return session.WriterStatus{}, errors.New(failure.message)
			}

			var stdout, stderr bytes.Buffer
			code := runRemoteDrain(&stdout, &stderr, []string{"--into", conductor, "boxb"}, fetch)
			if code != drainExitUnreachable {
				t.Fatalf("exit=%d, want %d; stderr=%s", code, drainExitUnreachable, stderr.String())
			}
			if *calls != 1 {
				t.Fatalf("fetch calls=%d, want 1", *calls)
			}
			if !strings.Contains(stderr.String(), "UNVERIFIED") || !strings.Contains(stderr.String(), "writer-status probe failed") {
				t.Fatalf("probe failure is not explicit: %s", stderr.String())
			}
			if !strings.Contains(stderr.String(), failure.message) {
				t.Fatalf("probe failure shape is missing: %s", stderr.String())
			}
			if stdout.Len() != 0 || session.InboxHasPending(conductor) {
				t.Fatalf("partial reply was printed or written: stdout=%s", stdout.String())
			}
		})
	}
}

// The headline: a completion that only ever existed on the remote host is in
// the conductor's LOCAL inbox after one drain.
func TestIssue1948_RemoteDrain_WritesRemoteCompletionIntoLocalInbox(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-local"
	registerDrainTarget(t, conductor)

	fetch, _ := stubFetch([]session.TransitionNotificationEvent{
		remoteCompletion("worker-on-b", "migration finished", time.Now()),
	}, nil)

	var stdout, stderr bytes.Buffer
	if code := runRemoteDrain(&stdout, &stderr, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("drain exit=%d stderr=%s", code, stderr.String())
	}

	pending, err := session.ReadInboxEvents(conductor)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	// The stored id names its host (review round 2): a bare child id is unique
	// only on the machine that minted it.
	if len(pending) != 1 || pending[0].ChildSessionID != "boxb:worker-on-b" {
		t.Fatalf("remote completion did not reach the local inbox: %+v", pending)
	}
	if pending[0].SourceRemote != "boxb" {
		t.Fatalf("pulled record must name the remote it came from: %+v", pending[0])
	}
	if pending[0].TargetSessionID != conductor {
		t.Fatalf("pulled record must be addressed to the draining conductor: %+v", pending[0])
	}
	if !strings.Contains(stdout.String(), "1 record(s) fetched, 1 new (shown above), 0 already present") {
		t.Fatalf("drain report unclear:\n%s", stdout.String())
	}
	// The record itself is listed, because it is genuinely new on this run.
	if !strings.Contains(stdout.String(), "worker-on-b") {
		t.Fatalf("a newly committed record must be shown, not just counted:\n%s", stdout.String())
	}

	// And the conductor's normal consumption path surfaces it.
	var drainOut bytes.Buffer
	if err := runInbox(&drainOut, []string{"drain", conductor}); err != nil {
		t.Fatalf("inbox drain: %v", err)
	}
	if !strings.Contains(drainOut.String(), "worker-on-b") {
		t.Fatalf("conductor's inbox drain missed the pulled record:\n%s", drainOut.String())
	}
}

// Draining twice must not double-write. The remote is unchanged (it is
// read-only, so it keeps answering with the same record) and the local inbox
// refuses the duplicate on its existing fingerprint dedup.
func TestIssue1948_RemoteDrain_SecondDrainIsIdempotent(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-idem"
	registerDrainTarget(t, conductor)

	record := remoteCompletion("worker-on-b", "migration finished", time.Now())
	fetch, calls := stubFetch([]session.TransitionNotificationEvent{record}, nil)

	var out1, err1 bytes.Buffer
	if code := runRemoteDrain(&out1, &err1, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("first drain exit=%d: %s", code, err1.String())
	}
	var out2, err2 bytes.Buffer
	if code := runRemoteDrain(&out2, &err2, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("second drain exit=%d: %s", code, err2.String())
	}

	if *calls != 2 {
		t.Fatalf("both drains must query the remote, got %d calls", *calls)
	}
	pending, err := session.ReadInboxEvents(conductor)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("second drain double-wrote: inbox holds %d records: %+v", len(pending), pending)
	}
	if !strings.Contains(out2.String(), "1 record(s) fetched, nothing new — all already present") {
		t.Fatalf("second drain must report the duplicate explicitly:\n%s", out2.String())
	}
	// Field finding 2026-08-20: a drain that committed nothing must not re-print
	// the backlog as though it had just arrived. The remote is read-only, so it
	// keeps serving the same records on every heartbeat, and listing them again
	// made routine polling look like a stream of new work.
	if strings.Contains(out2.String(), "worker-on-b") {
		t.Fatalf("a drain that committed nothing must not re-list already-present records:\n%s", out2.String())
	}
}

// Idempotence must survive a FRESH PROCESS (the real shape: each drain is its
// own `agent-deck` invocation, so the in-process fingerprint cache is empty and
// dedup has to come off disk).
func TestIssue1948_RemoteDrain_IdempotentAcrossProcesses(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-fresh"
	registerDrainTarget(t, conductor)

	record := remoteCompletion("worker-on-b", "done", time.Now())
	fetch, _ := stubFetch([]session.TransitionNotificationEvent{record}, nil)

	var out, errBuf bytes.Buffer
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("first drain exit=%d: %s", code, errBuf.String())
	}

	session.ResetInboxFingerprintCacheForTest() // simulate a new process
	out.Reset()
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("second drain exit=%d: %s", code, errBuf.String())
	}

	pending, err := session.ReadInboxEvents(conductor)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("fresh-process drain double-wrote: %d records", len(pending))
	}
}

// The durable consumed-turn ledger, not the now-empty inbox file, is the source
// of truth after a conductor has acted on a record. A later remote poll must not
// call that old turn new or put it back into the inbox.
func TestIssue1948_RemoteDrain_AfterConsumptionReportsAlreadyPresent(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-consumed"
	registerDrainTarget(t, conductor)
	record := remoteCompletion("worker-on-b", "done hours ago", time.Now().Add(-3*time.Hour))
	fetch, _ := stubFetch([]session.TransitionNotificationEvent{record}, nil)

	var out, errBuf bytes.Buffer
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("first drain exit=%d: %s", code, errBuf.String())
	}
	if _, err := session.DrainInboxForParent(conductor); err != nil {
		t.Fatalf("consume first drain: %v", err)
	}
	session.ResetInboxFingerprintCacheForTest()
	out.Reset()
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("second drain exit=%d: %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "1 record(s) fetched, nothing new — all already present") {
		t.Fatalf("consumed record counted dishonestly:\n%s", out.String())
	}
	if session.InboxHasPending(conductor) {
		t.Fatal("already-consumed remote record was reinserted")
	}
}

// Reviewer R2B's exact backup/restore sequence: a valid ledger backup is made
// before A arrives, A is pulled and consumed, the old ledger is restored, and
// a fresh process pulls the unchanged window-inside export again. The marker
// beside the inbox must expose the rollback; producer time cannot reconstruct
// the forgotten consumption history.
func TestIssue1948_RemoteDrain_OlderLedgerRestoreIsUnknownWithWarning(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-restored"
	registerDrainTarget(t, conductor)
	if err := os.MkdirAll(session.ConsumedTurnsDir(), 0o755); err != nil {
		t.Fatalf("mkdir consumed ledger: %v", err)
	}
	ledgerPath := filepath.Join(session.ConsumedTurnsDir(), conductor+".json")
	backup := []byte("{}")
	if err := os.WriteFile(ledgerPath, backup, 0o644); err != nil {
		t.Fatalf("create pre-A ledger backup: %v", err)
	}

	record := remoteCompletion("worker-on-b", "done one hour ago", time.Now().Add(-time.Hour))
	fetch, _ := stubFetch([]session.TransitionNotificationEvent{record}, nil)
	var out, errBuf bytes.Buffer
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("first drain exit=%d: %s", code, errBuf.String())
	}
	if events, err := session.DrainInboxForParent(conductor); err != nil || len(events) != 1 {
		t.Fatalf("consume A: events=%d err=%v", len(events), err)
	}
	if err := os.WriteFile(ledgerPath, backup, 0o644); err != nil {
		t.Fatalf("restore pre-A ledger backup: %v", err)
	}
	session.ResetInboxFingerprintCacheForTest()
	out.Reset()
	errBuf.Reset()
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("post-restore drain exit=%d: %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "1 record(s) fetched, 0 new, 0 already present, 1 unknown") {
		t.Fatalf("forgotten A was not unknown:\n%s", out.String())
	}
	if !strings.Contains(errBuf.String(), "detected consumed-ledger restore for inbox "+conductor) {
		t.Fatalf("restore warning missing or unnamed:\n%s", errBuf.String())
	}
	if strings.Count(errBuf.String(), "detected consumed-ledger restore") != 1 {
		t.Fatalf("restore warning must be one line per drain:\n%s", errBuf.String())
	}
	if session.InboxHasPending(conductor) {
		t.Fatal("forgotten A was reinserted after ledger restore")
	}
}

func TestIssue1948_RemoteDrain_MixedConsumedAndNewReportsHonestSplit(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-mixed"
	registerDrainTarget(t, conductor)
	old := remoteCompletion("worker-old", "old turn", time.Now().Add(-3*time.Hour))
	first, _ := stubFetch([]session.TransitionNotificationEvent{old}, nil)
	var out, errBuf bytes.Buffer
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, first); code != 0 {
		t.Fatalf("first drain exit=%d: %s", code, errBuf.String())
	}
	if _, err := session.DrainInboxForParent(conductor); err != nil {
		t.Fatalf("consume first drain: %v", err)
	}

	newRecord := remoteCompletion("worker-new", "new turn", time.Now())
	mixed, _ := stubFetch([]session.TransitionNotificationEvent{old, newRecord}, nil)
	out.Reset()
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, mixed); code != 0 {
		t.Fatalf("mixed drain exit=%d: %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "2 record(s) fetched, 1 new (shown above), 1 already present") {
		t.Fatalf("mixed split dishonest:\n%s", out.String())
	}
	if strings.Contains(out.String(), "worker-old") || !strings.Contains(out.String(), "worker-new") {
		t.Fatalf("mixed drain must show only the inserted record:\n%s", out.String())
	}
}

func TestIssue1948_RemoteDrain_UnreadableDedupStateIsUnknownNotNew(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-unknown"
	registerDrainTarget(t, conductor)
	if err := os.MkdirAll(session.ConsumedTurnsDir(), 0o755); err != nil {
		t.Fatalf("mkdir consumed ledger: %v", err)
	}
	ledgerPath := filepath.Join(session.ConsumedTurnsDir(), conductor+".json")
	if err := os.WriteFile(ledgerPath, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("corrupt consumed ledger: %v", err)
	}

	record := remoteCompletion("worker-on-b", "state unknowable", time.Now())
	fetch, _ := stubFetch([]session.TransitionNotificationEvent{record}, nil)
	var out, errBuf bytes.Buffer
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("drain exit=%d: %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "1 record(s) fetched, 0 new, 0 already present, 1 unknown") {
		t.Fatalf("unknown dedup state folded into another count:\n%s", out.String())
	}
	if session.InboxHasPending(conductor) {
		t.Fatal("record with unknown dedup state must not be inserted")
	}
}

// A read-only remote can keep exporting a record after the bounded consumed
// ledger prunes its fingerprint. The record's own age must keep that stale turn
// from being resurrected as fresh work.
func TestIssue1948_RemoteDrain_StaleExportCannotReplayAfterConsumedPrune(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-pruned"
	registerDrainTarget(t, conductor)
	stale := remoteCompletion("worker-old", "done weeks ago", time.Now().Add(-15*24*time.Hour))
	stale.SourceRemote = "boxb"
	stale.ChildSessionID = session.RemoteScopedChildID("boxb", stale.ChildSessionID)
	stale.TurnFingerprint = session.TurnFingerprint(stale)

	if err := os.MkdirAll(session.ConsumedTurnsDir(), 0o755); err != nil {
		t.Fatalf("mkdir consumed ledger: %v", err)
	}
	ledgerPath := filepath.Join(session.ConsumedTurnsDir(), conductor+".json")
	ledger := map[string]int64{stale.TurnFingerprint: time.Now().Add(-15 * 24 * time.Hour).Unix()}
	raw, err := json.Marshal(ledger)
	if err != nil {
		t.Fatalf("marshal consumed ledger: %v", err)
	}
	if err := os.WriteFile(ledgerPath, raw, 0o644); err != nil {
		t.Fatalf("seed consumed ledger: %v", err)
	}

	// Consuming another turn exercises the production save/prune path and
	// removes the stale fingerprint before the remote serves it again.
	fresh := remoteCompletion("worker-new", "new turn", time.Now())
	fresh.SourceRemote = "boxb"
	fresh.ChildSessionID = session.RemoteScopedChildID("boxb", fresh.ChildSessionID)
	fresh.TurnFingerprint = session.TurnFingerprint(fresh)
	if _, err := session.WriteInboxEventIfUnseen(conductor, fresh); err != nil {
		t.Fatalf("stage fresh turn: %v", err)
	}
	if _, err := session.DrainInboxForParent(conductor); err != nil {
		t.Fatalf("consume fresh turn and prune ledger: %v", err)
	}
	raw, err = os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read pruned ledger: %v", err)
	}
	var pruned map[string]int64
	if err := json.Unmarshal(raw, &pruned); err != nil {
		t.Fatalf("decode pruned ledger: %v", err)
	}
	if _, ok := pruned[stale.TurnFingerprint]; ok {
		t.Fatal("test setup failed: production save did not prune stale fingerprint")
	}

	// Undo the local ingest scoping above: the fetch seam returns the unchanged
	// remote representation and production ingest scopes it exactly once.
	remoteStale := remoteCompletion("worker-old", "done weeks ago", stale.Timestamp)
	fetch, _ := stubFetch([]session.TransitionNotificationEvent{remoteStale}, nil)
	var out, errBuf bytes.Buffer
	if code := runRemoteDrain(&out, &errBuf, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("stale re-drain exit=%d: %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "nothing new — all already present") {
		t.Fatalf("stale export was not classified already-present:\n%s", out.String())
	}
	if session.InboxHasPending(conductor) {
		t.Fatal("stale exported record was reinserted after ledger pruning")
	}
}

func TestIssue1948_RemoteDrain_UncertainTimestampIsUnknownNotNew(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{name: "missing", at: time.Time{}},
		{name: "future-clock-skew", at: time.Now().Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drainTestHome(t)
			configureRemote(t, "boxb", "worker@box-b")
			registerDrainTarget(t, "conductor-skew")
			fetch, _ := stubFetch([]session.TransitionNotificationEvent{
				remoteCompletion("worker-on-b", "age unknown", tc.at),
			}, nil)
			var out, errBuf bytes.Buffer
			if code := runRemoteDrain(&out, &errBuf, []string{"--into", "conductor-skew", "boxb"}, fetch); code != 0 {
				t.Fatalf("drain exit=%d: %s", code, errBuf.String())
			}
			if !strings.Contains(out.String(), "0 new, 0 already present, 1 unknown") {
				t.Fatalf("uncertain age was not unknown:\n%s", out.String())
			}
			if session.InboxHasPending("conductor-skew") {
				t.Fatal("record with uncertain age was inserted")
			}
		})
	}
}

// An unknown host is reported as unknown — with the configured remotes listed —
// and never as an empty drain.
func TestIssue1948_RemoteDrain_UnknownRemoteIsDistinguishable(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")

	fetch, calls := stubFetch(nil, nil)
	var stdout, stderr bytes.Buffer
	code := runRemoteDrain(&stdout, &stderr, []string{"--into", "c", "typo-host"}, fetch)

	if code != drainExitUsage {
		t.Fatalf("unknown remote exit=%d, want %d", code, drainExitUsage)
	}
	if *calls != 0 {
		t.Fatalf("unknown remote must not be contacted")
	}
	msg := stderr.String()
	if !strings.Contains(msg, "unknown remote 'typo-host'") {
		t.Fatalf("unknown-remote message unclear:\n%s", msg)
	}
	if !strings.Contains(msg, "boxb") || !strings.Contains(msg, "worker@box-b") {
		t.Fatalf("unknown-remote message should list what IS configured:\n%s", msg)
	}
	if strings.Contains(stdout.String(), "no completion") {
		t.Fatalf("unknown remote must not read as an empty drain:\n%s", stdout.String())
	}
}

// With no remotes at all the message says so, rather than blaming the name.
func TestIssue1948_RemoteDrain_NoRemotesConfigured(t *testing.T) {
	drainTestHome(t)

	fetch, _ := stubFetch(nil, nil)
	var stdout, stderr bytes.Buffer
	code := runRemoteDrain(&stdout, &stderr, []string{"--into", "c", "boxb"}, fetch)

	if code != drainExitUsage {
		t.Fatalf("exit=%d, want %d", code, drainExitUsage)
	}
	if !strings.Contains(stderr.String(), "no remotes are configured") {
		t.Fatalf("message unclear:\n%s", stderr.String())
	}
}

// An ssh failure is loud and has its own exit code: silence here would look
// exactly like "your fleet has nothing to report".
func TestIssue1948_RemoteDrain_UnreachableRemoteIsDistinguishable(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	registerDrainTarget(t, "c")

	fetch, _ := stubFetch(nil, errors.New("ssh command failed: exit status 255: connection refused"))
	var stdout, stderr bytes.Buffer
	code := runRemoteDrain(&stdout, &stderr, []string{"--into", "c", "boxb"}, fetch)

	if code != drainExitUnreachable {
		t.Fatalf("unreachable exit=%d, want %d", code, drainExitUnreachable)
	}
	if code == drainExitUsage {
		t.Fatalf("unreachable must not share the unknown-remote code")
	}
	msg := stderr.String()
	if !strings.Contains(msg, "could not be drained") || !strings.Contains(msg, "connection refused") {
		t.Fatalf("unreachable message must carry the ssh failure:\n%s", msg)
	}
	if !strings.Contains(msg, "NOT an empty inbox") {
		t.Fatalf("unreachable message must not be mistakable for an empty drain:\n%s", msg)
	}
}

// Reachable but empty is a real answer and says so — exit 0, explicit wording.
func TestIssue1948_RemoteDrain_ReachableButEmptySaysSo(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	registerDrainTarget(t, "c")

	fetch, _ := stubFetch(nil, nil)
	var stdout, stderr bytes.Buffer
	code := runRemoteDrain(&stdout, &stderr, []string{"--into", "c", "boxb"}, fetch)

	if code != 0 {
		t.Fatalf("reachable-but-empty exit=%d, want 0 (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Reachable") || !strings.Contains(out, "no completion or transition records") {
		t.Fatalf("empty drain must be explicit, got:\n%s", out)
	}
}

// The remote can also be named by its host string, the spelling #1948 uses.
func TestIssue1948_RemoteDrain_ResolvesRemoteByHost(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	registerDrainTarget(t, "c")

	fetch, calls := stubFetch([]session.TransitionNotificationEvent{
		remoteCompletion("worker-on-b", "ok", time.Now()),
	}, nil)
	var stdout, stderr bytes.Buffer
	if code := runRemoteDrain(&stdout, &stderr, []string{"--into", "c", "worker@box-b"}, fetch); code != 0 {
		t.Fatalf("exit=%d: %s", code, stderr.String())
	}
	if *calls != 1 {
		t.Fatalf("host spelling should resolve to the configured remote")
	}
}

// --json gives a conductor's heartbeat a machine-readable result.
func TestIssue1948_RemoteDrain_JSONShape(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-json"
	registerDrainTarget(t, conductor)

	fetch, _ := stubFetch([]session.TransitionNotificationEvent{
		remoteCompletion("worker-on-b", "ok", time.Now()),
	}, nil)
	var stdout, stderr bytes.Buffer
	if code := runRemoteDrain(&stdout, &stderr, []string{"--json", "--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("exit=%d: %s", code, stderr.String())
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	var shaped remoteDrainResult
	if err := json.Unmarshal(stdout.Bytes(), &shaped); err != nil {
		t.Fatalf("typed json: %v\n%s", err, stdout.String())
	}
	if shaped.Remote != "boxb" || shaped.Host != "worker@box-b" || shaped.TargetSessionID != conductor || len(shaped.Records) != 1 {
		t.Fatalf("json identity/records wrong: %+v", shaped)
	}
	var unknown int
	if raw, ok := got["unknown"]; !ok || json.Unmarshal(raw, &unknown) != nil || unknown != 0 {
		t.Fatalf("json must explicitly emit unknown: 0: %s", stdout.String())
	}
	for key, want := range map[string]int{"fetched": 1, "written": 1, "duplicates": 0} {
		var value int
		if raw, ok := got[key]; !ok || json.Unmarshal(raw, &value) != nil || value != want {
			t.Fatalf("json %s=%d missing/wrong: %s", key, want, stdout.String())
		}
	}
}

func TestIssue1948_RemoteDrain_JSONPinsUnknownFieldOnUnreadableLedger(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-json-unknown"
	registerDrainTarget(t, conductor)
	if err := os.MkdirAll(session.ConsumedTurnsDir(), 0o755); err != nil {
		t.Fatalf("mkdir consumed ledger: %v", err)
	}
	if err := os.WriteFile(filepath.Join(session.ConsumedTurnsDir(), conductor+".json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("corrupt consumed ledger: %v", err)
	}
	fetch, _ := stubFetch([]session.TransitionNotificationEvent{
		remoteCompletion("worker-on-b", "unknown", time.Now()),
	}, nil)
	var stdout, stderr bytes.Buffer
	if code := runRemoteDrain(&stdout, &stderr, []string{"--json", "--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("exit=%d: %s", code, stderr.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	for key, want := range map[string]int{"fetched": 1, "written": 0, "duplicates": 0, "unknown": 1} {
		var value int
		if raw, ok := got[key]; !ok || json.Unmarshal(raw, &value) != nil || value != want {
			t.Fatalf("json %s=%d missing/wrong: %s", key, want, stdout.String())
		}
	}
	if session.InboxHasPending(conductor) {
		t.Fatal("unknown record was inserted")
	}
}

// The remote side of the wire: `inbox export --json` is what the drain runs
// over ssh. It must emit a JSON array (the shape FetchPendingRecords parses)
// and must not consume the inbox it read.
func TestIssue1948_InboxExportCLI_EmitsArrayAndConsumesNothing(t *testing.T) {
	drainTestHome(t)
	parent := "parent-on-the-remote"
	registerDrainTarget(t, parent)

	if err := session.CommitToInbox(parent, session.TransitionNotificationEvent{
		ChildSessionID: "worker-on-b", ChildTitle: "migrate", Profile: "default",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var buf bytes.Buffer
	if err := runInbox(&buf, []string{"export", "--json"}); err != nil {
		t.Fatalf("inbox export: %v", err)
	}
	var records []session.TransitionNotificationEvent
	if err := json.Unmarshal(buf.Bytes(), &records); err != nil {
		t.Fatalf("export must emit a JSON array: %v\n%s", err, buf.String())
	}
	if len(records) != 1 || records[0].ChildSessionID != "worker-on-b" {
		t.Fatalf("exported records wrong: %+v", records)
	}

	// Nothing consumed: the host's own conductor still drains it.
	var drainOut bytes.Buffer
	if err := runInbox(&drainOut, []string{"drain", parent}); err != nil {
		t.Fatalf("local drain: %v", err)
	}
	if !strings.Contains(drainOut.String(), "worker-on-b") {
		t.Fatalf("export consumed the remote's own inbox:\n%s", drainOut.String())
	}
}

// With nothing to report, export still emits a valid empty array rather than
// nothing at all — the drain parses this, and "[]" is a real answer.
func TestIssue1948_InboxExportCLI_EmptyIsAnEmptyArray(t *testing.T) {
	drainTestHome(t)

	var buf bytes.Buffer
	if err := runInbox(&buf, []string{"export", "--json"}); err != nil {
		t.Fatalf("inbox export: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Fatalf("want [], got %q", buf.String())
	}
}

// Review P2c, CLI end: a host whose records cannot be read must make
// `inbox export` FAIL, so the ssh call carries a non-zero exit and the draining
// conductor reports a failed remote instead of an empty one.
func TestIssue1948_InboxExportCLI_UnreadableRecordsFailLoudly(t *testing.T) {
	drainTestHome(t)

	if err := session.WriteLedgerEntry(session.CompletionLedgerEntry{
		ChildID: "worker-ok", Profile: "default", Status: "ok", FinishedAt: time.Now(),
	}); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	dir, err := session.CompletionLedgerDir()
	if err != nil {
		t.Fatalf("ledger dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker-corrupt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	var buf bytes.Buffer
	if err := runInbox(&buf, []string{"export", "--json"}); err == nil {
		t.Fatalf("export must fail when a record file cannot be read, printed: %s", buf.String())
	}
	if strings.TrimSpace(buf.String()) == "[]" {
		t.Fatalf("an unreadable host must never print an empty record array")
	}
}

// Two conductors draining the same host both receive the record: the pull reads
// the remote, it never claims it.
func TestIssue1948_RemoteDrain_TwoConductorsBothReceive(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")

	record := remoteCompletion("worker-on-b", "ok", time.Now())
	fetch, _ := stubFetch([]session.TransitionNotificationEvent{record}, nil)

	var stdout, stderr bytes.Buffer
	for _, conductor := range []string{"conductor-one", "conductor-two"} {
		registerDrainTarget(t, conductor)
		stdout.Reset()
		if code := runRemoteDrain(&stdout, &stderr, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
			t.Fatalf("%s drain exit=%d: %s", conductor, code, stderr.String())
		}
		pending, err := session.ReadInboxEvents(conductor)
		if err != nil {
			t.Fatalf("read %s inbox: %v", conductor, err)
		}
		if len(pending) != 1 {
			t.Fatalf("%s should have received the record, has %d", conductor, len(pending))
		}
	}
}
