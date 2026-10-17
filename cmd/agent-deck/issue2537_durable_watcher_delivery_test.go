package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Issue #2537: a routed watcher event waits in the send queue for a stopped
// conductor, survives the exit of the process that routed it, and reaches
// the conductor once when it is back. Real binary, on the #2530 test env.

// killDetached kills what this env left running outside its tmux server: the
// detached send workers, which a failed test could leave waiting.
func (e *issue2530Env) killDetached() {
	home := []byte("HOME=" + filepath.Join(e.root, "home") + "\x00")
	entries, _ := os.ReadDir("/proc")
	for _, ent := range entries {
		pid, err := strconv.Atoi(ent.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", ent.Name(), "environ"))
		if err == nil && bytes.Contains(env, home) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

type issue2537Undelivered struct {
	Conductor string `json:"conductor"`
	Count     int    `json:"count"`
}

// undelivered reads `watcher status hook --json`'s undelivered list.
func (e *issue2530Env) undelivered() []issue2537Undelivered {
	e.t.Helper()
	var st struct {
		Undelivered []issue2537Undelivered `json:"undelivered"`
	}
	if err := json.Unmarshal([]byte(e.cli("watcher", "status", "hook", "--json")), &st); err != nil {
		e.t.Fatalf("watcher status --json: %v", err)
	}
	return st.Undelivered
}

func (e *issue2530Env) pane(tmuxSession string) string {
	capture := exec.Command("tmux", "capture-pane", "-p", "-J", "-S", "-", "-t", tmuxSession)
	capture.Env = e.env
	out, _ := capture.Output()
	return string(out)
}

// TestIssue2537_EventsForAStoppedConductorAreDeliveredOnceAfterQuit is the
// maintainer's test (a): events are routed while the conductor is stopped,
// the process that routed them quits, the conductor comes back, and it gets
// each event exactly once, in order.
func TestIssue2537_EventsForAStoppedConductorAreDeliveredOnceAfterQuit(t *testing.T) {
	e := newIssue2530Env(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	e.env = append(e.env,
		"AGENTDECK_SEND_WORKER_POLL=200ms",
		"AGENTDECK_SEND_RETRY_BACKOFF_MAX=500ms",
		"AGENTDECK_SEND_LAND_WINDOW=2s",
	)
	t.Cleanup(e.killDetached)
	e.createWebhookWatcher()

	var conductor struct {
		ID string `json:"id"`
	}
	out := e.cli("launch", filepath.Join(e.root, "proj"), "-t", "conductor-demo", "-c", "cat >/dev/null", "--no-parent", "--json")
	if err := json.Unmarshal([]byte(out), &conductor); err != nil || conductor.ID == "" {
		t.Fatalf("launch conductor-demo: %v\n%s", err, out)
	}
	e.cli("session", "stop", conductor.ID)

	web := e.startWeb("web")
	issue2530WaitFor(t, 10*time.Second, "the watcher port to listen", e.listening)
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	first, second := "first-2537-"+stamp, "second-2537-"+stamp
	e.post(first)
	e.post(second)
	e.waitRecorded(second)
	issue2530WaitFor(t, 10*time.Second, "both events queued for the stopped conductor", func() bool {
		u := e.undelivered()
		return len(u) == 1 && u[0].Conductor == "demo" && u[0].Count == 2
	})

	// The process that routed the events quits.
	if err := web.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	issue2530WaitFor(t, 15*time.Second, "web to exit on SIGTERM", func() bool {
		return syscall.Kill(web.Process.Pid, 0) != nil
	})
	time.Sleep(time.Second)
	if u := e.undelivered(); len(u) != 1 || u[0].Count != 2 {
		t.Fatalf("after quit the queue holds %+v, want both events for demo", u)
	}

	// The conductor comes back.
	e.cli("session", "start", conductor.ID)
	var shown struct {
		TmuxSession string `json:"tmux_session"`
	}
	if err := json.Unmarshal([]byte(e.cli("session", "show", conductor.ID, "--json")), &shown); err != nil || shown.TmuxSession == "" {
		t.Fatalf("session show: %v (%q)", err, shown.TmuxSession)
	}
	wantFirst := "[webhook] alice@example.com: " + first
	wantSecond := "[webhook] alice@example.com: " + second
	issue2530WaitFor(t, 30*time.Second, "both events in the conductor pane", func() bool {
		p := e.pane(shown.TmuxSession)
		return strings.Contains(p, wantFirst) && strings.Contains(p, wantSecond)
	})
	issue2530WaitFor(t, 30*time.Second, "the queue to empty", func() bool { return len(e.undelivered()) == 0 })
	time.Sleep(2 * time.Second)
	p := e.pane(shown.TmuxSession)
	if n := strings.Count(p, wantFirst); n != 1 {
		t.Fatalf("first event typed %d times, want once:\n%s", n, p)
	}
	if n := strings.Count(p, wantSecond); n != 1 {
		t.Fatalf("second event typed %d times, want once:\n%s", n, p)
	}
	if strings.Index(p, wantFirst) > strings.Index(p, wantSecond) {
		t.Fatalf("events typed out of order:\n%s", p)
	}
}
