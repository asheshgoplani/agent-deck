package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Drive the real CLI and real tmux transport against the existing input recorder.
// No paid harness runs: fake_harness.py records the exact input bytes without
// evaluating commands. TestMain supplies an isolated HOME and tmux socket.
func TestPiSendSource_RealCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the real CLI with tmux")
	}
	for _, binary := range []string{"tmux", "python3"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s not available", binary)
		}
	}
	for _, targetTool := range []string{"pi", "claude"} {
		t.Run(targetTool, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)

			fixture, err := filepath.Abs("../../tests/comms_matrix/fake_harness.py")
			if err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(home, "input.bin")
			sender := session.NewInstanceWithGroupAndTool("source", home, "test", "pi")
			target := session.NewInstanceWithGroupAndTool("receiver", home, "test", targetTool)
			pane := tmux.NewSession("pi-source-"+targetTool, home)
			command := "python3 " + shellescape.Quote(fixture) + " --pane --state ready --tool " + targetTool + " --capture " + shellescape.Quote(capture)
			if targetTool == "claude" {
				composer, err := filepath.Abs("testdata/source_attribution_composer.py")
				if err != nil {
					t.Fatal(err)
				}
				target.ClaudeSessionID = "11111111-1111-4111-8111-111111111111"
				transcriptDir := filepath.Join(session.GetClaudeConfigDir(), "projects", session.ConvertToClaudeDirName(home))
				if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
					t.Fatal(err)
				}
				transcript := filepath.Join(transcriptDir, target.ClaudeSessionID+".jsonl")
				if err := os.WriteFile(transcript, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				command = "python3 " + shellescape.Quote(composer) + " --capture " + shellescape.Quote(capture) + " --transcript " + shellescape.Quote(transcript)
			}
			if err := pane.Start(command); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pane.Kill() })
			target.SetTmuxSessionForTest(pane)
			target.Status = session.StatusIdle
			storage, err := session.NewStorageWithProfile("pi_source_test")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = storage.Close() })
			instances := []*session.Instance{sender, target}
			if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(capture); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("input recorder did not start")
				}
				time.Sleep(20 * time.Millisecond)
			}
			const message = "Please investigate this task"
			stdout, stderr, rc := runAgentDeckEnv(t, home, "", []string{"AGENTDECK_INSTANCE_ID=" + sender.ID}, "-p", "pi_source_test", "session", "send", "--no-wait", "--json", target.ID, message)
			if rc != 0 {
				t.Fatalf("send exited %d: stdout=%s stderr=%s", rc, stdout, stderr)
			}
			var receipt struct {
				Tagged bool `json:"tagged"`
			}
			if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
				t.Fatalf("decode send receipt: %v: %s", err, stdout)
			}
			if !receipt.Tagged {
				t.Errorf("Pi source must be attributed: %s", stdout)
			}
			want := session.SendEnvelope(sender.ID) + "\n" + message
			deadline = time.Now().Add(3 * time.Second)
			var input []byte
			for {
				input, err = os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(string(input), "\r") || time.Now().After(deadline) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			body := strings.ReplaceAll(strings.ReplaceAll(string(input), "\x1b[200~", ""), "\x1b[201~", "")
			if body != want+"\r" {
				t.Errorf("exact pane input = %q, want %q", body, want+"\r")
			}
			// Only claim success when the assertions above actually held: on a red
			// run this line used to contradict the failure just reported.
			if !t.Failed() {
				t.Logf("Pi -> %s: tagged=%v; source envelope and body reached real tmux; exactly one submit", targetTool, receipt.Tagged)
			}
		})
	}
}
