package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Opt-in integration: the runner builds the actual CLI and supplies its path.
// Every process lives on this test's private socket and exits naturally.
func TestIssue2091RealProducersAndVanishedServer(t *testing.T) {
	binary := os.Getenv("ISSUE2091_BIN")
	if binary == "" {
		t.Skip("set ISSUE2091_BIN to the built agent-deck CLI")
	}
	for _, tool := range []string{"claude", "codex"} {
		for _, scenario := range []string{"done", "ordinary", "prior-launch"} {
			done := scenario != "ordinary"
			t.Run(fmt.Sprintf("%s/%s", tool, scenario), func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("XDG_CONFIG_HOME", "")
				t.Setenv("XDG_DATA_HOME", "")
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				socket := fmt.Sprintf("i2091-%d", time.Now().UnixNano())
				tm := func(args ...string) []byte {
					t.Helper()
					out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
					if err != nil {
						t.Fatalf("private tmux %v: %v: %s", args, err, out)
					}
					return out
				}
				tm("new-session", "-d", "-s", "keeper", "sleep 60")
				t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", "keeper").Run() })
				id := "issue2091-real"
				producer := &Instance{ID: id, Tool: tool}
				if err := producer.seedCompletionLaunch(); err != nil {
					t.Fatal(err)
				}
				text := "ordinary turn"
				if done {
					text = "===AGENTDECK_DONE=== status=ok summary=finished"
				}
				payload := map[string]any{"type": "agent-turn-complete", "thread-id": "thread", "turn-id": "turn", "last-assistant-message": text}
				subcommand := "codex-notify"
				if tool == "claude" {
					dir := filepath.Join(home, ".claude", "projects", "test")
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					transcript := filepath.Join(dir, "thread.jsonl")
					record, _ := json.Marshal(map[string]any{"timestamp": time.Now().Add(-time.Hour).Format(time.RFC3339Nano), "type": "assistant", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": text}}}})
					if err := os.WriteFile(transcript, append(record, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
					payload = map[string]any{"hook_event_name": "Stop", "session_id": "thread", "transcript_path": transcript, "stop_hook_active": true, "cwd": home}
					subcommand = "hook-handler"
				}
				data, _ := json.Marshal(payload)
				// Sleep past hook timestamp's second precision after the actual launch.
				command := "export HOME=" + shellescape.Quote(home) + " AGENTDECK_INSTANCE_ID=" + id + "; sleep 2; printf '%s' " + shellescape.Quote(string(data)) + " | " + shellescape.Quote(binary) + " " + subcommand
				if tool == "codex" {
					command = "export HOME=" + shellescape.Quote(home) + " AGENTDECK_INSTANCE_ID=" + id + "; sleep 2; " + shellescape.Quote(binary) + " " + subcommand + " " + shellescape.Quote(string(data))
				}
				started := time.Now()
				tm("new-session", "-d", "-s", "worker", producer.bindCompletionLaunchCommand(command))
				if tool == "claude" && scenario != "prior-launch" {
					record, _ := json.Marshal(map[string]any{"timestamp": time.Now().Format(time.RFC3339Nano), "type": "assistant", "message": map[string]any{"role": "assistant", "content": text}})
					if err := os.WriteFile(payload["transcript_path"].(string), append(record, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				}
				deadline := time.Now().Add(10 * time.Second)
				for exec.Command("tmux", "-L", socket, "has-session", "-t", "worker").Run() == nil {
					if time.Now().After(deadline) {
						t.Fatal("worker did not exit")
					}
					time.Sleep(50 * time.Millisecond)
				}
				hook := readHookStatusFile(id)
				if hook == nil || hook.SessionID != "thread" || hook.HookGeneration != producer.hookLaunchGeneration || (hook.DoneStatus == "ok") != done {
					t.Fatalf("actual producer evidence: %+v", hook)
				}
				classify := func() Status {
					observer := &Instance{ID: id, Tool: tool, ClaudeSessionID: "thread", CodexSessionID: "thread", Status: StatusWaiting, LastStartedAt: started, tmuxSession: &tmux.Session{Name: "worker", SocketName: socket}}
					observer.mu.Lock()
					observer.applyTerminatedPaneStatus()
					got := observer.Status
					observer.mu.Unlock()
					return got
				}
				want := StatusError
				if done && !(tool == "claude" && scenario == "prior-launch") {
					want = StatusStopped
				}
				if got := classify(); got != want {
					t.Fatalf("vanished worker: %s, want %s", got, want)
				}
				tm("kill-session", "-t", "keeper")
				if got := classify(); got != StatusError {
					t.Fatalf("vanished server: %s", got)
				}
			})
		}
	}
}
