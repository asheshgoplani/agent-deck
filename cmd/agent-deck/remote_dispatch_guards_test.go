package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/creack/pty"
)

// Run the built CLI with real terminal descriptors: telemetry intentionally
// ignores ordinary subprocess pipes and agent/CI environments.
func TestCLIDispatchTelemetryCounters(t *testing.T) {
	bin := channelsCLIBinary(t)
	for _, command := range []struct {
		name     string
		args     []string
		counters map[string]int
	}{
		{"local", []string{"list", "--json"}, map[string]int{"cli_invocations": 1}},
		{"remote", []string{"remote", "list"}, map[string]int{"cli_invocations": 1, "remote_used": 1}},
	} {
		for _, control := range []string{"enabled", "disabled", "declined", "nonterminal", "help"} {
			t.Run(command.name+"/"+control, func(t *testing.T) {
				home := t.TempDir()
				socketDir := filepath.Join(home, "tmux")
				if err := os.Mkdir(socketDir, 0700); err != nil {
					t.Fatal(err)
				}
				dataDir := filepath.Join(home, "data", "agent-deck")
				if err := os.MkdirAll(dataDir, 0700); err != nil {
					t.Fatal(err)
				}
				statePath := filepath.Join(dataDir, telemetry.StateFileName)
				state := telemetry.State{SchemaVersion: telemetry.SchemaVersion, Consent: telemetry.ConsentGranted,
					ConsentEndpoint: telemetry.DefaultEndpoint, InstallID: strings.Repeat("a", 32), Counters: map[string]int{}}
				if control == "declined" {
					state.Consent = telemetry.ConsentDeclined
				}
				before, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(statePath, before, 0600); err != nil {
					t.Fatal(err)
				}
				args := append([]string(nil), command.args...)
				if control == "help" {
					args = append(args, "--help")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, bin, args...)
				cmd.Dir = home
				// An allowlist also clears all current/future CI and agent-session markers.
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home,
					"XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
					"XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "TERM=xterm-256color", "TMUX_TMPDIR=" + socketDir}
				if control == "disabled" {
					cmd.Env = append(cmd.Env, "AGENTDECK_TELEMETRY=0")
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if control == "nonterminal" {
					if out, err := cmd.Output(); err != nil {
						t.Fatalf("CLI: %v stdout=%q stderr=%q", err, out, stderr.String())
					}
				} else {
					master, slave, err := pty.Open()
					if err != nil {
						t.Fatal(err)
					}
					defer master.Close()
					cmd.Stdin, cmd.Stdout = slave, slave
					if err := cmd.Start(); err != nil {
						slave.Close()
						t.Fatal(err)
					}
					slave.Close()
					output := make(chan []byte, 1)
					go func() { b, _ := io.ReadAll(master); output <- b }()
					err = cmd.Wait()
					master.Close()
					out := <-output
					if err != nil {
						t.Fatalf("PTY CLI: %v stdout=%q stderr=%q", err, out, stderr.String())
					}
				}
				after, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				var got telemetry.State
				if err := json.Unmarshal(after, &got); err != nil {
					t.Fatal(err)
				}
				if control == "enabled" {
					if !reflect.DeepEqual(got.Counters, command.counters) {
						t.Fatalf("counters=%v, want exactly %v", got.Counters, command.counters)
					}
				} else if !bytes.Equal(before, after) {
					t.Fatalf("%s modified telemetry state: before=%s after=%s", control, before, after)
				}
			})
		}
	}
}
