package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// pluginsOffHint is the machine-readable hint `limits --json` and
// `events publish --json` carry when [macapp] plugins is off.
type pluginsOffHint struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Code    string `json:"code"`
	Hint    struct {
		Key     string   `json:"key"`
		Value   any      `json:"value"`
		Command string   `json:"command"`
		Argv    []string `json:"argv"`
		Consent string   `json:"consent"`
		Docs    string   `json:"docs"`
	} `json:"hint"`
}

// TestMacappPluginsOffErrorSaysWhatToRun: with [macapp] plugins off, limits
// and events publish name the exact command that enables it, in text and as
// a JSON hint the Mac app can show and run after the user agrees.
func TestMacappPluginsOffErrorSaysWhatToRun(t *testing.T) {
	const enable = "agent-deck config set macapp.plugins true --yes"
	wantArgv := []string{"config", "set", "macapp.plugins", "true", "--yes"}
	home := t.TempDir()
	for _, args := range [][]string{
		{"limits", "--json"},
		{"events", "publish", "--kind", "macapp.canvas.show", "--data", `{}`, "--json"},
	} {
		stdout, stderr, code := runAgentDeck(t, home, args...)
		if code != 2 {
			t.Fatalf("%v: exit %d, want 2 (%s %s)", args, code, stdout, stderr)
		}
		var res pluginsOffHint
		if err := json.Unmarshal([]byte(stdout), &res); err != nil {
			t.Fatalf("%v: not JSON: %v %s", args, err, stdout)
		}
		if res.Success || res.Code != ErrCodeInvalidOperation || !strings.Contains(res.Error, enable) || !strings.Contains(res.Error, "plugins = true") {
			t.Errorf("%v: error %q code %q, want it to name %q", args, res.Error, res.Code, enable)
		}
		h := res.Hint
		if h.Key != "macapp.plugins" || h.Value != true || h.Command != enable || !reflect.DeepEqual(h.Argv, wantArgv) || h.Consent == "" || h.Docs != "docs/macapp-core.md" {
			t.Errorf("%v: hint %+v", args, h)
		}
		// Human output carries the same command.
		_, stderr, code = runAgentDeck(t, home, args[:len(args)-1]...)
		if code != 2 || !strings.Contains(stderr, enable) {
			t.Errorf("%v (text): exit %d stderr %q, want it to name %q", args, code, stderr, enable)
		}
	}
}

// TestConfigSetMacappPluginsNeedsConsent: turning [macapp] plugins on needs
// --yes (or an interactive yes); without it config set changes nothing and
// returns CONSENT_REQUIRED with the consent text and the confirm argv. Turning
// it off, and every other key, needs no consent.
func TestConfigSetMacappPluginsNeedsConsent(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "config", "set", "macapp.plugins", "true", "--json")
	if code != 2 {
		t.Fatalf("set without --yes: exit %d, want 2 (%s %s)", code, stdout, stderr)
	}
	var res struct {
		Success     bool     `json:"success"`
		Code        string   `json:"code"`
		Error       string   `json:"error"`
		Key         string   `json:"key"`
		Value       any      `json:"value"`
		Consent     string   `json:"consent"`
		ConfirmArgv []string `json:"confirm_argv"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("not JSON: %v %s", err, stdout)
	}
	if res.Success || res.Code != "CONSENT_REQUIRED" || res.Key != "macapp.plugins" || res.Value != true || res.Consent == "" ||
		!reflect.DeepEqual(res.ConfirmArgv, []string{"config", "set", "macapp.plugins", "true", "--yes"}) {
		t.Fatalf("consent error: %+v", res)
	}
	if stdout, _, _ := runAgentDeck(t, home, "config", "get", "macapp.plugins"); strings.TrimSpace(stdout) != "false" {
		t.Fatalf("set without consent changed the value: %q", stdout)
	}
	// Non-interactive text mode (stdin is not a terminal) refuses too.
	if _, stderr, code := runAgentDeck(t, home, "config", "set", "macapp.plugins", "true"); code != 2 || !strings.Contains(stderr, "--yes") {
		t.Fatalf("text set without --yes: exit %d %q", code, stderr)
	}
	for _, yes := range []string{"--yes", "-y"} {
		if _, _, code := runAgentDeck(t, home, "config", "set", "macapp.plugins", "false"); code != 0 {
			t.Fatalf("turning plugins off needs no consent: exit %d", code)
		}
		stdout, stderr, code := runAgentDeck(t, home, "config", "set", "macapp.plugins", "true", yes, "--json")
		if code != 0 || !strings.Contains(stdout, `"value": true`) {
			t.Fatalf("set %s: exit %d %s %s", yes, code, stdout, stderr)
		}
	}
	// With consent given, limits runs.
	if stdout, stderr, code := runAgentDeck(t, home, "limits", "--json"); code != 0 {
		t.Fatalf("limits after consent: exit %d %s %s", code, stdout, stderr)
	}
	// The schema tells the app which keys need consent.
	stdout, _, _ = runAgentDeck(t, home, "config", "schema", "--json")
	var schema struct {
		Keys []struct {
			Key     string `json:"key"`
			Consent string `json:"consent"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(stdout), &schema); err != nil {
		t.Fatal(err)
	}
	for _, k := range schema.Keys {
		if (k.Consent != "") != (k.Key == "macapp.plugins") {
			t.Errorf("schema %s consent %q", k.Key, k.Consent)
		}
	}
}

// TestConfigSetMacappPluginsInteractiveConsent: in a terminal, config set
// shows the consent text and asks; only y or yes turns plugins on.
func TestConfigSetMacappPluginsInteractiveConsent(t *testing.T) {
	home := t.TempDir()
	run := func(answer string) (string, int) {
		t.Helper()
		cmd := exec.Command(channelsCLIBinary(t), "config", "set", "macapp.plugins", "true")
		cmd.Env = agentDeckTestEnv(home, nil)
		terminal, err := pty.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = terminal.Close() }()
		var out bytes.Buffer
		buf := make([]byte, 4096)
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(out.String(), "[y/N]") {
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				t.Fatalf("no prompt: %q", out.String())
			}
			n, err := terminal.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				t.Fatalf("read before prompt: %v %q", err, out.String())
			}
		}
		if _, err := terminal.Write([]byte(answer + "\n")); err != nil {
			t.Fatal(err)
		}
		for {
			n, err := terminal.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				break
			}
		}
		code := 0
		var exitErr *exec.ExitError
		if err := cmd.Wait(); errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), code
	}
	out, code := run("n")
	if code != 1 || !strings.Contains(out, "Mac app acts on") || !strings.Contains(out, "Not changed.") {
		t.Fatalf("answer n: exit %d %q", code, out)
	}
	if stdout, _, _ := runAgentDeck(t, home, "config", "get", "macapp.plugins"); strings.TrimSpace(stdout) != "false" {
		t.Fatalf("declined consent changed the value: %q", stdout)
	}
	if out, code := run("y"); code != 0 || !strings.Contains(out, "true") {
		t.Fatalf("answer y: exit %d %q", code, out)
	}
	if stdout, _, _ := runAgentDeck(t, home, "config", "get", "macapp.plugins"); strings.TrimSpace(stdout) != "true" {
		t.Fatalf("consent did not turn plugins on: %q", stdout)
	}
}
