package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionRestartAllSelfHandoff(t *testing.T) {
	if helper := os.Getenv("AGENT_DECK_BULK_HANDOFF_HELPER"); helper != "" {
		runRestartAllSelfHandoffHelper(t, helper, os.Getenv("AGENT_DECK_BULK_HANDOFF_CASE"))
		return
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	for _, handler := range []string{"legacy", "core"} {
		for _, tc := range []struct {
			name    string
			handoff bool
			exit    int
		}{
			{"id", true, 0},
			{"tmux", true, 0},
			{"stale-id", true, 0},
			{"quiet", true, 0},
			{"human", true, 0},
			{"start-failure", false, 1},
			{"outside", false, 0},
			{"inactive", false, 1},
			{"child", false, 0},
		} {
			t.Run(handler+"/"+tc.name, func(t *testing.T) {
				home := t.TempDir()
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionRestartAllSelfHandoff$")
				cmd.Env = agentDeckTestEnv(home, []string{
					"AGENT_DECK_TASK6_HELPER_PROCESS=1",
					"AGENT_DECK_BULK_HANDOFF_HELPER=" + handler,
					"AGENT_DECK_BULK_HANDOFF_CASE=" + tc.name,
					"AGENTDECK_SKIP_UPDATE_CHECK=1",
					"AGENTDECK_EVENTS_BUS=off",
				})
				output, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("handler timed out: %v\n%s", ctx.Err(), output)
				}
				exit := 0
				if err != nil {
					if ee, ok := err.(*exec.ExitError); ok {
						exit = ee.ExitCode()
					} else {
						t.Fatalf("start helper: %v", err)
					}
				}
				if exit != tc.exit {
					t.Fatalf("exit = %d, want %d\n%s", exit, tc.exit, output)
				}
				text := string(output)
				if strings.Contains(text, "HANDOFF ") != tc.handoff {
					t.Fatalf("handoff = %v, want %v\n%s", strings.Contains(text, "HANDOFF "), tc.handoff, output)
				}
				if tc.handoff {
					if !strings.Contains(text, "4242") || !strings.Contains(text, "/tmp/bulk-handoff.log") {
						t.Fatalf("missing pid/log receipt:\n%s", output)
					}
					if tc.name == "human" || tc.name == "quiet" {
						for _, want := range []string{"all active sessions", "including bulk-self", "Read the batch result in /tmp/bulk-handoff.log"} {
							if !strings.Contains(text, want) {
								t.Errorf("missing batch receipt %q:\n%s", want, output)
							}
						}
						if strings.Contains(text, "agent-deck session show") {
							t.Errorf("batch receipt points to a single session:\n%s", output)
						}
					}
					var got struct {
						Profile string
						Args    []string
					}
					for _, line := range strings.Split(text, "\n") {
						if strings.HasPrefix(line, "HANDOFF ") {
							if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "HANDOFF ")), &got); err != nil {
								t.Fatal(err)
							}
						}
					}
					want := []string{"session", "restart", "--all", "--force", "--env", "BULK_VALUE=two words"}
					if tc.name == "quiet" {
						want = append(want, "--quiet")
					} else if tc.name != "human" {
						want = append(want, "--json")
					}
					if got.Profile != "bulk_handoff_test" || strings.Join(got.Args, "\x00") != strings.Join(want, "\x00") {
						t.Fatalf("handoff lost profile/argv: %+v, want %q", got, want)
					}
				}
				if tc.name == "start-failure" && !strings.Contains(text, "detached start failed") {
					t.Fatalf("start failure was not reported:\n%s", output)
				}
				if tc.name == "inactive" && !strings.Contains(text, "no active sessions") {
					t.Fatalf("inactive self was treated as a batch target:\n%s", output)
				}
				if tc.name == "child" && !strings.Contains(text, "CHILD_MARKER_CLEARED") {
					t.Fatalf("child marker was not consumed:\n%s", output)
				}
			})
		}
	}
}

func runRestartAllSelfHandoffHelper(t *testing.T, handler, scenario string) {
	t.Helper()
	const profile = "bulk_handoff_test"
	project := filepath.Join(os.Getenv("HOME"), "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	self := session.NewInstanceWithTool("bulk-self", project, "shell")
	stale := session.NewInstanceWithTool("stopped-id", project, "shell")
	// Batch membership is live tmux existence, even if saved status is stopped.
	self.Status = session.StatusStopped
	if scenario == "inactive" {
		self.Status = session.StatusRunning
	} else {
		if err := self.GetTmuxSession().Start("sleep 60"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = self.GetTmuxSession().Kill() })
	}
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveWithGroups([]*session.Instance{stale, self}, nil); err != nil {
		_ = storage.Close()
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	caller, name := self.ID, ""
	switch scenario {
	case "tmux", "child":
		caller, name = "", self.GetTmuxSession().Name
	case "stale-id":
		caller, name = stale.ID, self.GetTmuxSession().Name
	case "outside":
		caller = "not-in-this-profile"
	}
	selfHandoffCallerID = func() string { return caller }
	selfHandoffTmuxName = func() string { return name }
	selfHandoffStart = func(p string, sub []string) (selfHandoff, error) {
		if scenario == "start-failure" {
			// exitCLI exits immediately; reap this helper's private pane first.
			_ = self.GetTmuxSession().Kill()
			return selfHandoff{}, fmt.Errorf("detached start failed")
		}
		payload, err := json.Marshal(struct {
			Profile string
			Args    []string
		}{p, sub})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("HANDOFF %s\n", payload)
		return selfHandoff{PID: 4242, Log: "/tmp/bulk-handoff.log"}, nil
	}
	if scenario == "child" {
		t.Setenv(selfHandoffParentEnv, "-1")
	}
	args := []string{"--all", "--force", "--env", "BULK_VALUE=two words"}
	if scenario == "quiet" {
		args = append(args, "--quiet")
	} else if scenario != "human" {
		args = append(args, "--json")
	}
	if handler == "core" {
		cliSessionRestart(profile, args)
	} else {
		handleSessionRestart(profile, args)
	}
	if scenario == "child" {
		if _, exists := os.LookupEnv(selfHandoffParentEnv); !exists {
			fmt.Println("CHILD_MARKER_CLEARED")
		}
	}
}
