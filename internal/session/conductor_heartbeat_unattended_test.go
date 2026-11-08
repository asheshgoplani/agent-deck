package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// These tests run the rendered heartbeat.sh against a fake agent-deck and pin
// how it behaves when nobody is watching: a send that was not delivered is
// never saved as delivered, a dead conductor says so once, and two runs never
// overlap.

// heartbeatScriptHarness writes fake as agent-deck on a private PATH and the
// rendered heartbeat.sh for conductor "ops". It returns the temp HOME, the
// conductor dir and a func that runs one tick and returns its exit error.
func heartbeatScriptHarness(t *testing.T, fake string) (string, string, func() error) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	conductorDir, err := ConductorNameDir("ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(conductorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "agent-deck"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(home, "heartbeat.sh")
	if err := os.WriteFile(script, []byte(renderConductorHeartbeatScript("ops", "default")), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func() error {
		cmd := exec.Command("bash", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
		return nil
	}
	return home, conductorDir, run
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// A heartbeat `session send` skipped (idle gate) or muted (trigger mute) exits
// 0 without typing anything. Committing it would mark that waiting/error set
// as delivered and it would never be sent again until it changed.
func TestHeartbeatScript_SkippedOrMutedSendIsNotCommitted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
	}{
		{"skipped", `{"success": true, "skipped": true, "session_id": "x"}`},
		{"muted", `{"success": true, "muted": true, "trigger_id": "heartbeat:ops"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := `#!/bin/bash
while [ "$1" = "-p" ]; do shift 2; done
case "$1 $2" in
  "conductor status") echo '{"conductors": [{"heartbeat": true}]}' ;;
  "session show") echo '{"status": "idle"}' ;;
  "conductor heartbeat-tick")
    if [[ "$5" = --commit-message=* ]]; then touch "$HOME/committed";
    elif [ ! -f "$HOME/committed" ]; then echo '[HEARTBEAT] [ops] Status: 1 waiting.'; fi ;;
  "session send")
    echo "$4" >> "$HOME/sent"
    echo '` + tc.reply + `' ;;
esac
`
			home, _, run := heartbeatScriptHarness(t, fake)
			for i := 0; i < 2; i++ {
				if err := run(); err != nil {
					t.Fatalf("tick %d: %v", i+1, err)
				}
			}
			if _, err := os.Stat(filepath.Join(home, "committed")); err == nil {
				t.Fatalf("a %s send was committed as delivered", tc.name)
			}
			if n := countLines(t, filepath.Join(home, "sent")); n != 2 {
				t.Fatalf("a %s heartbeat must be offered again next tick: sends=%d, want 2", tc.name, n)
			}
		})
	}
}

// The delivered path still commits, and a send failure reported as a JSON
// error still logs its reason.
func TestHeartbeatScript_JSONSendOutcomes(t *testing.T) {
	fake := `#!/bin/bash
while [ "$1" = "-p" ]; do shift 2; done
case "$1 $2" in
  "conductor status") echo '{"conductors": [{"heartbeat": true}]}' ;;
  "session show") echo '{"status": "waiting"}' ;;
  "conductor heartbeat-tick")
    if [[ "$5" = --commit-message=* ]]; then touch "$HOME/committed";
    elif [ ! -f "$HOME/committed" ]; then echo '[HEARTBEAT] [ops] Status: 1 waiting.'; fi ;;
  "session send")
    if [ ! -f "$HOME/failed" ]; then
      touch "$HOME/failed"
      printf '{\n  "code": "DELIVERY_FAILED",\n  "error": "message not delivered to conductor-ops: pane gone",\n  "success": false\n}\n'
      exit 1
    fi
    printf '{\n  "success": true,\n  "session_id": "x"\n}\n' ;;
esac
`
	home, conductorDir, run := heartbeatScriptHarness(t, fake)
	_ = run()
	logData, _ := os.ReadFile(filepath.Join(conductorDir, "heartbeat.log"))
	if !strings.Contains(string(logData), "heartbeat: send failed: message not delivered to conductor-ops: pane gone") {
		t.Fatalf("a JSON send error must log its reason: log=%q", logData)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "committed")); err != nil {
		t.Fatalf("a delivered send must be committed: %v", err)
	}
}

// A conductor in error, stopped or missing gets no heartbeat. The script must
// say so once per outage (log line plus one urgent notify) instead of exiting
// silently, and stay quiet while the conductor is merely busy.
func TestHeartbeatScript_DeadConductorLogsAndNotifiesOnce(t *testing.T) {
	fake := `#!/bin/bash
while [ "$1" = "-p" ]; do shift 2; done
case "$1 $2" in
  "conductor status") echo '{"conductors": [{"heartbeat": true}]}' ;;
  "session show")
    st=$(cat "$HOME/status")
    if [ "$st" = "missing" ]; then echo 'Error: session not found' >&2; exit 2; fi
    echo "{\"status\": \"$st\"}" ;;
  "conductor heartbeat-tick") ;;
  "conductor notify") echo "$*" >> "$HOME/notified" ;;
  "session send") echo "$4" >> "$HOME/sent" ;;
