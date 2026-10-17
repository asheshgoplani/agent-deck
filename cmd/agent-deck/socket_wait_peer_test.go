package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionSendSocketWaitPeerReply(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	for _, mode := range []string{"idle", "fast", "dropped", "busy", "partial", "next-turn", "short-fast", "short-partial", "late-turn"} {
		t.Run(mode, func(t *testing.T) {
			// Short HOME keeps the Unix socket below the platform path limit.
			home, err := os.MkdirTemp("", "socket-wait-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			config := filepath.Join(home, ".config", "agent-deck")
			binDir := filepath.Join(home, "bin")
			tmuxDir := filepath.Join(home, "tmux")
			for _, dir := range []string{config, binDir, tmuxDir} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(config, "config.toml"), []byte("send_transport = 'auto'\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			shim := "#!/bin/sh\nexec \"$SOCKET_WAIT_TEST_BINARY\" -test.run=^TestSocketWaitClaudeFixture$ -- \"$@\" 2>\"$HOME/socket-wait-helper.stderr\"\n"
			if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(shim), 0o700); err != nil {
				t.Fatal(err)
			}
			env := []string{
				"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
				// A reused tmux server retains the first launch's environment.
				// Each fixture needs its own HOME, helper and mode in that env.
				"TMUX_TMPDIR=" + tmuxDir,
				"SOCKET_WAIT_TEST_BINARY=" + os.Args[0], "SOCKET_WAIT_FIXTURE=" + mode,
				"AGENT_DECK_TASK6_HELPER_PROCESS=1",
			}
			out, stderr, code := runAgentDeckEnv(t, home, "", env, "launch", home,
				"--title", "socket-wait", "--cmd", "claude", "--no-parent", "--no-wait", "--no-assert-done", "--json")
			if code != 0 {
				t.Fatalf("fixture launch failed: %d %s %s", code, out, stderr)
			}
			t.Cleanup(func() { runAgentDeckEnv(t, home, "", env, "session", "stop", "socket-wait") })
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(home, "socket-wait-ready")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					diagnostic, _ := os.ReadFile(filepath.Join(home, "socket-wait-helper.stderr"))
					t.Fatalf("fixture did not publish its verified socket record: helper stderr=%s", diagnostic)
				}
				time.Sleep(20 * time.Millisecond)
			}
			budget := "5s"
			if strings.HasPrefix(mode, "short-") || mode == "late-turn" {
				budget = "300ms"
			}
			out, stderr, code = runAgentDeckEnv(t, home, "", env, "session", "send", "socket-wait",
				"--no-tag", "--no-wait", "--wait", "--timeout", budget, "--json", "reply ok")
			finished := time.Now()
			t.Logf("send exit=%d stdout=%s stderr=%s", code, out, stderr)
			// Busy waits may return before the receiver flushes its frame marker.
			deadline = time.Now().Add(time.Second)
			var frame []byte
			for {
				frame, err = os.ReadFile(filepath.Join(home, "socket-wait-frame"))
				if err == nil || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil || !strings.Contains(string(frame), `"content":"reply ok"`) {
				t.Fatalf("fixture did not receive the actual socket message: %s %v", frame, err)
			}
			if budget == "300ms" {
				info, err := os.Stat(filepath.Join(home, "socket-wait-frame"))
				if err != nil {
					t.Fatal(err)
				}
				// Start at socket receipt, excluding unrelated CLI startup work.
				if elapsed := finished.Sub(info.ModTime()); elapsed > 600*time.Millisecond {
					t.Fatalf("socket wait exceeded its shared 300ms budget: %s", elapsed)
				}
			}
			var result map[string]interface{}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("socket --wait did not emit one JSON result: %v", err)
			}
			if mode == "dropped" || mode == "partial" || mode == "short-partial" {
				if code != 1 || result["success"] != false || result["content"] != nil || strings.Contains(out, "STALE REPLY") {
					t.Fatalf("unanswered socket message returned stale/partial success: %d %s", code, out)
				}
				return
			}
			if code != 0 || result["transport"] != "socket" || result["verified"] != false || result["submitted"] != false || result["acknowledged"] != false {
				t.Fatalf("socket --wait failed its unverified delivery contract: %d %s", code, out)
			}
			if mode == "busy" {
				if result["wait_outcome"] != waitOutcomeUnverifiedBusyTarget || result["content"] != nil {
					t.Fatalf("busy socket target must skip output: %s", out)
				}
				return
			}
			if result["wait_outcome"] != waitOutcomeObservedNotCorrelated || result["content"] != "PEER REPLY" || result["completion"] != "complete" {
				t.Fatalf("idle peer reply was not observed without a correlation claim: %s", out)
			}
		})
	}
}

