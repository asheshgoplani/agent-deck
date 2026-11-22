package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestInstallPythonDepsChecksImportsBeforePip(t *testing.T) {
	for _, tc := range []struct {
		name, config, missing string
		wantProbe             []string // fragments the import probe must contain
		wantPip               []string
	}{
		{
			name:   "all configured integrations already importable without pip",
			config: "[conductor.telegram]\ntoken = 't'\n[conductor.slack]\nbot_token = 's'\n[conductor.discord]\nbot_token = 'd'\n[conductor.mattermost]\nbot_token = 'm'\n",
			wantProbe: []string{
				"import toml",
				"from aiogram import Bot, Dispatcher, types",
				"from aiogram.filters import Command, CommandStart",
				"from aiogram.client.session.aiohttp import AiohttpSession",
				"from slack_bolt.async_app import AsyncApp",
				"from slack_bolt.authorization import AuthorizeResult",
				"from slack_sdk.web.async_client import AsyncWebClient",
				"import aiohttp",
				"import discord; from discord import app_commands",
			},
		},
		{
			name:      "telegram only does not need other integrations",
			config:    "[conductor.telegram]\ntoken = 't'\n",
			wantProbe: []string{"from aiogram.filters import Command, CommandStart"},
		},
		{
			name:    "missing dependency falls back to pinned pip install",
			config:  "[conductor.telegram]\ntoken = 't'\n",
			missing: "from aiogram import Bot, Dispatcher, types",
			wantPip: []string{"-m", "pip", "install", "--quiet", "--user", "toml==0.10.2", "aiogram==3.13.1"},
		},
		{
			// aiogram 2.x provides the root module but not aiogram.filters or
			// aiogram.client.session.aiohttp, so a root-only probe would skip
			// pip and leave the bridge silently disabled.
			name:    "incompatible aiogram major falls back to pinned pip install",
			config:  "[conductor.telegram]\ntoken = 't'\n",
			missing: "from aiogram.client.session.aiohttp import AiohttpSession",
			wantPip: []string{"-m", "pip", "install", "--quiet", "--user", "toml==0.10.2", "aiogram==3.13.1"},
		},
		{
			// discord.py 1.x imports discord but has no app_commands module.
			name:    "incompatible discord.py major falls back to pinned pip install",
			config:  "[conductor.discord]\nbot_token = 'd'\n",
			missing: "from discord import app_commands",
			wantPip: []string{"-m", "pip", "install", "--quiet", "--user", "toml==0.10.2", "discord.py==2.7.1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxConductorHome(t)
			cfgPath, err := session.GetUserConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfgPath, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)

			bin := t.TempDir()
			t.Setenv("PATH", bin)
			t.Setenv("MISSING_SYMBOL", tc.missing)
			pipLog := filepath.Join(bin, "pip-argv")
			probeLog := filepath.Join(bin, "probes")
			t.Setenv("PIP_LOG", pipLog)
			t.Setenv("PROBE_LOG", probeLog)
			// The probe call carries "sys.path"; fails when the statement
			// names MISSING_SYMBOL, as an incompatible major would.
			const fake = `#!/bin/sh
if [ "$1" = "-c" ] && [ "${2#*sys.path}" != "$2" ]; then
  printf '%s\n' "$2" >> "$PROBE_LOG"
  if [ -n "$MISSING_SYMBOL" ]; then
    case "$2" in *"$MISSING_SYMBOL"*) exit 1 ;; esac
  fi
  exit 0
fi
if [ "$1" = "-c" ] && [ "${2#import sys;}" != "$2" ]; then
  printf '%s\n' "$0" 3.9.18
  exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "pip" ]; then
  printf '%s\n' "$@" >> "$PIP_LOG"
  exit 0
fi
exit 1
`
			if err := os.WriteFile(filepath.Join(bin, "python3"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}
			if !installPythonDeps() {
				t.Error("bridge dependencies should be available")
			}
			probes, err := os.ReadFile(probeLog)
			if err != nil {
				t.Fatalf("the import probe never ran: %v", err)
			}
			for _, fragment := range tc.wantProbe {
				if !strings.Contains(string(probes), fragment) {
					t.Errorf("import probe does not contain %q:\n%s", fragment, probes)
				}
			}
			raw, err := os.ReadFile(pipLog)
			if tc.wantPip == nil {
				if !os.IsNotExist(err) {
					t.Fatalf("pip should not have run: argv=%q, err=%v", raw, err)
				}
			} else if err != nil {
				t.Fatalf("reading pip argv: %v", err)
			} else if got := strings.Split(strings.TrimSpace(string(raw)), "\n"); !slices.Equal(got, tc.wantPip) {
				t.Errorf("pip argv = %q, want %q", got, tc.wantPip)
			}
		})
	}
}

