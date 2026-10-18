package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
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
//
// The agent pins the profile it resolved, so when the host's default_profile
// changes under an agent started without -p, the agent exits for a reconnect;
// one started with -p keeps serving its profile.
func TestRemoteAgent_ChangeFeedWatchesTheProfileItServes(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real agent-deck processes")
	}
	tests := []struct {
		name          string
		flag          []string // global flags before remote-agent
		env           string   // AGENTDECK_PROFILE
		configDefault string   // config.json default_profile
		moveDefaultTo string   // default_profile set while the agent runs
	}{
		{name: "default_profile", configDefault: "work", moveDefaultTo: "other"},
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
			waitExit := func(within time.Duration) bool {
				deadline := time.After(within)
				for {
					select {
					case _, ok := <-events:
						if !ok {
							return true
						}
					case <-deadline:
						return false
					}
				}
			}
			if waitEvent("ready", 15*time.Second) == nil {
				t.Fatalf("remote-agent never said ready\nstderr: %s", stderr())
			}

			// The feed seeds itself with one listing right after "ready". An
			// add that lands before the seed is in the seed, so a second add
			// is made if the first one is not pushed.
			pushed := false
			for i := 1; i <= 2 && !pushed; i++ {
				title := "added-" + strconv.Itoa(i)
				cli("-p", "work", "add", project, "--title", title, "--no-parent")
				if r := waitEvent("changed", 8*time.Second); r != nil {
					if r.Sessions != "" && !strings.Contains(r.Sessions, title) {
						t.Fatalf("pushed listing lacks %q: %s", title, r.Sessions)
					}
					pushed = true
				}
			}
			if !pushed {
				t.Fatalf("remote-agent pushed no \"changed\" after two sessions were added to the profile it serves\nstderr: %s", stderr())
			}

			switch {
			case tt.moveDefaultTo != "":
				// SaveConfig rewrites config.json in place: a poll that reads
				// it empty must keep the profile, not take it for "default".
				configPath := filepath.Join(home, ".config", "agent-deck", "config.json")
				saved, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatalf("read config.json: %v", err)
				}
				if err := os.WriteFile(configPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				exited := waitExit(remoteAgentProfileEvery + 2*time.Second)
				if err := os.WriteFile(configPath, saved, 0o600); err != nil {
					t.Fatal(err)
				}
				if exited {
					t.Fatalf("remote-agent exited while config.json was empty\nstderr: %s", stderr())
				}
				cli("profile", "create", tt.moveDefaultTo)
				cli("profile", "default", tt.moveDefaultTo)
				if !waitExit(remoteAgentProfileEvery + 10*time.Second) {
					t.Fatalf("remote-agent kept serving work after default_profile moved to %s\nstderr: %s", tt.moveDefaultTo, stderr())
				}
				if !strings.Contains(stderr(), "the host now selects another profile") {
					t.Fatalf("remote-agent exited without saying why\nstderr: %s", stderr())
				}
			case len(tt.flag) > 0:
				// The host selects other here: an agent pinned by -p must not
				// take that for a moved profile.
				if waitExit(remoteAgentProfileEvery + 2*time.Second) {
					t.Fatalf("remote-agent started with -p exited\nstderr: %s", stderr())
				}
			}
		})
	}
}

// When the host would now pick another profile, the agent exits the way it
// does for a newer binary, so the controller redials; until then it serves.
func TestRemoteAgent_ExitsWhenTheHostSelectsAnotherProfile(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inW.Close()
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	var moved atomic.Bool
	var checks atomic.Int64
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, outW, remoteAgentConfig{
			Run: func(context.Context, []string) (string, string, int) { return "", "", 0 },
			ProfileMoved: func() bool {
				checks.Add(1)
				return moved.Load()
			},
			ProfileEvery: 5 * time.Millisecond,
		})
		_ = outW.Close()
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for checks.Load() < 5 {
		if time.Now().After(deadline) {
			t.Fatal("the profile was never checked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("agent exited while the host still selected its profile")
	default:
	}
	moved.Store(true)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not exit after the host selected another profile")
	}
}
