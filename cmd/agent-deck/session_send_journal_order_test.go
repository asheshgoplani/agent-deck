package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Round-3/round-4 regression test for the finding that handleSessionSend used
// to journal the send synchronously BEFORE printing its verdict
// (session_cmd.go, line ~3153 pre-round-3-fix; line ~3289 pre-round-4-fix for
// the `--wait --json` and `--stream` paths specifically), so a wedged health
// volume would delay the CLI's answer. It now records the event after each
// flag combination's own final verdict is printed and the exit code decided,
// mirroring handleSessionStop / handleSessionRestart. This proves it end to
// end for all three shapes handleSessionSend can take: with the journal write
// hooked to block indefinitely, the verdict must still print promptly, and
// the event must land only once the write is unblocked.

const sendJournalOrderHelperEnv = "AGENT_DECK_SEND_JOURNAL_HELPER_PROCESS"
const sendJournalOrderExtraArgsEnv = "AGENT_DECK_SEND_JOURNAL_EXTRA_ARGS"

// TestSendJournalOrderHelperProcess is re-exec'd as a subprocess by
// TestSessionSendVerdictNotDelayedByBlockedJournal. handleSessionSend can
// os.Exit on several paths, which would kill the real test binary if called
// in-process — the same reason issue2099's CLI tests re-exec themselves
// (see runIssue2099CLI). It also does its own tmux/storage setup rather than
// inheriting the parent's: TestMain's testutil.IsolateTmuxSocket() re-runs
// unconditionally in every re-exec'd process and points it at a fresh,
// isolated tmux socket the parent's own tmux session is not on.
func TestSendJournalOrderHelperProcess(t *testing.T) {
	if os.Getenv(sendJournalOrderHelperEnv) != "1" {
		return
	}
	// Not AGENTDECK_PROFILE: TestMain unconditionally overwrites that to
	// "_test" for every test in this package (see runTestMain), even in a
	// re-exec'd helper process.
	profile := os.Getenv("AGENT_DECK_SEND_JOURNAL_PROFILE")
	title := os.Getenv("AGENT_DECK_SEND_JOURNAL_TARGET_TITLE")
	message := os.Getenv("AGENT_DECK_SEND_JOURNAL_MESSAGE")
	release := os.Getenv("AGENT_DECK_SEND_JOURNAL_RELEASE_FILE")
	extraArgs := strings.Fields(os.Getenv(sendJournalOrderExtraArgsEnv))
	if len(extraArgs) == 0 {
		extraArgs = []string{"--no-wait", "--json"}
	}
	tool := os.Getenv("AGENT_DECK_SEND_JOURNAL_TOOL")
	if tool == "" {
		tool = "shell"
	}

	inst := session.NewInstanceWithTool(title, t.TempDir(), tool)
	inst.Status = session.StatusRunning
	inst.GroupPath = session.DefaultGroupPath

	sess := inst.GetTmuxSession()
	if err := sess.Start("bash"); err != nil {
		t.Fatalf("failed to start fixture tmux session: %v", err)
	}

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	if err := saveSessionData(storage, []*session.Instance{inst}, nil); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close seed storage: %v", err)
	}

	sendJournalWriter = func(profile, sessionID string, detail map[string]any) {
		for {
			if _, err := os.Stat(release); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		session.RecordSessionEvent(profile, sessionID, health.KindSend, detail)
	}

	if tool == "claude" {
		// The claude-tool delivery path (executeSend's claudeLike branch)
		// requires Claude-shaped submit evidence a plain bash pane cannot
		// produce (a sustained "active" tmux status, or a composer draft
		// marker clearing) — driving that through real pane timing is
		// exactly the flakiness the project's own fake-claude integration
		// tests (issue1409_1413_send_guard_integration_test.go) are gated
		// behind AGENT_DECK_INTEGRATION_TESTS to avoid. Sidestep it with the
		// SAME hook-file-driven signal #1978/#2033 added for real Claude
		// hooks: once the message is actually visible in the pane (proof the
		// send happened — hookBusyBeforeSend is read just before this, so it
		// is guaranteed to have seen no busy file yet), seed the target's
		// on-disk hook status to "running". sendWithRetryTarget treats a new
		// copy of the body on screen plus an idle->busy hook edge as
		// deterministic submission evidence, independent of tmux activity
		// timing (which a fake pane cannot reproduce faithfully — the
		// preflight composer wait and composer-draft guard alone add several
		// seconds before the message is even typed, ruling out a
		// fixed-delay write). This gives the test a reliable path to a
		// confirmed delivery so it can reach the turn-identity code the
		// round-4 fix touches.
		go func() {
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				content, err := sess.CapturePaneFresh()
				if err == nil && strings.Contains(content, message) {
					hooksDir := session.GetHooksDir()
					if mkErr := os.MkdirAll(hooksDir, 0o755); mkErr != nil {
						return
					}
					payload := map[string]any{
						"status":     "running",
						"session_id": "fake-" + inst.ID,
						"event":      "UserPromptSubmit",
						"ts":         time.Now().Unix(),
					}
					data, marshalErr := json.Marshal(payload)
					if marshalErr != nil {
						return
					}
					_ = os.WriteFile(filepath.Join(hooksDir, inst.ID+".json"), data, 0o644)
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
	}

	handleSessionSend(profile, append([]string{title, message}, extraArgs...))
	_ = sess.Kill()
	os.Exit(0)
}

// sendJournalOrderCase is one flag combination exercised end to end: the
// three shapes handleSessionSend's verdict can take (round-4 re-review
// findings: `--wait --json` and `--stream` both used to journal before
// printing their real verdict; only `--no-wait --json` was covered before).
type sendJournalOrderCase struct {
	name      string
	extraArgs []string
	// tool defaults to "shell" (a plain bash pane); --stream refuses any
	// tool that is not Claude-compatible, so the stream and wait-json cases
	// use "claude" instead — there is no real Claude process behind the
	// fixture pane, so those two deterministically fail once turn-identity
	// setup cannot find a transcript within --timeout, which is exactly the
	// call site (session_cmd.go's awaitTranscriptPath failure) this round's
	// fix moved the journal write behind.
	tool string
	// validate inspects the parsed verdict object once it fully prints,
	// confirming this case's flag combination actually reached (and
	// journaled after) its own real verdict rather than an earlier one.
	validate func(t *testing.T, payload map[string]any)
}

func TestSessionSendVerdictNotDelayedByBlockedJournal(t *testing.T) {
	skipIfNoTmuxBinaryCLI(t)

	cases := []sendJournalOrderCase{
		{
			name:      "no-wait-json",
			extraArgs: []string{"--no-wait", "--json"},
			validate: func(t *testing.T, payload map[string]any) {
				if _, ok := payload["success"].(bool); !ok {
					t.Fatalf("verdict has no success field: %v", payload)
				}
			},
		},
		{
			// Round-4 finding: this combination skips the eager
			// `!*wait || !*jsonOutput` ack entirely and only prints its
			// verdict after turn-identity/completion — recordSendEvent must
			// wait for that, not fire at the pre-round-4 call site that
			// only covered the eager-ack combinations. `--stream` and the
			// post-send `--wait` completion path both require a
			// Claude-compatible tool for turn identity, so this uses
			// "claude"; `--no-wait` is combined in purely to skip the
			// unrelated PRE-send agent-readiness gate (send.WaitForAgentReady
			// with PromptGates.ClaudeComposer), which the fixture's plain
			// bash pane can never satisfy and which sits well before any
			// code this round's fix touches. `--wait` and `--no-wait`
			// govern different phases (post-send completion vs. pre-send
			// readiness) and are not mutually exclusive. With no real
			// Claude process behind the pane, turn identity can never be
			// established, so the deterministic, fast outcome is
			// awaitTranscriptPath's own timeout error — exactly the call
			// site this round's fix moved the journal write behind.
			name:      "wait-json",
			extraArgs: []string{"--wait", "--no-wait", "--json", "--timeout", "3s"},
			tool:      "claude",
			validate: func(t *testing.T, payload map[string]any) {
				if v, ok := payload["success"].(bool); !ok || v {
					t.Fatalf("expected a failed --wait --json verdict (no real Claude transcript behind the fixture pane), got: %v", payload)
				}
				errMsg, _ := payload["error"].(string)
				if !strings.Contains(errMsg, "transcript not found") {
					t.Fatalf("expected the turn-identity transcript-not-found failure (the round-4 call site), got a different failure: %v", payload)
				}
			},
		},
		{
			// Round-4 finding: --stream skips the whole `!*stream` block
			// unconditionally, so its real verdict (the streamed JSONL
			// reply, or here the transcript-not-found error event since
			// the fixture has no real Claude process) is emitted even
			// later. See the wait-json case above for why --no-wait is
			// combined in here too. A short --timeout keeps this
			// deterministic and fast.
			name:      "stream",
			extraArgs: []string{"--stream", "--no-wait", "--json", "--timeout", "3s"},
			tool:      "claude",
			validate: func(t *testing.T, payload map[string]any) {
				if payload["type"] != "error" {
					t.Fatalf("expected a --stream error verdict (no real Claude transcript behind the fixture pane), got: %v", payload)
				}
				msg, _ := payload["message"].(string)
				if !strings.Contains(msg, "transcript not found") {
					t.Fatalf("expected the turn-identity transcript-not-found failure (the round-4 call site), got a different failure: %v", payload)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			runSendJournalOrderCase(t, tc)
		})
	}
}

func runSendJournalOrderCase(t *testing.T, tc sendJournalOrderCase) {
	profile := "_test_send_journal_order_" + tc.name
	const title = "send-journal-order-target"
	const message = "hello from the blocked-journal regression test"

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	releaseFile := filepath.Join(t.TempDir(), "release")

	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "XDG_") || strings.HasPrefix(e, "AGENTDECK_PROFILE=") {
			continue
		}
		env = append(env, e)
	}
	helperCmd := exec.Command(os.Args[0], "-test.run=^TestSendJournalOrderHelperProcess$")
	helperCmd.Env = append(env,
		sendJournalOrderHelperEnv+"=1",
		// Tells TestMain to keep this process's inherited HOME/XDG instead of
		// re-isolating it (see runTestMain).
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"AGENT_DECK_SEND_JOURNAL_PROFILE="+profile,
		"AGENT_DECK_SEND_JOURNAL_RELEASE_FILE="+releaseFile,
		"AGENT_DECK_SEND_JOURNAL_TARGET_TITLE="+title,
		"AGENT_DECK_SEND_JOURNAL_MESSAGE="+message,
		"AGENT_DECK_SEND_JOURNAL_TOOL="+tc.tool,
		sendJournalOrderExtraArgsEnv+"="+strings.Join(tc.extraArgs, " "),
	)
	stdout, err := helperCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr strings.Builder
	helperCmd.Stderr = &stderr

	if err := helperCmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		// Always unblock, even if an assertion above failed, so the helper
		// process (and this test run) never hangs.
		_ = os.WriteFile(releaseFile, []byte("go"), 0o644)
		_ = helperCmd.Wait()
	})

	// The verdict is pretty-printed JSON (json.MarshalIndent) spanning
	// several lines, or (for --stream) one compact JSON object per line;
	// accumulate lines until they parse as one JSON object.
	lineCh := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	start := time.Now()
	var buf strings.Builder
	var payload map[string]any
	deadline := time.After(20 * time.Second)