// The helper is only the external paid tool; every parent assertion invokes
// the real agent-deck CLI, socket resolver, transport and transcript reader.
func TestSocketWaitClaudeFixture(t *testing.T) {
	mode := os.Getenv("SOCKET_WAIT_FIXTURE")
	if mode == "" {
		return
	}
	for _, arg := range os.Args {
		if arg == "--version" {
			fmt.Println("Claude Code 2.1.296")
			return
		}
	}
	sid := ""
	for index, arg := range os.Args {
		if (arg == "--session-id" || arg == "--resume") && index+1 < len(os.Args) {
			sid = os.Args[index+1]
		}
	}
	if sid == "" {
		t.Fatal("fixture needs a native session ID")
	}
	home := os.Getenv("HOME")
	config := os.Getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		config = filepath.Join(home, ".claude")
	}
	project, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(config, "projects", session.ConvertToClaudeDirName(project), sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	appendJSONL(t, transcript, jsonlUser("old-user", "2000-01-01T00:00:00Z", "old prompt"),
		jsonlAssistant("old-reply", "2000-01-01T00:00:00Z", "STALE REPLY", "end_turn"))
	listener, err := net.Listen("unix", filepath.Join(home, "inbox.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", os.Getpid()))
	procStart := ""
	if err == nil {
		procStart = strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])[19]
	} else {
		cmd := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(os.Getpid()))
		cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
		value, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		procStart = strings.TrimSpace(string(value))
	}
	record, err := json.Marshal(map[string]interface{}{
		"pid": os.Getpid(), "sessionId": sid, "procStart": procStart,
		"peerProtocol": 1, "messagingSocketPath": listener.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	recordDir := filepath.Join(config, "sessions")
	if err := os.MkdirAll(recordDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordDir, fmt.Sprintf("%d.json", os.Getpid())), record, 0o600); err != nil {
		t.Fatal(err)
	}
	paint := func(busy bool) {
		if busy {
			fmt.Print("\x1b[2J\x1b[HClaude Code\nWorking (esc to interrupt)\n")
		} else {
			fmt.Print("\x1b[2J\x1b[HClaude Code\n> \n")
		}
	}
	paint(mode == "busy")
	if err := os.WriteFile(filepath.Join(home, "socket-wait-ready"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "socket-wait-frame"), line, 0o600); err != nil {
		t.Fatal(err)
	}
	if mode != "dropped" && mode != "busy" {
		if mode == "late-turn" {
			time.Sleep(200 * time.Millisecond)
		}
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		appendJSONL(t, transcript, fmt.Sprintf(`{"type":"user","uuid":"peer-user","sessionId":%q,"timestamp":%q,"isMeta":true,"origin":{"kind":"peer"},"message":{"role":"user","content":"Another Claude session sent a message:\nreply ok\n\nThis came from another Claude session, not typed by your user."}}`, sid, ts))
		if mode == "idle" || mode == "partial" {
			paint(true)
			time.Sleep(time.Second)
		}
		stopReason := "end_turn"
		if mode == "partial" || mode == "short-partial" {
			stopReason = "tool_use"
		}
		appendJSONL(t, transcript, jsonlAssistant("peer-reply", time.Now().UTC().Format(time.RFC3339Nano), "PEER REPLY", stopReason))
		if mode == "next-turn" {
			appendJSONL(t, transcript, jsonlUser("later-user", time.Now().UTC().Format(time.RFC3339Nano), "unrelated follow-up"))
			paint(true)
		} else {
			paint(false)
		}
	}
	time.Sleep(30 * time.Second)
}

func TestSessionSendSocketWaitHelpDescribesSharedBudget(t *testing.T) {
	out, stderr, code := runAgentDeckEnv(t, t.TempDir(), "", nil, "session", "send", "--help")
	if code != 0 {
		t.Fatalf("help failed: %d %s %s", code, out, stderr)
	}
	if !strings.Contains(out, "Claude socket waits share --timeout") || strings.Contains(out, "first waits up to 30s for the turn to start") {
		t.Fatalf("help must describe socket observation/reply budget, not the old 30s bound: %s", out)
	}
}
