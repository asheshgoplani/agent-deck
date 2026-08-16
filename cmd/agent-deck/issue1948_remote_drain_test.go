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
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func drainTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	session.ResetInboxFingerprintCacheForTest()
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

// The headline: a completion that only ever existed on the remote host is in
// the conductor's LOCAL inbox after one drain.
func TestIssue1948_RemoteDrain_WritesRemoteCompletionIntoLocalInbox(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-local"

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
	if len(pending) != 1 || pending[0].ChildSessionID != "worker-on-b" {
		t.Fatalf("remote completion did not reach the local inbox: %+v", pending)
	}
	if pending[0].SourceRemote != "boxb" {
		t.Fatalf("pulled record must name the remote it came from: %+v", pending[0])
	}
	if pending[0].TargetSessionID != conductor {
		t.Fatalf("pulled record must be addressed to the draining conductor: %+v", pending[0])
	}
	if !strings.Contains(stdout.String(), "1 record(s) fetched, 1 new, 0 already present") {
		t.Fatalf("drain report unclear:\n%s", stdout.String())
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
	if !strings.Contains(out2.String(), "1 record(s) fetched, 0 new, 1 already present") {
		t.Fatalf("second drain must report the duplicate explicitly:\n%s", out2.String())
	}
}

// Idempotence must survive a FRESH PROCESS (the real shape: each drain is its
// own `agent-deck` invocation, so the in-process fingerprint cache is empty and
// dedup has to come off disk).
func TestIssue1948_RemoteDrain_IdempotentAcrossProcesses(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-fresh"

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

	fetch, _ := stubFetch([]session.TransitionNotificationEvent{
		remoteCompletion("worker-on-b", "ok", time.Now()),
	}, nil)
	var stdout, stderr bytes.Buffer
	if code := runRemoteDrain(&stdout, &stderr, []string{"--json", "--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("exit=%d: %s", code, stderr.String())
	}

	var got remoteDrainResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	if got.Remote != "boxb" || got.Host != "worker@box-b" || got.TargetSessionID != conductor {
		t.Fatalf("json identity fields wrong: %+v", got)
	}
	if got.Fetched != 1 || got.Written != 1 || got.Duplicates != 0 || len(got.Records) != 1 {
		t.Fatalf("json counts wrong: %+v", got)
	}
}

// The remote side of the wire: `inbox export --json` is what the drain runs
// over ssh. It must emit a JSON array (the shape FetchPendingRecords parses)
// and must not consume the inbox it read.
func TestIssue1948_InboxExportCLI_EmitsArrayAndConsumesNothing(t *testing.T) {
	drainTestHome(t)
	parent := "parent-on-the-remote"

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

// Two conductors draining the same host both receive the record: the pull reads
// the remote, it never claims it.
func TestIssue1948_RemoteDrain_TwoConductorsBothReceive(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")

	record := remoteCompletion("worker-on-b", "ok", time.Now())
	fetch, _ := stubFetch([]session.TransitionNotificationEvent{record}, nil)

	var stdout, stderr bytes.Buffer
	for _, conductor := range []string{"conductor-one", "conductor-two"} {
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
