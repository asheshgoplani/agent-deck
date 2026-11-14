package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	envFlagGHPSentinel  = "ghp_SENTINELabcdefghijklmnopqrstuvwxyz0123456789"
	envFlagAKIASentinel = "AKIASENTINEL0123456789"
)

func TestEnvVarFlags(t *testing.T) {
	env := newEnvVarFlags()

	for _, value := range []string{"API_URL=https://example.com?a=b", "EMPTY=", "API_URL=override"} {
		if err := env.Set(value); err != nil {
			t.Fatalf("Set(%q): %v", value, err)
		}
	}
	if err := env.Err(); err != nil {
		t.Fatalf("valid entries recorded an error: %v", err)
	}

	if got := env.Map()["API_URL"]; got != "override" {
		t.Fatalf("duplicate key should use the last value, got %q", got)
	}
	if got, ok := env.Map()["EMPTY"]; !ok || got != "" {
		t.Fatalf("empty value should be retained, got %q (present=%v)", got, ok)
	}
}

func TestEnvVarFlagsRejectsInvalidInput(t *testing.T) {
	for _, value := range []string{"MISSING_EQUALS", "=value", "BAD-KEY=value", "1KEY=value"} {
		env := newEnvVarFlags()
		if err := env.Set(value); err != nil {
			t.Errorf("Set(%q) returned %v; rejections must be deferred to Err so the flag package never echoes the value", value, err)
		}
		if env.Err() == nil {
			t.Errorf("Set(%q) unexpectedly accepted", value)
		}
		if len(env.Map()) != 0 {
			t.Errorf("Set(%q) stored a rejected entry: %v", value, env.Map())
		}
	}
}

// A malformed -env entry is often the secret itself ('API_KEY sk-…' with no
// '=', or a value with a bad key). Neither the deferred error nor the flag
// package's own parse output may carry it.
func TestEnvVarFlagsErrorsNeverCarryValue(t *testing.T) {
	const sentinel = "sk-test-SENTINEL-1234"
	for _, value := range []string{
		"AGENTDECK_TEST_SECRET " + sentinel,
		"BAD-KEY=" + sentinel,
		sentinel,
		"export AGENTDECK_TEST_SECRET=" + sentinel,
		// Bare tokens of identifier characters only (forgot KEY=): no
		// prefix of them may be shown.
		envFlagGHPSentinel,
		envFlagAKIASentinel,
		envFlagGHPSentinel + " =x",
		envFlagAKIASentinel + "/wJalr=x",
	} {
		env := newEnvVarFlags()
		_ = env.Set(value)
		if env.Err() == nil {
			t.Fatalf("Set(%q) unexpectedly accepted", value)
		}
		if strings.Contains(env.Err().Error(), sentinel) || strings.Contains(env.Err().Error(), "SENTINEL") {
			t.Errorf("rejection of %q leaks the value: %v", value, env.Err())
		}
	}
}

// Every command that takes -env must stop with exit 2 on a rejected entry
// (fail closed: a dropped exitOnEnvFlagError would otherwise run the command
// without the entry) and must not print any part of it.
func TestEnvFlagRejectedEntryExits2AtEveryCallSite(t *testing.T) {
	if os.Getenv("AGENT_DECK_ENVFLAG_HELPER") != "" {
		for i, arg := range os.Args {
			if arg != "--" {
				continue
			}
			rest := os.Args[i+1:]
			switch os.Getenv("AGENT_DECK_ENVFLAG_HELPER") {
			case "cliSessionRestart":
				cliSessionRestart("_envflag", rest)
			case "handleSessionRestart":
				handleSessionRestart("_envflag", rest)
			case "handleConductorSetup":
				handleConductorSetup("_envflag", rest)
			}
			os.Exit(0)
		}
		os.Exit(3)
	}
	for _, site := range []struct {
		name string
		args []string
	}{
		{"cliSessionRestart", []string{"envflag-missing-session"}},
		{"handleSessionRestart", []string{"envflag-missing-session"}},
		{"handleConductorSetup", []string{"envflagcond"}},
	} {
		for _, bad := range []string{envFlagGHPSentinel, envFlagAKIASentinel + "/x=v"} {
			t.Run(site.name, func(t *testing.T) {
				home := t.TempDir()
				args := append([]string{"-test.run=^TestEnvFlagRejectedEntryExits2AtEveryCallSite$", "--"}, site.args...)
				args = append(args, "--env", bad)
				cmd := exec.Command(os.Args[0], args...)
				cmd.Env = append(os.Environ(), "AGENT_DECK_ENVFLAG_HELPER="+site.name, "AGENT_DECK_TASK6_HELPER_PROCESS=1",
					"HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_DATA_HOME="+filepath.Join(home, "data"),
					"AGENTDECK_SKIP_UPDATE_CHECK=1")
				out, err := cmd.CombinedOutput()
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
					t.Fatalf("%s --env <rejected>: err=%v, want exit 2\n%s", site.name, err, out)
				}
				if !strings.Contains(string(out), "invalid value for flag -env") {
					t.Errorf("%s: rejection not reported:\n%s", site.name, out)
				}
				if strings.Contains(string(out), "SENTINEL") {
					t.Errorf("%s: output carries the rejected entry:\n%s", site.name, out)
				}
			})
		}
	}
}