// TestInstallPythonDepsPinsEveryPackage: conductor setup installs the bridge's
// Python packages at exact versions, and the manual-install commands it
// prints on failure name the same versions. A fake interpreter at the venv
// path the resolver prefers records the pip argv and fails like a PEP 668
// Python, so both the automatic install and the printed fix are observed.
func TestInstallPythonDepsPinsEveryPackage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   []string // package names, in install order
	}{
		{"telegram only", "[conductor.telegram]\ntoken = \"t\"\n", []string{"toml", "aiogram"}},
		{"slack only", "[conductor.slack]\nbot_token = \"b\"\n", []string{"toml", "slack-bolt", "slack-sdk", "aiohttp"}},
		{"discord and mattermost", "[conductor.discord]\nbot_token = \"d\"\n\n[conductor.mattermost]\nbot_token = \"m\"\n", []string{"toml", "discord.py", "aiohttp"}},
		{"nothing configured", "", []string{"toml", "aiogram", "slack-bolt", "slack-sdk", "aiohttp", "discord.py"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxConductorHome(t)
			cfgPath, err := session.GetUserConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfgPath, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)

			venv := session.ConductorVenvDir()
			if venv == "" {
				t.Fatal("no conductor venv dir")
			}
			bin := filepath.Join(venv, "bin")
			if err := os.MkdirAll(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			argvLog := filepath.Join(t.TempDir(), "argv")
			fake := "#!/bin/sh\n" +
				"if [ \"$1\" = \"-m\" ] && [ \"$2\" = \"pip\" ]; then\n" +
				"  printf '%s\\n' \"$@\" >> '" + argvLog + "'\n" +
				"  echo 'error: externally-managed-environment' >&2\n" +
				"  exit 1\n" +
				"fi\n" +
				"exit 1\n"
			if err := os.WriteFile(filepath.Join(bin, "python3"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}

			stderr := captureStderr(t, func() {
				if installPythonDeps() {
					t.Error("installPythonDeps reported success from a failing pip")
				}
			})

			raw, err := os.ReadFile(argvLog)
			if err != nil {
				t.Fatalf("pip was never run: %v", err)
			}
			// Two attempts (--user, then plain), each ending in the packages.
			attempts := strings.Split(strings.TrimSpace(string(raw)), "-m\npip\ninstall\n")
			ran := 0
			var specs []string
			for _, attempt := range attempts {
				if attempt == "" {
					continue
				}
				ran++
				args := strings.Split(strings.TrimSpace(attempt), "\n")
				var pkgs []string
				for _, a := range args {
					if !strings.HasPrefix(a, "-") {
						pkgs = append(pkgs, a)
					}
				}
				if len(pkgs) != len(tc.want) {
					t.Fatalf("pip installed %v, want one exact pin each for %v", pkgs, tc.want)
				}
				for i, spec := range pkgs {
					name, version, pinned := strings.Cut(spec, "==")
					if !pinned || name != tc.want[i] || !exactVersion(version) {
						t.Errorf("pip package %q is not %s pinned to one exact version", spec, tc.want[i])
					}
				}
				if specs == nil {
					specs = pkgs
				} else if strings.Join(specs, " ") != strings.Join(pkgs, " ") {
					t.Errorf("the two pip attempts installed different packages: %v vs %v", specs, pkgs)
				}
			}
			if ran != 2 {
				t.Errorf("pip ran %d times, want 2: %q", ran, raw)
			}
			hint := "/bin/pip install " + strings.Join(specs, " ")
			if !strings.Contains(stderr, hint) {
				t.Errorf("printed fix does not install the pinned versions (want %q)\n%s", hint, stderr)
			}
		})
	}
}

// exactVersion reports whether v is a plain release number such as 3.13.1,
// with no range, wildcard or extra specifier.
func exactVersion(v string) bool {
	if v == "" {
		return false
	}
	for _, part := range strings.Split(v, ".") {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return false
		}
	}
	return true
}

// TestPinBridgePackages_PicksTableByPythonVersion: Python 3.8 and 3.9 get the
// legacy table (aiogram 3.13.1 and an aiohttp it accepts); 3.10 and newer
// (and an interpreter whose version could not be read) get the current table,
// whose versions ship wheels for 3.14, so setup never has to compile aiohttp
// from source there.
func TestPinBridgePackages_PicksTableByPythonVersion(t *testing.T) {
	names := []string{"toml", "aiogram", "slack-bolt", "slack-sdk", "aiohttp", "discord.py"}
	for _, tc := range []struct {
		version string
		want    map[string]string
	}{
		{"3.8.18", bridgePythonPinsLegacy},
		{"3.9", bridgePythonPinsLegacy},
		{"3.10.14", bridgePythonPins},
		{"3.14.5", bridgePythonPins},
		{"3.80.1", bridgePythonPins},
		{"", bridgePythonPins},
	} {
		got := pinBridgePackages(names, tc.version)
		for i, name := range names {
			if want := name + "==" + tc.want[name]; got[i] != want {
				t.Errorf("python %q: got %q, want %q", tc.version, got[i], want)
			}
		}
	}
	// 3.10.11 has security fixes 3.10.10 lacks and still fits aiogram
	// 3.13.1's aiohttp<3.11 range on 3.8/3.9.
	if got := pinBridgePackages([]string{"aiohttp"}, "3.9.18"); got[0] != "aiohttp==3.10.11" {
		t.Errorf("python 3.9 aiohttp pin: got %q, want aiohttp==3.10.11", got[0])
	}
	if bridgePythonPins["aiohttp"] == bridgePythonPinsLegacy["aiohttp"] {
		t.Error("current and legacy tables pin the same aiohttp; the legacy one has no 3.13+ wheels")
	}
}
