package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The local TUI dials `agent-deck remote-agent` without -p when the remote's
// profile is "" or "default" (session.SSHRunner.buildRemoteCommand), so the
// remote resolves the profile itself: AGENTDECK_PROFILE, then default_profile.
// The agent's probe, pane capturer and requests resolved it, but its change
// feed watched GetDBPathForProfile(""), the default profile's state.db. On a
// remote whose default_profile is work, a change to work's sessions was never
// pushed; the TUI only saw it at its next periodic fetch.
func TestRemoteAgent_ChangeFeedWatchesTheProfileItServes(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real agent-deck processes")
	}
	tests := []struct {
		name          string
		flag          []string // global flags before remote-agent
		env           string   // AGENTDECK_PROFILE
		configDefault string   // config.json default_profile
	}{
		{name: "default_profile", configDefault: "work"},
		{name: "AGENTDECK_PROFILE", env: "work"},
		{name: "-p wins", flag: []string{"-p", "work"}, env: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			project := filepath.Join(home, "proj")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}
			// A short dir of its own: the probe asks tmux for session state,
			// and the socket path must stay under 108 bytes.
			tmuxDir, err := os.MkdirTemp("", "ra-tmux-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(tmuxDir) })
			env := []string{
				"AGENTDECK_PROFILE=" + tt.env,
				"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
				"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
				"TMUX_TMPDIR=" + tmuxDir,
			}
			cli := func(args ...string) {
				t.Helper()
				if stdout, stderr, code := runAgentDeckEnv(t, home, "", env, args...); code != 0 {
					t.Fatalf("agent-deck %v: exit %d\n%s\n%s", args, code, stdout, stderr)
				}
			}
			// Sessions go to work through -p, whatever the env selects.
			cli("-p", "work", "add", project, "--title", "one", "--no-parent")
			if tt.configDefault != "" {
				cli("profile", "default", tt.configDefault)
			}

			agent := exec.Command(channelsCLIBinary(t), append(tt.flag, "remote-agent")...)
			agent.Env = agentDeckTestEnv(home, env)
			stdin, err := agent.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := agent.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stderrFile, err := os.Create(filepath.Join(home, "agent.stderr"))
			if err != nil {
				t.Fatal(err)
			}
			defer stderrFile.Close()
			agent.Stderr = stderrFile
			stderr := func() string {
				b, _ := os.ReadFile(stderrFile.Name())
				return string(b)
			}
			if err := agent.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			t.Cleanup(func() {
				_ = stdin.Close() // the agent exits when stdin closes
				select {
				case <-exited:
				case <-time.After(10 * time.Second):
					_ = agent.Process.Kill()
					<-exited
				}
			})
			events := make(chan remoteAgentReply, 64)
			// Wait only once stdout is drained: it closes the pipe.
			go func() {
				defer close(exited)
				sc := bufio.NewScanner(stdout)
				sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
				for sc.Scan() {
					var r remoteAgentReply
					if json.Unmarshal(sc.Bytes(), &r) == nil {
						select {
						case events <- r:
						default: // nobody is waiting: drop it
						}
					}
				}
				_, _ = io.Copy(io.Discard, stdout)
				close(events)
				_ = agent.Wait()
			}()
			waitEvent := func(name string, within time.Duration) *remoteAgentReply {
				t.Helper()
				deadline := time.After(within)
				for {
					select {
					case r, ok := <-events:
						if !ok {
							t.Fatalf("remote-agent exited before %q\nstderr: %s", name, stderr())
						}
						if r.Event == name {
							return &r
						}
					case <-deadline:
						return nil
					}
				}
			}
			if waitEvent("ready", 15*time.Second) == nil {
				t.Fatalf("remote-agent never said ready\nstderr: %s", stderr())
			}

			// The feed seeds itself with one listing right after "ready". An
			// add that lands before the seed is in the seed, so a second add
			// is made if the first one is not pushed.
			for i := 1; i <= 2; i++ {
				title := "added-" + strconv.Itoa(i)
				cli("-p", "work", "add", project, "--title", title, "--no-parent")
				if r := waitEvent("changed", 8*time.Second); r != nil {
					if r.Sessions != "" && !strings.Contains(r.Sessions, title) {
						t.Fatalf("pushed listing lacks %q: %s", title, r.Sessions)
					}
					return
				}
			}
			t.Fatalf("remote-agent pushed no \"changed\" after two sessions were added to the profile it serves\nstderr: %s", stderr())
		})
	}
}
