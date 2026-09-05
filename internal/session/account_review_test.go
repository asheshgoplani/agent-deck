package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/stretchr/testify/require"
)

func accountReviewConfig(t *testing.T, config string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	dir := filepath.Join(home, "config", "agent-deck")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0600))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	return home
}

func TestAccountReviewDeepSeekCannotUseAmbientHome(t *testing.T) {
	accountReviewConfig(t, "[profiles.empty]\n[profiles.named.deepseek]\nconfig_dir = '~/.named-dsh'\n")
	t.Setenv("DSH_HOME", t.TempDir())
	require.ErrorContains(t, ValidateAccountSlot("empty", "deepseek"), "DeepSeek config_dir")
	require.NoError(t, ValidateAccountSlot("named", "deepseek"))
	require.NoError(t, ValidateAccountSlot("", "deepseek"))
}

func TestAccountReviewShellValidationMatchesProvenance(t *testing.T) {
	accountReviewConfig(t, "[profiles.empty]\n")
	for _, passthrough := range []bool{false, true} {
		inst := &Instance{Tool: "shell", Command: "claude mcp list", SubcommandPassthrough: passthrough}
		_, _, err := SetField(inst, FieldAccount, "empty", nil)
		if passthrough {
			require.ErrorContains(t, err, "Claude config_dir")
			require.Empty(t, inst.Account)
		} else {
			require.NoError(t, err)
			require.Equal(t, "empty", inst.Account)
		}
	}
}

func TestAccountReviewFallbackShellRestartKeepsTypedCommandAccount(t *testing.T) {
	home := accountReviewConfig(t, "[profiles.saved]\n[profiles.ambient]\n")
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("AGENTDECK_ACCOUNT", "ambient")
	inst := NewInstanceWithTool("account-shell", home, "shell")
	inst.Account = "saved"
	inst.Command = "true"
	t.Cleanup(func() { _ = inst.Kill() })
	require.NoError(t, inst.Start())
	require.False(t, inst.GetTmuxSession().RunCommandAsInitialProcess)
	for _, restart := range []bool{false, true} {
		if restart {
			require.NoError(t, inst.Restart())
		}
		output := filepath.Join(t.TempDir(), "account")
		require.NoError(t, inst.GetTmuxSession().SendKeysAndEnter("printf '%s' \"$AGENTDECK_ACCOUNT\" > "+shellescape.Quote(output)))
		require.Eventually(t, func() bool { _, err := os.Stat(output); return err == nil }, 5*time.Second, 10*time.Millisecond)
		data, err := os.ReadFile(output)
		require.NoError(t, err)
		require.Equal(t, "saved", string(data), "typed command after restart=%v", restart)
	}
}

func TestAccountReviewPassthroughLifecycleRejectsBeforeSpawn(t *testing.T) {
	home := accountReviewConfig(t, "[profiles.empty]\n")
	for _, tool := range []string{"claude", "codex"} {
		for _, operation := range []string{"start", "message", "restart"} {
			t.Run(tool+"/"+operation, func(t *testing.T) {
				// An invalid project path is an independent early spawn failure on the
				// old code. Account refusal must precede it, without starting a tool.
				inst := NewInstanceWithTool("invalid-account", filepath.Join(home, "missing"), "shell")
				inst.Account, inst.Command, inst.SubcommandPassthrough = "empty", tool+" mcp list", true
				var err error
				switch operation {
				case "start":
					err = inst.Start()
				case "message":
					err = inst.StartWithMessage("hello")
				case "restart":
					err = inst.Restart()
				}
				require.ErrorContains(t, err, "config_dir")
			})
		}
	}
}

func TestAccountReviewPassthroughConfigPathsFollowExecutionHome(t *testing.T) {
	home := accountReviewConfig(t, "[profiles.named.claude]\nconfig_dir = '~/.claude space; literal'\n")
	bin := filepath.Join(home, "bin")
	require.NoError(t, os.MkdirAll(bin, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nprintf '%s\\n' \"$CLAUDE_CONFIG_DIR\" \"$CLAUDE_SECURESTORAGE_CONFIG_DIR\"\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, remote := range []bool{false, true} {
		inst := &Instance{Tool: "shell", Command: "claude mcp list", SubcommandPassthrough: true, Account: "named"}
		executionHome := home
		if remote {
			inst.SSHHost = "fixture.invalid"
			executionHome = t.TempDir()
		}
		payload := inst.buildShellPassthroughCommand(inst.Command)
		cmd := exec.Command("bash", "-c", payload)
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "HOME=") {
				cmd.Env = append(cmd.Env, item)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+executionHome)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		want := filepath.Join(executionHome, ".claude space; literal")
		require.Equal(t, want+"\n"+want+"\n", string(output), "remote=%v", remote)
	}
}
