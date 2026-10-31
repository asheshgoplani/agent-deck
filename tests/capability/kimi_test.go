//go:build capability_e2e

package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const kimiNativeSessionID = "native-kimi-original"

type kimiLaunch struct {
	args []string
	env  map[string]string
}

type kimiHookStatus struct {
	Status     string `json:"status"`
	Event      string `json:"event"`
	SessionID  string `json:"session_id"`
	ClientType string `json:"client_type"`
}

func requireKimiContainer(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTDECK_TEST_CONTAINER") == "1" {
		return
	}
	for _, path := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Skip("Kimi CLI/tmux integration tests are container-only")
}

func setupKimiExecutable(t *testing.T, c *capSandbox) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "kimi.sh"))
	if err != nil {
		t.Fatalf("read deterministic Kimi executable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(c.ShimDir, "kimi"), data, 0o755); err != nil {
		t.Fatalf("install deterministic Kimi executable: %v", err)
	}
	if err := os.Symlink(c.BinPath, filepath.Join(c.ShimDir, "agent-deck")); err != nil {
		t.Fatalf("make agent-deck available to Kimi hooks: %v", err)
	}

	kimiHome := filepath.Join(c.Home, "isolated-kimi-home")
	if err := os.MkdirAll(kimiHome, 0o700); err != nil {
		t.Fatalf("create isolated Kimi home: %v", err)
	}
	configDir := filepath.Join(c.Home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create agent-deck config dir: %v", err)
	}
	config := fmt.Sprintf("[kimi]\ndefault_model = %q\nyolo_mode = true\nconfig_dir = %q\n", "model with spaces", kimiHome)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write Kimi config: %v", err)
	}
}

func readKimiLaunches(t *testing.T, c *capSandbox) []kimiLaunch {
	t.Helper()
	root := filepath.Join(c.Home, ".kimi-test-launches")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read Kimi launch records: %v", err)
	}
	launches := make([]kimiLaunch, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		argsData, err := os.ReadFile(filepath.Join(dir, "args"))
		if err != nil {
			t.Fatalf("read Kimi argv: %v", err)
		}
		var args []string
		if text := strings.TrimSuffix(string(argsData), "\n"); text != "" {
			args = strings.Split(text, "\n")
		}
		envData, err := os.ReadFile(filepath.Join(dir, "environment"))
		if err != nil {
			t.Fatalf("read Kimi environment: %v", err)
		}
		env := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(envData)), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				env[key] = value
			}
		}
		launches = append(launches, kimiLaunch{args: args, env: env})
	}
	return launches
}

func waitForKimiLaunches(t *testing.T, c *capSandbox, count int) []kimiLaunch {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		launches := readKimiLaunches(t, c)
		if len(launches) >= count {
			return launches
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("saw %d Kimi launches, want at least %d", len(readKimiLaunches(t, c)), count)
	return nil
}

func waitForKimiHook(t *testing.T, c *capSandbox, instanceID, event string) kimiHookStatus {
	t.Helper()
	path := filepath.Join(c.Home, ".local", "share", "agent-deck", "hooks", instanceID+".json")
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			last = string(data)
			var status kimiHookStatus
			if json.Unmarshal(data, &status) == nil && status.Event == event {
				return status
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	pane, paneErr := c.try("session", "output", instanceID, "--pane")
	t.Fatalf("Kimi hook event %q not observed; last status: %s\nrows: %+v\npane error: %v\npane:\n%s", event, last, c.list(t), paneErr, pane)
	return kimiHookStatus{}
}

func releaseKimiTurn(t *testing.T, c *capSandbox) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.Home, ".kimi-test-finish-turn"), []byte("finish\n"), 0o600); err != nil {
		t.Fatalf("release deterministic Kimi turn: %v", err)
	}
}

