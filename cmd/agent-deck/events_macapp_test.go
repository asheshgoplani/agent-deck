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
)

func writeMacappConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEventsPublishGateAndNamespace(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "events", "publish", "--kind", "macapp.canvas.show", "--data", `{"path":"/x.svg"}`)
	if code != 2 || !strings.Contains(stdout+stderr, "plugins = true") {
		t.Fatalf("publish without [macapp] plugins: exit %d %s", code, stderr)
	}
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n")
	if _, _, code := runAgentDeck(t, home, "events", "publish", "--kind", "session.status"); code != 2 {
		t.Fatalf("publish outside macapp.* accepted: exit %d", code)
	}
	if _, _, code := runAgentDeck(t, home, "events", "publish", "--kind", "macapp.x", "--data", "{not json"); code != 2 {
		t.Fatalf("invalid JSON accepted: exit %d", code)
	}
}

// TestEventsPublishFollowRoundTrip: a macapp.* frame published from the CLI
// reaches a running `events follow --kind macapp.` within the budget, with
// its session id and data, and `events stats` counts it per kind.
func TestEventsPublishFollowRoundTrip(t *testing.T) {
	home := t.TempDir()
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n")
	cmd := exec.Command(channelsCLIBinary(t), "events", "follow", "--json", "--kind", "macapp.")
	cmd.Env = agentDeckTestEnv(home, nil)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	stdout, stderr, code := runAgentDeck(t, home, "events", "publish", "--kind", "macapp.canvas.show", "--session", "sess-9", "--data", `{"path":"/tmp/d.svg","caption":"c"}`, "--json")
	if code != 0 {
		t.Fatalf("publish: %d %s %s", code, stdout, stderr)
	}
	var res struct {
		OK     bool   `json:"ok"`
		Cursor uint64 `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || !res.OK || res.Cursor == 0 {
		t.Fatalf("publish result: %v %s", err, stdout)
	}
	select {
	case line := <-lines:
		if !strings.Contains(line, `"kind":"macapp.canvas.show"`) || !strings.Contains(line, `"session_id":"sess-9"`) || !strings.Contains(line, `"caption":"c"`) {
			t.Fatalf("frame: %s", line)
		}
		t.Logf("publish -> follow: %v (includes CLI start-up)", time.Since(start))
	case <-time.After(5 * time.Second):
		t.Fatal("follow never saw the frame")
	}
	stdout, _, code = runAgentDeck(t, home, "events", "stats", "--json")
	if code != 0 || !strings.Contains(stdout, `"macapp.canvas.show": 1`) {
		t.Fatalf("stats kinds: %d %s", code, stdout)
	}
}
