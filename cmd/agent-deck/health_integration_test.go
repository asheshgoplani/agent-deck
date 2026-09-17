package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestRuntimeHealthStartupConfigAndProfileIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		enabled      bool
	}{
		{"default", "", true},
		{"enabled", "[health]\nenabled = true\n", true},
		{"disabled", "[health]\nenabled = false\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR"} {
				t.Setenv(key, "")
			}
			t.Setenv("AGENTDECK_PROFILE", "unselected")
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)
			configPath := filepath.Join(home, ".config", "agent-deck", "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			stop := startRuntimeHealth("selected", "web")
			stop()
			stop() // shutdown paths may overlap
			report, err := readRuntimeHealth("selected", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if tc.enabled {
				if len(report.Processes) != 1 || report.Processes[0].Latest.Role != "web" || report.Processes[0].Latest.PID != os.Getpid() {
					t.Fatalf("startup report: %+v", report)
				}
			} else {
				dir, err := healthLogDir("selected")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("disabled sampler created state: %v", err)
				}
			}
			dir, err := healthLogDir("unselected")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("sampler wrote to unselected profile: %v", err)
			}
		})
	}
}

func TestHealthRemoteExecJSONParity(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Fatalf("health parity requires OpenSSH client: %v", err)
	}
	bin := channelsCLIBinary(t)
	controller, remote, shim := t.TempDir(), t.TempDir(), t.TempDir()
	configPath := filepath.Join(controller, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nprofile = 'selected'\nagent_deck_path = '%s'\n", bin)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	startParitySSH(t, remote, shim)
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	// Distinct profile histories prove remote exec reads the owner's selected data.
	now := time.Now().UTC().Add(-time.Second)
	for _, fixture := range []struct {
		home, profile, role string
		pid                 int
	}{
		{remote, "selected", "remote-web", 101},
		{remote, "default", "wrong-profile", 102},
		{controller, "selected", "controller-web", 103},
	} {
		dir := filepath.Join(fixture.home, ".local", "share", "agent-deck", "profiles", fixture.profile, "logs", "health")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		sample := health.Sample{Version: 1, Timestamp: now, StartedAt: now.Add(-time.Minute), Role: fixture.role, PID: fixture.pid}
		data, err := json.Marshal(sample)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(home string, args ...string) health.Summary {
		t.Helper()
		out, stderr, code := runAgentDeck(t, home, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s %s", args, code, out, stderr)
		}
		var report health.Summary
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		report.Since = time.Time{} // each CLI computes its own time window boundary
		return report
	}
	local := read(remote, "-p", "selected", "health", "--json", "--since", "1h")
	if len(local.Processes) != 1 || local.Processes[0].Latest.Role != "remote-web" {
		t.Fatalf("remote fixture missing: %+v", local)
	}
	overSSH := read(controller, "remote", "exec", "lab", "health", "--json", "--since", "1h")
	if !reflect.DeepEqual(local, overSSH) {
		t.Fatalf("remote JSON differs:\nlocal: %+v\nremote: %+v", local, overSSH)
	}
}

func TestHealthReadDoesNotStartSampler(t *testing.T) {
	home := t.TempDir()
	out, stderr, code := runAgentDeck(t, home, "-p", "reader", "health", "--json")
	if code != 0 {
		t.Fatalf("health: %d %s %s", code, out, stderr)
	}
	dir := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "reader", "logs", "health")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("health read created sampler state: %v", err)
	}
}

func TestRuntimeHealthHeadlessWebStartup(t *testing.T) {
	home := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	output, err := os.Create(filepath.Join(home, "web.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd := exec.Command(channelsCLIBinary(t), "-p", "selected", "web", "--no-tui", "--listen", address)
	// A normal CLI binary does not have the Go test socket guard. Restore an
	// explicit private base after sandboxedCLIEnv strips inherited TMUX variables.
	socket, cleanupTmux := testutil.ShortTmuxSocket()
	t.Cleanup(cleanupTmux) // resolves this same directory and kills before removal
	cmd.Env = append(sandboxedCLIEnv(home), "TMUX_TMPDIR="+filepath.Dir(socket))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	client := &http.Client{Timeout: time.Second}
	dir := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "selected", "logs", "health")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + "/healthz")
		if err == nil {
			response.Body.Close()
			report, err := health.Report(dir, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Processes) == 1 && report.Processes[0].Latest.Role == "web" && report.Processes[0].Latest.PID == cmd.Process.Pid {
				if report.Processes[0].Latest.BinaryVersion != Version {
					t.Fatalf("sample missing running binary's version: got %q, want %q", report.Processes[0].Latest.BinaryVersion, Version)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(output.Name())
	t.Fatalf("headless web did not serve with its own health sample: %s", data)
}

func TestDoctorUnreadableHealthPreservesAccounts(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "reader", "logs", "health")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	out, stderr, code := runAgentDeck(t, home, "-p", "reader", "doctor", "--json")
	if code != 0 {
		t.Fatalf("doctor lost account diagnostics: %d %s %s", code, out, stderr)
	}
	var report struct {
		AccountSlots json.RawMessage `json:"account_slots"`
		Health       health.Summary  `json:"health"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.AccountSlots == nil || len(report.Health.Flags) == 0 {
		t.Fatalf("doctor must retain account diagnostics and flag unknown health: %s", out)
	}
}