esac
`
	for _, status := range []string{"error", "stopped", "missing"} {
		t.Run(status, func(t *testing.T) {
			home, conductorDir, run := heartbeatScriptHarness(t, fake)
			logPath := filepath.Join(conductorDir, "heartbeat.log")
			notified := filepath.Join(home, "notified")
			setStatus := func(s string) {
				if err := os.WriteFile(filepath.Join(home, "status"), []byte(s), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			setStatus(status)
			if status == "missing" {
				// One failed read is not an outage.
				if err := run(); err != nil {
					t.Fatal(err)
				}
				if n := countLines(t, logPath) + countLines(t, notified); n != 0 {
					t.Fatalf("a single missing status must not alert: %d lines", n)
				}
			}
			for i := 0; i < 3; i++ {
				if err := run(); err != nil {
					t.Fatalf("tick %d: %v", i+1, err)
				}
			}
			logData, _ := os.ReadFile(logPath)
			want := "heartbeat: conductor ops is " + status + "; heartbeat not sent\n"
			if string(logData) != want {
				t.Fatalf("dead conductor must log one line per outage:\n got %q\nwant %q", logData, want)
			}
			notes, _ := os.ReadFile(notified)
			if strings.Count(string(notes), "\n") != 1 ||
				!strings.Contains(string(notes), "notify --tier urgent --conductor ops ") ||
				!strings.Contains(string(notes), " since ") {
				t.Fatalf("dead conductor must notify once, urgent: %q", notes)
			}

			// Back to idle clears the outage; a new outage reports again.
			setStatus("idle")
			if err := run(); err != nil {
				t.Fatal(err)
			}
			setStatus(status)
			for i := 0; i < 2; i++ { // "missing" needs two runs in a row
				if err := run(); err != nil {
					t.Fatal(err)
				}
			}
			if n := countLines(t, logPath); n != 2 {
				t.Fatalf("a second outage must log again: lines=%d", n)
			}
			if n := countLines(t, notified); n != 2 {
				t.Fatalf("a second outage must notify again: notifies=%d", n)
			}
		})
	}

	t.Run("running stays silent", func(t *testing.T) {
		home, conductorDir, run := heartbeatScriptHarness(t, fake)
		if err := os.WriteFile(filepath.Join(home, "status"), []byte("running"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := run(); err != nil {
			t.Fatal(err)
		}
		if n := countLines(t, filepath.Join(conductorDir, "heartbeat.log")); n != 0 {
			t.Fatalf("a busy conductor is not dead; log lines=%d", n)
		}
		if n := countLines(t, filepath.Join(home, "notified")); n != 0 {
			t.Fatalf("a busy conductor is not dead; notifies=%d", n)
		}
	})
}

// A manual run or cron fallback must not race the timer: while another run
// holds heartbeat.lock the script exits 0 without sending.
func TestHeartbeatScript_SkipsWhileLockHeld(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not available; the script runs unlocked there")
	}
	fake := `#!/bin/bash
while [ "$1" = "-p" ]; do shift 2; done
case "$1 $2" in
  "conductor status") echo '{"conductors": [{"heartbeat": true}]}' ;;
  "session show") echo '{"status": "idle"}' ;;
  "conductor heartbeat-tick")
    if [[ "$5" != --commit-message=* ]]; then echo '[HEARTBEAT] [ops] Status: 1 waiting.'; fi ;;
  "session send") echo "$4" >> "$HOME/sent"; echo '{"success": true}' ;;
esac
`
	home, conductorDir, run := heartbeatScriptHarness(t, fake)
	lock, err := os.OpenFile(filepath.Join(conductorDir, "heartbeat.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatalf("a run that finds the lock held must exit 0: %v", err)
	}
	if n := countLines(t, filepath.Join(home, "sent")); n != 0 {
		t.Fatalf("a run must not send while another holds the lock: sends=%d", n)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if n := countLines(t, filepath.Join(home, "sent")); n != 1 {
		t.Fatalf("the run after the lock is released must send: sends=%d", n)
	}
}

// writeHeartbeatJSON is called by concurrent heartbeat-tick processes. A
// shared fixed temp name lets one writer rename the other's half-written file
// or fail with ENOENT.
func TestWriteHeartbeatJSON_ConcurrentWritersDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heartbeat-tick.json")
	// A stale entry under the old fixed temp name must not break a write.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8*50)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := writeHeartbeatJSON(path, map[string]int{"writer": w, "i": i}); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("final file is not valid JSON: %q: %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "heartbeat-tick.json" && e.Name() != "heartbeat-tick.json.tmp" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v, want 0600", info.Mode().Perm())
	}
}

// A flock that rejects its flags (or cannot open the lock) must not stop the
// heartbeat: the run logs one line and goes on unlocked.
func TestHeartbeatScript_UnusableFlockRunsUnlocked(t *testing.T) {
	fake := `#!/bin/bash
while [ "$1" = "-p" ]; do shift 2; done
case "$1 $2" in
  "conductor status") echo '{"conductors": [{"heartbeat": true}]}' ;;
  "session show") echo '{"status": "idle"}' ;;
  "conductor heartbeat-tick")
    if [[ "$5" != --commit-message=* ]]; then echo '[HEARTBEAT] [ops] Status: 1 waiting.'; fi ;;
  "session send") echo "$4" >> "$HOME/sent"; echo '{"success": true}' ;;
esac
`
	home, conductorDir, run := heartbeatScriptHarness(t, fake)
	// A flock that only knows -n and fails on -o with a usage error (64).
	badFlock := "#!/bin/bash\necho 'flock: unknown option -- o' >&2\nexit 64\n"
	if err := os.WriteFile(filepath.Join(home, "bin", "flock"), []byte(badFlock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatalf("an unusable flock must not fail the run: %v", err)
	}
	if n := countLines(t, filepath.Join(home, "sent")); n != 1 {
		t.Fatalf("the heartbeat must still be sent unlocked: sends=%d", n)
	}
	logData, _ := os.ReadFile(filepath.Join(conductorDir, "heartbeat.log"))
	if !strings.Contains(string(logData), "heartbeat: flock unusable (exit 64); running unlocked") {
		t.Fatalf("an unusable flock must be logged: %q", logData)
	}
}
