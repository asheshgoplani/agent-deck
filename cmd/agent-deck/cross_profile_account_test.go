package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestCrossProfileAccountEnvironment(t *testing.T) {
	cases := []struct {
		name, profile string
		unsetProfile  bool
		emptyProfile  bool
		unsetAccount  bool
		emptyAccount  bool
		clear         bool
	}{
		{name: "different profile", profile: "work", clear: true},
		{name: "same profile", profile: "it"},
		{name: "no flag"},
		{name: "no caller profile", profile: "work", unsetProfile: true},
		{name: "empty caller profile", profile: "work", emptyProfile: true},
		{name: "absent account variables", profile: "work", unsetAccount: true, clear: true},
		{name: "empty account variables", profile: "work", emptyAccount: true, clear: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldProfile, oldSet, oldOverridden := inheritedProfileEnv, inheritedProfileEnvSet, profileEnvOverridden
			oldAccountEnv, oldAccountOverridden := inheritedAccountEnv, accountEnvOverridden
			t.Cleanup(func() {
				inheritedProfileEnv, inheritedProfileEnvSet, profileEnvOverridden = oldProfile, oldSet, oldOverridden
				inheritedAccountEnv, accountEnvOverridden = oldAccountEnv, oldAccountOverridden
			})
			profileEnvOverridden = false
			t.Setenv("AGENTDECK_PROFILE", "it")
			if tc.unsetProfile {
				if err := os.Unsetenv("AGENTDECK_PROFILE"); err != nil {
					t.Fatal(err)
				}
			} else if tc.emptyProfile {
				t.Setenv("AGENTDECK_PROFILE", "")
			}
			t.Setenv("AGENTDECK_ACCOUNT", "it")
			t.Setenv("CLAUDE_CONFIG_DIR", "/caller/claude")
			t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "/caller/claude")
			for _, key := range []string{"AGENTDECK_ACCOUNT", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
				if tc.unsetAccount {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				} else if tc.emptyAccount {
					t.Setenv(key, "")
				}
			}
			type value struct {
				text string
				set  bool
			}
			before := make(map[string]value)
			for _, key := range []string{"AGENTDECK_PROFILE", "AGENTDECK_ACCOUNT", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
				text, set := os.LookupEnv(key)
				before[key] = value{text, set}
			}
			applyProfileFlag(tc.profile)
			for _, key := range []string{"AGENTDECK_ACCOUNT", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
				text, set := os.LookupEnv(key)
				want := before[key]
				if tc.clear {
					want = value{}
				}
				if (value{text, set}) != want {
					t.Errorf("caller account leaked across profile selection: %s=(%q,%v), want (%q,%v)", key, text, set, want.text, want.set)
				}
			}
			wrapped := make(map[string]value)
			for _, kv := range inheritedEnviron() {
				key, text, _ := strings.Cut(kv, "=")
				wrapped[key] = value{text, true}
			}
			for key, want := range before {
				if wrapped[key] != want {
					t.Errorf("wrapped statusLine environment changed: %s=%+v, want %+v", key, wrapped[key], want)
				}
			}
		})
	}
}

func TestCrossProfileLaunchUsesSelectedAccount(t *testing.T) {
	for _, tc := range []struct {
		name, profile, explicit, wantAccount string
	}{
		{name: "target profile", profile: "work", wantAccount: ""},
		{name: "explicit account", profile: "work", explicit: "it", wantAccount: "it"},
		{name: "same profile", profile: "it", wantAccount: "it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			configDir := filepath.Join(home, ".config", "agent-deck")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			config := "[profiles.it.claude]\nconfig_dir = \"~/.claude-it\"\n[profiles.work.claude]\nconfig_dir = \"~/.claude-work\"\n"
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			binDir := filepath.Join(home, "bin")
			if err := os.MkdirAll(binDir, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(home, "child-config-dir")
			shim := "#!/bin/sh\nif [ \"$1\" = '--version' ]; then echo 2.0.0; exit 0; fi\nprintf '%s' \"$CLAUDE_SECURESTORAGE_CONFIG_DIR\" > \"$HOME/child-secure-dir\"\nprintf '%s' \"$CLAUDE_CONFIG_DIR\" > \"$HOME/child-config-dir\"\nexec sleep 60\n"
			if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(shim), 0o700); err != nil {
				t.Fatal(err)
			}
			env := []string{"AGENTDECK_PROFILE=it", "AGENTDECK_ACCOUNT=it", "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude-it"), "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + filepath.Join(home, ".claude-it"), "PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH")}
			args := []string{"-p", tc.profile, "launch", home, "--title", "cross-profile", "--cmd", "claude", "--no-parent", "--no-wait", "--no-assert-done", "--json"}
			if tc.explicit != "" {
				args = append(args, "--account", tc.explicit)
			}
			t.Cleanup(func() {
				runAgentDeckEnv(t, home, "", env, "-p", tc.profile, "session", "stop", "cross-profile")
			})
			stdout, stderr, code := runAgentDeckEnv(t, home, "", env, args...)
			if code != 0 {
				t.Fatalf("launch exit=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			deadline := time.Now().Add(10 * time.Second)
			var childDir []byte
			for time.Now().Before(deadline) {
				childDir, _ = os.ReadFile(marker)
				if len(childDir) > 0 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			wantDir := filepath.Join(home, ".claude-"+tc.profile)
			if tc.explicit != "" {
				wantDir = filepath.Join(home, ".claude-"+tc.explicit)
			}
			if string(childDir) != wantDir {
				t.Errorf("launched Claude config_dir=%q, want %q", childDir, wantDir)
			}
			secureDir, err := os.ReadFile(filepath.Join(home, "child-secure-dir"))
			if err != nil {
				t.Fatalf("child secure-storage marker unavailable: %v", err)
			}
			wantSecure := ""
			if tc.wantAccount != "" {
				wantSecure = wantDir
			}
			if string(secureDir) != wantSecure {
				t.Errorf("caller secure-storage selector leaked: got %q, want %q", secureDir, wantSecure)
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
			storage, err := session.NewStorageWithProfile(tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			instances, err := storage.Load()
			if err != nil || len(instances) != 1 {
				t.Fatalf("persisted sessions=%d, error=%v", len(instances), err)
			}
			if instances[0].Account != tc.wantAccount {
				t.Errorf("persisted caller account=%q, want %q", instances[0].Account, tc.wantAccount)
			}
		})
	}
}

func TestCrossProfileHelpPrecedence(t *testing.T) {
	stdout, stderr, code := runAgentDeck(t, t.TempDir(), "--help")
	if code != 0 || stderr != "" {
		t.Fatalf("help exit=%d stderr=%q", code, stderr)
	}
	for _, text := range []string{
		"When -p differs from a nonempty AGENTDECK_PROFILE",
		"CLAUDE_SECURESTORAGE_CONFIG_DIR",
		"Explicit --account still overrides the selected profile",
	} {
		if !strings.Contains(stdout, text) {
			t.Errorf("profile/account help is missing %q", text)
		}
	}
}
