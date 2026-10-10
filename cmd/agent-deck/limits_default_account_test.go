package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// limitsAccount is one `limits --json` account as the Mac app reads it.
type limitsAccount struct {
	Harness string `json:"harness"`
	Name    string `json:"name"`
	Windows []struct {
		Window  string  `json:"window"`
		UsedPct float64 `json:"used_pct"`
	} `json:"windows"`
	Source    string `json:"source"`
	UpdatedAt string `json:"updated_at"`
	Stale     bool   `json:"stale"`
	Error     string `json:"error"`
	Default   bool   `json:"default"`
}

// limitsReport decodes `limits --json`.
type limitsReport struct {
	Accounts []limitsAccount `json:"accounts"`
}

func decodeLimits(t *testing.T, stdout string) limitsReport {
	t.Helper()
	var report limitsReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &report), stdout)
	return report
}

func claudeAccount(report limitsReport, name string) (limitsAccount, bool) {
	for _, a := range report.Accounts {
		if a.Harness == "claude" && a.Name == name {
			return a, true
		}
	}
	return limitsAccount{}, false
}

func requireDefaultWindows(t *testing.T, stdout string) {
	t.Helper()
	acc, ok := claudeAccount(decodeLimits(t, stdout), "default")
	require.True(t, ok, "limits --json lacks the default Claude account: %s", stdout)
	require.True(t, acc.Default, "the default account must be marked default: %s", stdout)
	require.Empty(t, acc.Error, stdout)
	require.NotEmpty(t, acc.UpdatedAt, stdout)
	require.Equal(t, "quota cache (statusLine ingester)", acc.Source)
	windows := map[string]float64{}
	for _, w := range acc.Windows {
		windows[w.Window] = w.UsedPct
	}
	require.Equal(t, map[string]float64{"5h": 23.5, "7d": 48}, windows, stdout)
}

// T21-6 as the Mac app runs it: a fresh HOME whose only Claude login is the
// default ~/.claude (no [profiles.<name>.claude] slot), `hooks install`, the
// configured statusLine command run with a real payload, then
// `-p default limits --json`. CORE-CHANGES 9: the default account carries the
// same 5h/7d the statusLine reported, and no account slot is invented.
func TestLimitsDefaultAccountT21_6(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	binDir := filepath.Join(home, ".local", "bin")
	for _, d := range []string{claudeDir, binDir} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	require.NoError(t, os.Symlink(channelsCLIBinary(t), filepath.Join(binDir, "agent-deck")))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{}`), 0o600))
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n")
	env := []string{"CODEX_HOME=" + filepath.Join(home, "absent-codex"), "PATH=" + binDir + ":" + os.Getenv("PATH")}

	_, stderr, code := runAgentDeckEnv(t, home, "", env, "-p", "default", "hooks", "install")
	require.Equal(t, 0, code, stderr)
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &root))
	var line struct{ Command string }
	require.NoError(t, json.Unmarshal(root["statusLine"], &line))
	require.Contains(t, line.Command, "-p default usage statusline-wrap", "the default login's feed")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", line.Command)
	cmd.Env = agentDeckTestEnv(home, env)
	cmd.Stdin = strings.NewReader(remoteLimitsPayload)
	output, err := cmd.Output()
	require.NoError(t, err)
	require.Contains(t, string(output), "5h 23.5%")

	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "-p", "default", "limits", "--json")
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	requireDefaultWindows(t, stdout)

	// No account slot is invented for the default login.
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "-p", "default", "accounts", "--json")
	require.Equal(t, 0, code, stderr)
	require.JSONEq(t, `[]`, stdout)
}

// The default login with no feed wired is still listed, with the explicit
// no-feed error, so the app can tell "no feed" from "no account".
func TestLimitsDefaultAccountWithoutFeed(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n")
	env := []string{"CODEX_HOME=" + filepath.Join(home, "absent-codex")}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "-p", "default", "limits", "--json")
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	acc, ok := claudeAccount(decodeLimits(t, stdout), "default")
	require.True(t, ok, stdout)
	require.True(t, acc.Default)
	require.Empty(t, acc.Windows)
	require.Equal(t, "no feed: run agent-deck hooks install", acc.Error)
}

// A default login that a configured slot already owns is listed once, under
// the slot's name, never again as the default account.
func TestLimitsDefaultAccountOwnedBySlotNotDuplicated(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o700))
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n[profiles.work.claude]\nconfig_dir = '"+claudeDir+"'\n")
	t.Setenv("CODEX_HOME", filepath.Join(home, "absent-codex"))
	ingest := runUsageCLI(t, home, remoteLimitsPayload, "-p", "work", "usage", "ingest", "claude")
	require.Equal(t, 0, ingest.exitCode, ingest.stderr)
	result := runUsageCLI(t, home, "", "limits", "--json")
	require.Equal(t, 0, result.exitCode, result.stderr)
	report := decodeLimits(t, result.stdout)
	require.Len(t, report.Accounts, 1, result.stdout)
	require.Equal(t, "work", report.Accounts[0].Name)
	require.False(t, report.Accounts[0].Default)
	require.NotContains(t, result.stdout, `"default"`)
}

// T21-7 "remote default-only host" as the Mac app runs it: the remote has
// only its default ~/.claude login (no slots) fed by its own statusLine
// ingest. Both the forwarded `remote NAME limits --json` and the app's direct
// `ssh host '<agent-deck>' -p default limits --json` return the remote's own
// default account with its 5h/7d, over the real ssh client and an in-process
// ssh server.
func TestRemoteLimitsDefaultOnlyHostT21_7(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "absent-codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	remote, shim := t.TempDir(), t.TempDir()
	startParitySSH(t, remote, shim)
	require.NoError(t, os.MkdirAll(filepath.Join(remote, ".claude"), 0o700))
	writeMacappConfig(t, remote, "[macapp]\nplugins = true\n")
	ingest := runUsageCLI(t, remote, remoteLimitsPayload, "-p", "default", "usage", "ingest", "claude")
	require.Equal(t, 0, ingest.exitCode, ingest.stderr)
	run := remoteLimitsController(t, shim, channelsCLIBinary(t))

	stdout, stderr, code := run("lab", "limits", "--json")
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	requireDefaultWindows(t, stdout)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	direct := exec.CommandContext(ctx, filepath.Join(shim, "ssh"), "test-host", "'"+channelsCLIBinary(t)+"' '-p' 'default' 'limits' '--json'")
	out, err := direct.Output()
	require.NoError(t, err, "direct ssh limits: %s", out)
	requireDefaultWindows(t, string(out))
}
