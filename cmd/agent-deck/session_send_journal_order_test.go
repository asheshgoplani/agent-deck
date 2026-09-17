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

// Round-3 regression test for the finding that handleSessionSend used to
// journal the send synchronously BEFORE printing its verdict
// (session_cmd.go, line ~3153 pre-fix), so a wedged health volume would
// delay the CLI's answer. It now records the event after the verdict is
// printed and the exit code decided, mirroring handleSessionStop /
// handleSessionRestart. This proves it end to end: with the journal write
// hooked to block indefinitely, the verdict must still print promptly, and
// the event must land only once the write is unblocked.

const sendJournalOrderHelperEnv = "AGENT_DECK_SEND_JOURNAL_HELPER_PROCESS"

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

	inst := session.NewInstanceWithTool(title, t.TempDir(), "shell")
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

	handleSessionSend(profile, []string{title, message, "--no-wait", "--json"})
	_ = sess.Kill()
	os.Exit(0)
}

func TestSessionSendVerdictNotDelayedByBlockedJournal(t *testing.T) {
	skipIfNoTmuxBinaryCLI(t)

	const profile = "_test_send_journal_order"
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
	// several lines; accumulate lines until they parse as one JSON object.
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
	if elapsed > 5*time.Second {
		t.Fatalf("verdict took %v to print — a blocked journal write delayed it; stderr:\n%s", elapsed, stderr.String())
	}
	// The exact delivery classification (confirmed/unconfirmed/failed) a bare
	// bash pane earns is not the point here — recordSendEvent runs on every
	// exit path handleSessionSend can take (see session_cmd.go), so any
	// well-formed verdict proves the property under test: the CLI's answer
	// (whatever it is) is not the thing waiting on the journal write.
	if _, ok := payload["success"].(bool); !ok {
		t.Fatalf("verdict has no success field: %v; stderr:\n%s", payload, stderr.String())
	}

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
		// (handleSessionSend's own os.Exit(1) paths); only crashes/timeouts
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
