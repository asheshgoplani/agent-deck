package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdInstallCreatesLogDirectoryBeforeActivation(t *testing.T) {
	for _, daemon := range []string{"notifier", "bridge"} {
		for _, location := range []string{"default", "xdg", "legacy", "override"} {
			if daemon == "notifier" && location == "override" {
				continue
			}
			t.Run(daemon+"/"+location, func(t *testing.T) {
				home, marker := systemdLogTestEnvironment(t)
				switch location {
				case "xdg":
					t.Setenv("XDG_DATA_HOME", filepath.Join(home, "custom data"))
				case "legacy":
					leaf := "logs"
					if daemon == "bridge" {
						leaf = "conductor"
					}
					if err := os.MkdirAll(filepath.Join(home, ".agent-deck", leaf), 0o700); err != nil {
						t.Fatal(err)
					}
				case "override":
					config := filepath.Join(home, ".config", "agent-deck")
					if err := os.MkdirAll(config, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(config, "config.toml"), []byte("[conductor]\ndir = '"+filepath.Join(home, "custom conductor")+"'\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				logDir := systemdLogTestDirectory(t, daemon)
				t.Setenv("EXPECTED_LOG_DIR", logDir)
				unitPath, err := systemdLogTestInstall(daemon)
				if err != nil {
					t.Fatalf("log directory must exist before service activation: %v", err)
				}
				info, err := os.Stat(logDir)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o700 {
					t.Fatalf("new log directory must be owner-only, got %04o", info.Mode().Perm())
				}
				unit, err := os.ReadFile(unitPath)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(unit), "StandardOutput=append:"+logDir+string(os.PathSeparator)) {
					t.Fatalf("installer prepared a different directory from the generated unit: %s", unit)
				}
				if strings.Contains(string(unit), "ExecStartPre=") {
					t.Fatal("mkdir ExecStartPre is ineffective before systemd opens append output")
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("service activation not requested: %v", err)
				}
			})
		}
	}
}

func TestSystemdInstallRejectsLogFileBeforeActivation(t *testing.T) {
	for _, daemon := range []string{"notifier", "bridge"} {
		t.Run(daemon, func(t *testing.T) {
			_, marker := systemdLogTestEnvironment(t)
			logDir := systemdLogTestDirectory(t, daemon)
			if err := os.MkdirAll(filepath.Dir(logDir), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(logDir, []byte("keep this file"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EXPECTED_LOG_DIR", logDir)
			if _, err := systemdLogTestInstall(daemon); err == nil || !strings.Contains(err.Error(), "log dir") {
				t.Fatalf("expected actionable log directory creation error, got %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("must not activate service after log directory failure: %v", err)
			}
			value, err := os.ReadFile(logDir)
			if err != nil || string(value) != "keep this file" {
				t.Fatalf("blocking user file changed: %q %v", value, err)
			}
		})
	}
}

func TestSystemdInstallPreservesActivationError(t *testing.T) {
	for _, daemon := range []string{"notifier", "bridge"} {
		t.Run(daemon, func(t *testing.T) {
			_, marker := systemdLogTestEnvironment(t)
			logDir := systemdLogTestDirectory(t, daemon)
			if err := os.MkdirAll(logDir, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EXPECTED_LOG_DIR", logDir)
			t.Setenv("SYSTEMD_LOG_TEST_FAIL_ENABLE", "1")
			path, err := systemdLogTestInstall(daemon)
			if path == "" || err == nil || !strings.Contains(err.Error(), "enable failed") {
				t.Fatalf("expected unit path and activation error, got %q %v", path, err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(logDir)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("existing private directory permissions changed: %v %v", info, err)
			}
		})
	}
}

func systemdLogTestEnvironment(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, "")
	}
	binDir := filepath.Join(home, "bin")
	runtimeDir := filepath.Join(home, "runtime")
	for _, dir := range []string{binDir, runtimeDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(home, "activation")
	t.Setenv("SYSTEMD_LOG_TEST_MARKER", marker)
	t.Setenv("SYSTEMD_LOG_TEST_FAIL_ENABLE", "")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	scripts := map[string]string{
		"loginctl": "#!/bin/sh\nexit 0\n",
		"systemctl": `#!/bin/sh
set -eu
if [ "$*" = "--user daemon-reload" ]; then exit 0; fi
printf '%s\n' "$*" >"$SYSTEMD_LOG_TEST_MARKER"
[ -d "$EXPECTED_LOG_DIR" ] || exit 9
[ "$SYSTEMD_LOG_TEST_FAIL_ENABLE" != 1 ] || exit 8
`,
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return home, marker
}

func systemdLogTestDirectory(t *testing.T, daemon string) string {
	t.Helper()
	var path string
	var err error
	if daemon == "bridge" {
		path, err = ConductorDir()
	} else {
		path, err = logDataPath("transition-notifier.log")
		path = filepath.Dir(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func systemdLogTestInstall(daemon string) (string, error) {
	if daemon == "bridge" {
		return installBridgeDaemonSystemd()
	}
	return installTransitionNotifierSystemd()
}