readLoop:
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatalf("helper closed stdout before printing a full JSON verdict; stderr:\n%s", stderr.String())
			}
			buf.WriteString(line)
			buf.WriteByte('\n')
			if json.Valid([]byte(buf.String())) {
				if err := json.Unmarshal([]byte(buf.String()), &payload); err != nil {
					t.Fatalf("verdict is not a JSON object: %v: %q", err, buf.String())
				}
				break readLoop
			}
		case <-deadline:
			t.Fatalf("verdict never finished printing within 20s; partial: %q; stderr:\n%s", buf.String(), stderr.String())
		}
	}
	elapsed := time.Since(start)
	if elapsed > 15*time.Second {
		t.Fatalf("verdict took %v to print — a blocked journal write delayed it; stderr:\n%s", elapsed, stderr.String())
	}
	tc.validate(t, payload)

	// The verdict already printed above; the journal write is still parked
	// behind the (as yet unwritten) release file, so nothing should have
	// landed yet.
	events := readSendJournalEvents(t, profile)
	if len(events) != 0 {
		t.Fatalf("journal event landed before being unblocked (proves the write was not actually deferred): %+v", events)
	}

	if err := os.WriteFile(releaseFile, []byte("go"), 0o644); err != nil {
		t.Fatalf("write release file: %v", err)
	}
	if err := helperCmd.Wait(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("helper process failed to run: %v; stderr:\n%s", err, stderr.String())
		}
		// A non-zero exit is expected on the failure verdicts
		// (handleSessionSend's own os.Exit(1) paths, e.g. the --stream
		// case's transcript-not-found error); only crashes/timeouts
		// (caught above) are a test infrastructure problem.
	}

	events = readSendJournalEvents(t, profile)
	if len(events) != 1 || events[0].Kind != health.KindSend {
		t.Fatalf("expected exactly one send event once unblocked, got: %+v", events)
	}
}

// readSendJournalEvents reads every event the profile's session-event
// journal holds under the current (test-isolated) HOME.
func readSendJournalEvents(t *testing.T, profile string) []health.Event {
	t.Helper()
	dir, err := session.HealthLogDir(profile)
	if err != nil {
		t.Fatalf("HealthLogDir: %v", err)
	}
	events, incomplete, err := health.ReadEvents(dir, time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if incomplete {
		t.Fatalf("journal read reported incomplete")
	}
	return events
}
