package main

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestWebCommand_NoTuiFlag_SkipsBubbleteaBoot is the regression gate for the
// perf-driven `--no-tui` flag on the `web` subcommand. Investigation by the
// adeck-test-webui worker showed that `agent-deck web` boots a full bubbletea
// TUI in the same process as the HTTP server, which is the bulk of the
// ~60 MB RSS that @arioliveira reported as "heavy on M4".
//
// Two arms must both be GREEN simultaneously:
//
//   - "flag_extraction" — pure unit test on extractNoTuiFlag. Fails fast (no
//     subprocess) when the helper is missing or stops recognizing --no-tui.
//   - "headless_server_starts" — subprocess integration. Builds the binary
//     and runs `web --no-tui --listen 127.0.0.1:<free>` without a PTY. Asserts
//     the HTTP server responds on the chosen port within a short window,
//     which is only possible if bubbletea was actually skipped (bubbletea
//     blocks on stdin and panics without a TTY).
//
// On origin/main this fails because (1) extractNoTuiFlag does not exist, and
// (2) the binary rejects --no-tui as an unknown flag and exits 1.
func TestWebCommand_NoTuiFlag_SkipsBubbleteaBoot(t *testing.T) {
	t.Run("flag_extraction", func(t *testing.T) {
		cases := []struct {
			name       string
			in         []string
			wantNoTui  bool
			wantRemain []string
		}{
			{"absent", []string{"--listen", "127.0.0.1:8420"}, false, []string{"--listen", "127.0.0.1:8420"}},
			{"bare", []string{"--no-tui"}, true, []string{}},
			{"with_other_args", []string{"--no-tui", "--listen", "127.0.0.1:9000"}, true, []string{"--listen", "127.0.0.1:9000"}},
			{"after_other_args", []string{"--listen", "127.0.0.1:9000", "--no-tui"}, true, []string{"--listen", "127.0.0.1:9000"}},
			{"equals_true", []string{"--no-tui=true"}, true, []string{}},
			{"equals_false", []string{"--no-tui=false"}, false, []string{}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				gotNoTui, gotRemain := extractNoTuiFlag(tc.in)
				if gotNoTui != tc.wantNoTui {
					t.Errorf("extractNoTuiFlag(%v) noTui = %v, want %v", tc.in, gotNoTui, tc.wantNoTui)
				}
				if !equalStringSlices(gotRemain, tc.wantRemain) {
					t.Errorf("extractNoTuiFlag(%v) remain = %v, want %v", tc.in, gotRemain, tc.wantRemain)
				}
			})
		}
	})

	t.Run("headless_server_starts", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping subprocess integration test in short mode")
		}

		// Find a free port — bind, capture, close. Tiny race window, fine for a test.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("free port: %v", err)
		}
		listenAddr := ln.Addr().String()
		_ = ln.Close()

		tmpHome := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmpHome, ".agent-deck"), 0o755); err != nil {
			t.Fatal(err)
		}

		binPath := filepath.Join(t.TempDir(), "agent-deck-no-tui-test")
		build := exec.Command("go", "build", "-o", binPath, ".")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build: %v\noutput: %s", err, out)
		}

		// Strip TMUX*/AGENTDECK_*/HOME to dodge the nested-session guard,
		// matching the pattern in cgroup_isolation_wiring_test.go.
		var env []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "TMUX") ||
				strings.HasPrefix(kv, "AGENTDECK_") ||
				strings.HasPrefix(kv, "HOME=") {
				continue
			}
			env = append(env, kv)
		}
		env = append(env,
			"HOME="+tmpHome,
			"AGENTDECK_PROFILE=test-no-tui",
			"TERM=dumb",
		)

		cmd := exec.Command(binPath, "web", "--no-tui", "--listen", listenAddr)
		cmd.Env = env
		// Detach into its own pgroup so we can SIGTERM the whole group cleanly.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// Capture stderr to help diagnose RED failures.
		stderrPath := filepath.Join(tmpHome, "stderr.log")
		stderrFile, _ := os.Create(stderrPath)
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
		cmd.Stdout = stderrFile

		if err := cmd.Start(); err != nil {
			t.Fatalf("start binary: %v", err)
		}
		t.Cleanup(func() {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				_, _ = cmd.Process.Wait()
			}
		})

		// Poll the HTTP server. In TUI mode without a PTY, bubbletea would
		// panic or exit on the bad stdin within a few hundred ms. In --no-tui
		// mode, the server stays up.
		deadline := time.Now().Add(5 * time.Second)
		var lastErr error
		for time.Now().Before(deadline) {
			resp, err := http.Get("http://" + listenAddr + "/healthz")
			if err == nil {
				resp.Body.Close()
				// Accept any non-5xx response — we just want proof the
				// server is alive. Some routes may 404; that still proves
				// the HTTP listener is up.
				if resp.StatusCode < 500 {
					return // GREEN
				}
				lastErr = nil
			} else {
				lastErr = err
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Read stderr for diagnostic context on RED.
		stderrFile.Close()
		stderrBytes, _ := os.ReadFile(stderrPath)
		t.Fatalf("PERF-NO-TUI-MISSING: web --no-tui did not start an HTTP server on %s within 5s; lastErr=%v\nsubprocess output:\n%s",
			listenAddr, lastErr, string(stderrBytes))
	})
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