func TestCapability_Kimi_CLITmuxLifecycle(t *testing.T) {
	requireKimiContainer(t)
	c := newCapSandbox(t)
	setupKimiExecutable(t, c)

	const title = "cap-kimi-lifecycle"
	const prompt = "KIMI-PANE-PROMPT"
	c.run(t, "launch", c.WorkDir, "-c", "kimi", "-t", title, "-m", prompt)
	defer c.stopQuietly(title)

	row, ok := c.findByTitle(t, title)
	if !ok {
		t.Fatalf("Kimi launch did not create a registry row: %+v", c.list(t))
	}
	launches := waitForKimiLaunches(t, c, 1)
	if want := []string{"--model", "model with spaces", "--yolo"}; !slices.Equal(launches[0].args, want) {
		t.Fatalf("fresh Kimi argv = %q, want %q", launches[0].args, want)
	}
	env := launches[0].env
	for key, want := range map[string]string{
		"HOME":                  c.Home,
		"XDG_CONFIG_HOME":       filepath.Join(c.Home, ".config"),
		"XDG_STATE_HOME":        filepath.Join(c.Home, ".local", "state"),
		"KIMI_CODE_HOME":        filepath.Join(c.Home, "isolated-kimi-home"),
		"AGENTDECK_INSTANCE_ID": row.ID,
		"AGENTDECK_TITLE":       title,
		"AGENTDECK_TOOL":        "kimi",
	} {
		if env[key] != want {
			t.Errorf("fresh Kimi environment %s = %q, want %q", key, env[key], want)
		}
	}
	if _, ok := env["AGENTDECK_PROFILE"]; !ok {
		t.Error("fresh Kimi environment has no AGENTDECK_PROFILE")
	}
	if env["AGENTDECK_HOOK_GENERATION"] == "" {
		t.Error("fresh Kimi environment has no hook generation")
	}

	hook := waitForKimiHook(t, c, row.ID, "PostToolUse")
	if hook.Status != "running" || hook.SessionID != kimiNativeSessionID || hook.ClientType != "kimi_code_cli" {
		t.Fatalf("PostToolUse hook = %+v, want running with the native Kimi identity", hook)
	}
	if got, ok := c.waitForStatus(t, title, 10*time.Second, "running"); !ok {
		t.Fatalf("Kimi PostToolUse external status = %q, want running", got)
	}
	pane, ok := c.waitForPaneContains(t, title, "PROMPT:"+prompt, 10*time.Second)
	if !ok {
		t.Fatalf("initial prompt did not arrive through the pane:\n%s", pane)
	}

	releaseKimiTurn(t, c)
	hook = waitForKimiHook(t, c, row.ID, "Stop")
	if hook.Status != "waiting" {
		t.Fatalf("Stop hook status = %q, want waiting", hook.Status)
	}
	if got, ok := c.waitForStatus(t, title, 10*time.Second, "waiting"); !ok {
		t.Fatalf("Kimi Stop external status = %q, want waiting", got)
	}
	pane, ok = c.waitForPaneContains(t, title, "ECHO:"+prompt, 10*time.Second)
	if !ok {
		t.Fatalf("Kimi response missing from pane after Stop:\n%s", pane)
	}

	ownedPath := filepath.Join(c.Home, ".local", "share", "agent-deck", "hooks", row.ID+".kimi-session")
	ownedData, err := os.ReadFile(ownedPath)
	if err != nil {
		t.Fatalf("read persisted Kimi session identity: %v", err)
	}
	var owned struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(ownedData, &owned); err != nil {
		t.Fatalf("decode persisted Kimi session identity: %v", err)
	}
	if owned.ID != kimiNativeSessionID {
		t.Fatalf("persisted Kimi session ID = %q, want %q", owned.ID, kimiNativeSessionID)
	}

	// A separate restart CLI process cold-loads the saved row and owned identity.
	c.run(t, "session", "restart", title, "--force")
	launches = waitForKimiLaunches(t, c, 2)
	if want := []string{"--session", kimiNativeSessionID, "--yolo"}; !slices.Equal(launches[1].args, want) {
		t.Fatalf("cold-restart Kimi argv = %q, want %q", launches[1].args, want)
	}
	if launches[1].env["AGENTDECK_INSTANCE_ID"] != row.ID {
		t.Fatalf("restart instance ID = %q, want %q", launches[1].env["AGENTDECK_INSTANCE_ID"], row.ID)
	}
}

func installKimiRemoteSSH(t *testing.T, host *capSandbox) {
	t.Helper()
	var script strings.Builder
	script.WriteString("#!/usr/bin/env bash\nset -eu\nremote_command=\"${!#}\"\nexec env -i")
	for _, entry := range host.Env() {
		fmt.Fprintf(&script, " %q", entry)
	}
	script.WriteString(" /bin/bash -c 'cd \"$HOME\" && exec /bin/bash -c \"$1\"' bash \"$remote_command\"\n")

	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "ssh"), []byte(script.String()), 0o755); err != nil {
		t.Fatalf("install remote SSH transport shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENT_DECK_REMOTE_CHANNEL", "0")
	t.Cleanup(session.CloseRemoteChannels)
}

func waitForRemoteKimiStatus(t *testing.T, runner *session.SSHRunner, title, wanted string) []session.RemoteSessionInfo {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var rows []session.RemoteSessionInfo
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		rows, _, err = runner.FetchSessions(ctx)
		cancel()
		lastErr = err
		if err == nil {
			for _, row := range rows {
				if row.Title == title && row.Status == wanted {
					return rows
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	raw, rawErr := runner.Run(ctx, "list", "--json")
	cancel()
	t.Fatalf("remote Kimi row did not reach %q; rows: %+v; last error: %v\nraw list: %s\nraw error: %v", wanted, rows, lastErr, raw, rawErr)
	return nil
}

func TestCapability_Kimi_RemoteRowUsesHostStatus(t *testing.T) {
	requireKimiContainer(t)
	host := newCapSandbox(t)
	setupKimiExecutable(t, host)

	const title = "cap-kimi-remote"
	host.run(t, "add", "-c", "kimi", "-t", title, host.WorkDir)
	host.run(t, "session", "start", title)
	defer host.stopQuietly(title)
	if got, ok := host.waitForStatus(t, title, 10*time.Second, "waiting"); !ok {
		t.Fatalf("host Kimi status after start = %q, want waiting", got)
	}

	installKimiRemoteSSH(t, host)
	runner := session.NewSSHRunner("kimi-test-host", session.RemoteConfig{
		Host:          "isolated.test",
		AgentDeckPath: host.BinPath,
	})
	rows := waitForRemoteKimiStatus(t, runner, title, "waiting")
	for _, row := range rows {
		if row.Title == title && row.Tool != "kimi" {
			t.Fatalf("remote row tool = %q, want kimi", row.Tool)
		}
	}

	host.run(t, "session", "send", title, "REMOTE-KIMI-PROMPT", "--no-wait")
	waitForRemoteKimiStatus(t, runner, title, "running")
	releaseKimiTurn(t, host)
	waitForRemoteKimiStatus(t, runner, title, "waiting")
}
