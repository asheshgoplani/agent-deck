package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

func TestReadonlyFollowerDoesNotRecoverWorkers(t *testing.T) {
	called := false
	recoverFollowerWorkers("default", true, func(string, string, string) { called = true })
	if called {
		t.Fatal("read-only following must not restart pending workers")
	}
}

func TestReadonlyEventHelpSynopsis(t *testing.T) {
	for _, action := range []string{"follow", "stats"} {
		stdout, stderr, code := runAgentDeck(t, t.TempDir(), "events", action, "--help")
		if code != 0 {
			t.Fatalf("%s help exited %d", action, code)
		}
		found := false
		for _, line := range strings.Split(stdout+stderr, "\n") {
			if strings.HasPrefix(line, "Usage:") {
				found = true
				if !strings.Contains(line, "[--read-only]") {
					t.Fatalf("%s synopsis omits read-only: %s", action, line)
				}
				break
			}
		}
		if !found {
			t.Fatalf("%s help omitted its synopsis", action)
		}
	}
}

func TestReadonlyEventCommandsObserveWithoutWriting(t *testing.T) {
	home := t.TempDir()
	writeMacappConfig(t, home, "[macapp]\nplugins=true\n")
	out, stderr, code := runAgentDeck(t, home, "events", "publish",
		"--kind", "macapp.probe", "--session", "readonly-child", "--data", `{"probe":true}`, "--json")
	if code != 0 {
		t.Fatalf("seed: %s %s", out, stderr)
	}
	busDir := filepath.Join(home, ".local", "share", "agent-deck", "bus", "ch_support_test")
	logPath := filepath.Join(busDir, "active.ndjson")
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, code = runAgentDeck(t, home, "events", "stats", "--read-only", "--json")
	if code != 0 || !strings.Contains(out, `"macapp.probe": 1`) {
		t.Fatalf("stats: %d %s %s", code, out, stderr)
	}
	cmd := exec.Command(channelsCLIBinary(t), "events", "follow", "--read-only", "--json",
		"--kind", "macapp.", "--session", "readonly-child")
	cmd.Env = agentDeckTestEnv(home, nil)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		if scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	select {
	case line := <-lines:
		if !strings.Contains(line, `"session_id":"readonly-child"`) {
			t.Fatal(line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI follower did not observe seeded event")
	}
	after, err := os.ReadFile(logPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("read-only command changed the event log")
	}
	if leases, _ := filepath.Glob(filepath.Join(busDir, "want", "*")); len(leases) != 0 {
		t.Fatal("read-only commands acquired demand leases")
	}
}

func TestReadonlyFollowerUsesResolvedProfileAndNormalizedBus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	events.SetProfile("resolved-reader")
	t.Cleanup(func() { events.SetProfile("default") })
	// Seed the exact resolved profile independently of the default process bus.
	reader, err := events.OpenReader("resolved-reader")
	if err == nil {
		reader.Close()
		t.Fatal("absent reader must not create storage")
	}
	writer, err := events.OpenAt(filepath.Join(home, "data", "agent-deck", "bus", "resolved-reader"), events.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Commit("session.turn", "child", map[string]string{"state": "completed"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"events", " EVENTS ", ""} {
		bus, err := openFollowerBus(name, true)
		if err != nil {
			t.Fatal(err)
		}
		if !bus.ReadOnly() || bus.Stats().Dir != writer.Stats().Dir {
			t.Fatal("follower must be read-only and use the resolved profile")
		}
		release := bus.Want("tmux.output")
		if matches, _ := filepath.Glob(filepath.Join(bus.Stats().Dir, "want", "*")); len(matches) != 0 {
			t.Fatal("read-only follower created a demand lease")
		}
		release()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		sub, err := bus.Subscribe(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case frame := <-sub.Frames():
			if frame.SessionID != "child" {
				t.Fatal("wrong profile event")
			}
		case <-ctx.Done():
			t.Fatal("seeded event not observed")
		}
		cancel()
		bus.Close()
	}
}
