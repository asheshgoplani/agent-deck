package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestOpenCodeRestartLivePanePreservesLaunchOptions exercises the real
// RestartWithEnv -> tmux respawn path while TestMain's isolated TMUX_TMPDIR
// keeps the host's sessions untouched. It guards the live-pane branch, which
// used to rebuild only "opencode -s <id>" and silently drop the persisted
// model and the other flags owned by buildOpenCodeCommand.
func TestOpenCodeRestartLivePanePreservesLaunchOptions(t *testing.T) {
	skipIfNoTmuxBinary(t)

	projectDir := t.TempDir()
	stubDir := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "opencode-argv.log")
	stubPath := filepath.Join(stubDir, "opencode")
	stub := `#!/bin/sh
if [ "$1" = "session" ] && [ "$2" = "list" ]; then
  printf '[]\n'
  exit 0
fi
printf 'ARGS=%s\nMARKER=%s\n' "$*" "$OPENCODE_RESTART_MARKER" > "$OPENCODE_RESTART_LOG"
sleep 30
`
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("write opencode stub: %v", err)
	}
	stubPATH := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", stubPATH)

	tmuxName := fmt.Sprintf("agentdeck-opencode-model-restart-%d", time.Now().UnixNano())
	start := exec.Command("tmux", "new-session", "-d", "-s", tmuxName, "-c", projectDir, "sleep 30")
	if output, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux session: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", tmuxName).Run()
	})

	inst := NewInstanceWithTool("opencode-model-restart", projectDir, "opencode")
	inst.ID = "opencode-model-restart-instance"
	inst.OpenCodeSessionID = "ses_MODEL_RESTART"
	if _, _, err := SetField(inst, FieldModel, "freemodel/gpt-5.3-codex", nil); err != nil {
		t.Fatalf("persist model intent: %v", err)
	}
	opts := inst.GetOpenCodeOptions()
	if opts == nil {
		t.Fatal("persisted OpenCode options are nil")
	}
	opts.Agent = "build"
	if err := inst.SetOpenCodeOptions(opts); err != nil {
		t.Fatalf("persist OpenCode agent: %v", err)
	}

	// Round-trip the instance before binding the live pane, matching a fresh
	// CLI process loading the model intent from the registry.
	encoded, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("marshal instance: %v", err)
	}
	revived := &Instance{}
	if err := json.Unmarshal(encoded, revived); err != nil {
		t.Fatalf("unmarshal instance: %v", err)
	}
	revived.tmuxSession = tmux.ReconnectSessionLazy(tmuxName, tmuxName, projectDir, "opencode", "waiting")

	if err := revived.RestartWithEnv(map[string]string{
		"OPENCODE_RESTART_LOG":    argvLog,
		"OPENCODE_RESTART_MARKER": "once",
		"PATH":                    stubPATH,
	}); err != nil {
		t.Fatalf("RestartWithEnv: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var argv []byte
	for time.Now().Before(deadline) {
		argv, err = os.ReadFile(argvLog)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("opencode stub did not capture restart argv: %v", err)
	}
	got := string(argv)
	for _, want := range []string{
		"-s ses_MODEL_RESTART",
		"-m freemodel/gpt-5.3-codex",
		"--agent build",
		"--port ",
		"MARKER=once",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("restart argv missing %q:\n%s", want, got)
		}
	}
	if port := revived.GetOpenCodePort(); port <= 0 {
		t.Errorf("canonical restart did not record an SSE port: %d", port)
	}

	paneCmd, err := exec.Command("tmux", "display-message", "-p", "-t", tmuxName+":", "#{pane_start_command}").Output()
	if err != nil {
		t.Fatalf("read respawned pane command: %v", err)
	}
	if count := strings.Count(string(paneCmd), "OPENCODE_RESTART_MARKER"); count != 1 {
		t.Errorf("one-shot restart environment prefix count = %d, want 1:\n%s", count, paneCmd)
	}
}

// TestOpenCodePortConcurrentBuildAndRead covers the live restart's interaction
// with the status loop: restart rebuilds the OpenCode command (and its SSE
// port) while the UI can concurrently snapshot that port for watcher targets.
func TestOpenCodePortConcurrentBuildAndRead(t *testing.T) {
	inst := NewInstanceWithTool("opencode-port-race", t.TempDir(), "opencode")
	inst.OpenCodeSessionID = "ses_PORT_RACE"

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for n := 0; n < 128; n++ {
			_ = inst.buildOpenCodeCommand("opencode")
			runtime.Gosched()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for n := 0; n < 4096; n++ {
			_ = inst.GetOpenCodePort()
			runtime.Gosched()
		}
	}()
	close(start)
	wg.Wait()

	if port := inst.GetOpenCodePort(); port <= 0 {
		t.Fatalf("canonical builder did not retain its final SSE port: %d", port)
	}
}
