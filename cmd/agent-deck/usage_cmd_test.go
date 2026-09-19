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

	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usageSentinelToken stands in for the user's Z.ai credential: it is exported
// into every `usage` subprocess so the JSON and human renderings can be checked
// for a leak against something that would only be there by mistake.
const usageSentinelToken = "SENTINEL-USAGE-TOKEN-do-not-leak"

type usageResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// runUsageCLI runs `agent-deck <args...>` in a subprocess with an isolated HOME
// and XDG tree, so a test never reads or writes the developer's real cache.
// ANTHROPIC_BASE_URL is cleared on purpose: `usage` must reach no network in
// the suite, and an unconfigured Z.ai provider is exactly the state that
// guarantees it.
func runUsageCLI(t *testing.T, home string, stdin string, args ...string) usageResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmdArgs := append([]string{"-test.run=^TestUsageHelperProcess$", "--"}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_USAGE_HELPER=1",
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"AGENTDECK_PROFILE=",
		"CLAUDE_CONFIG_DIR=",
		"ANTHROPIC_BASE_URL=",
		"ANTHROPIC_AUTH_TOKEN="+usageSentinelToken,
	)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NoError(t, ctx.Err(), "usage command did not return promptly")

	result := usageResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case asExitError(err, &exitErr):
		result.exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("running usage: %v\nstdout:%s\nstderr:%s", err, result.stdout, result.stderr)
	}
	return result
}

func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}

func TestUsageHelperProcess(t *testing.T) {
	if os.Getenv("AGENT_DECK_USAGE_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	os.Args = append([]string{"agent-deck"}, os.Args[separator+1:]...)
	main()
	// Exit rather than return: letting the test framework resume would append
	// its own "PASS" to stdout, and the passthrough assertions below compare
	// the statusLine output byte for byte.
	os.Exit(0)
}

// usageHelperProfile is the profile the helper subprocess resolves to. This
// package's TestMain forces AGENTDECK_PROFILE=_test (2025-12-11 incident: tests
// overwrote production sessions) and that runs inside the helper too, so the
// cache lands under _test rather than default.
const usageHelperProfile = "_test"

// seedSnapshot writes a provider file straight into the profile's quota cache,
// standing in for an earlier ingest or fetch.
func seedSnapshot(t *testing.T, home, profile string, snapshot quota.Snapshot) {
	t.Helper()
	dir := filepath.Join(home, ".cache", "agent-deck", "quota", profile)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, snapshot.ID+".json"), encoded, 0o644))
}

func freshClaudeSnapshot() quota.Snapshot {
	resets := time.Now().Add(2 * time.Hour).Unix()
	return quota.Snapshot{
		ID:    quota.ProviderClaude,
		Label: "Claude",
		Windows: []quota.Window{
			{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 23.5, ResetsAt: &resets},
			{Kind: quota.WindowSevenDay, Label: "7d", UsedPercentage: 41.2},
		},
		UpdatedAt: time.Now().Unix(),
	}
}

func TestUsageJSONShapeIsStable(t *testing.T) {
	home := t.TempDir()
	seedSnapshot(t, home, usageHelperProfile, freshClaudeSnapshot())

	result := runUsageCLI(t, home, "", "usage", "--json")
	require.Equal(t, 0, result.exitCode, result.stderr)

	var report quota.Report
	require.NoError(t, json.Unmarshal([]byte(result.stdout), &report))
	require.Len(t, report.Providers, 1)

	claude := report.Providers[0]
	assert.Equal(t, "claude", claude.ID)
	assert.Equal(t, "Claude", claude.Label)
	assert.False(t, claude.Stale)
	require.Len(t, claude.Windows, 2)
	assert.Equal(t, quota.WindowFiveHour, claude.Windows[0].Kind)
	assert.Equal(t, "5h", claude.Windows[0].Label)
	assert.Equal(t, 23.5, claude.Windows[0].UsedPercentage)
	assert.NotNil(t, claude.Windows[0].ResetsAt)
	// A window the provider gave no reset for must not acquire one.
	assert.Nil(t, claude.Windows[1].ResetsAt)

	// The field names are the contract for anything scripting against this.
	var generic map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.stdout), &generic))
	providers, ok := generic["providers"].([]any)
	require.True(t, ok, "providers key must be present: %s", result.stdout)
	first, ok := providers[0].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"id", "label", "windows", "updated_at", "stale"} {
		assert.Contains(t, first, key)
	}
}

func TestUsageJSONWithEmptyCacheStillHasProvidersKey(t *testing.T) {
	home := t.TempDir()
	result := runUsageCLI(t, home, "", "usage", "--json")
	require.Equal(t, 0, result.exitCode, result.stderr)

	var generic map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.stdout), &generic))
	providers, ok := generic["providers"]
	require.True(t, ok, "providers key must be present even with nothing cached: %s", result.stdout)
	assert.NotNil(t, providers, "providers must be [] not null so consumers can range over it")
}

func TestUsageOutputCarriesNoCredential(t *testing.T) {
	home := t.TempDir()
	seedSnapshot(t, home, usageHelperProfile, freshClaudeSnapshot())

	for _, args := range [][]string{{"usage"}, {"usage", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			result := runUsageCLI(t, home, "", args...)
			assert.NotContains(t, result.stdout, usageSentinelToken)
			assert.NotContains(t, result.stderr, usageSentinelToken)
		})
	}
}

func TestUsageProviderFailureDoesNotSuppressOthers(t *testing.T) {
	// The isolation requirement: one provider going wrong must never blank out
	// a provider that is working. A corrupt cache entry is the cheapest way to
	// produce a genuine per-provider failure. The id is one this build does not
	// know, which also pins the label fallback: an entry written by a newer
	// build must still be shown, under its own id, rather than disappearing.
	home := t.TempDir()
	seedSnapshot(t, home, usageHelperProfile, freshClaudeSnapshot())
	dir := filepath.Join(home, ".cache", "agent-deck", "quota", usageHelperProfile)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "some-other-provider.json"), []byte("{not json"), 0o644))

	result := runUsageCLI(t, home, "", "usage")
	// A failing provider is data, not a CLI failure: a script that checks the
	// exit status must not see one provider's outage as `usage` being broken.
	require.Equal(t, 0, result.exitCode, result.stderr)
	assert.Contains(t, result.stdout, "some-other-provider")
	assert.Contains(t, result.stdout, "unavailable")
	// ...and Claude still prints its numbers.
	assert.Contains(t, result.stdout, "Claude")
	assert.Contains(t, result.stdout, "23.5%")
}

func TestUsageMarksStaleSnapshot(t *testing.T) {
	home := t.TempDir()
	stale := freshClaudeSnapshot()
	stale.UpdatedAt = time.Now().Add(-24 * time.Hour).Unix()
	seedSnapshot(t, home, usageHelperProfile, stale)

	result := runUsageCLI(t, home, "", "usage")
	require.Equal(t, 0, result.exitCode, result.stderr)
	// Shown, but never presented as current.
	assert.Contains(t, result.stdout, "23.5%")
	assert.Contains(t, result.stdout, "stale")
}

func TestUsageReadsTheSelectedProfilesCache(t *testing.T) {
	home := t.TempDir()
	work := freshClaudeSnapshot()
	work.Windows[0].UsedPercentage = 77.7
	seedSnapshot(t, home, "work", work)

	inWork := runUsageCLI(t, home, "", "-p", "work", "usage")
	require.Equal(t, 0, inWork.exitCode, inWork.stderr)
	assert.Contains(t, inWork.stdout, "77.7%")

	inDefault := runUsageCLI(t, home, "", "-p", "default", "usage")
	require.Equal(t, 0, inDefault.exitCode, inDefault.stderr)
	assert.NotContains(t, inDefault.stdout, "77.7%")
}

const usageStatusLinePayload = `{"session_id":"abc","cwd":"/home/someone/private",` +
	`"rate_limits":{"five_hour":{"used_percentage":31.5,"resets_at":1738425600}}}`

func TestUsageIngestClaudeWritesCacheAndPrintsNothing(t *testing.T) {
	home := t.TempDir()

	result := runUsageCLI(t, home, usageStatusLinePayload, "usage", "ingest", "claude")
	require.Equal(t, 0, result.exitCode, result.stderr)
	// Without a wrapped command there was no status line before, so emitting
	// anything would put text on the user's status bar that they did not ask for.
	assert.Empty(t, result.stdout)

	shown := runUsageCLI(t, home, "", "usage")
	require.Equal(t, 0, shown.exitCode, shown.stderr)
	assert.Contains(t, shown.stdout, "31.5%")
}

func TestUsageIngestClaudePassesPayloadThroughToWrappedCommand(t *testing.T) {
	// Claude Code treats empty stdout or a non-zero exit from a statusLine
	// command as "blank status line". An ingester that swallowed either would
	// silently destroy a status line the user already had, so the wrapped
	// command gets the same bytes and its stdout and exit status are forwarded.
	home := t.TempDir()

	result := runUsageCLI(t, home, usageStatusLinePayload,
		"usage", "ingest", "claude", "--", "cat")
	require.Equal(t, 0, result.exitCode, result.stderr)
	assert.Equal(t, usageStatusLinePayload, result.stdout)

	shown := runUsageCLI(t, home, "", "usage")
	assert.Contains(t, shown.stdout, "31.5%")
}

func TestUsageIngestClaudeForwardsWrappedExitStatus(t *testing.T) {
	home := t.TempDir()

	result := runUsageCLI(t, home, usageStatusLinePayload,
		"usage", "ingest", "claude", "--", "sh", "-c", "printf custom-line; exit 3")
	assert.Equal(t, 3, result.exitCode)
	assert.Equal(t, "custom-line", result.stdout)
}

func TestUsageIngestClaudeStoresNothingButRateLimits(t *testing.T) {
	home := t.TempDir()
	require.Equal(t, 0, runUsageCLI(t, home, usageStatusLinePayload, "usage", "ingest", "claude").exitCode)

	cached, err := os.ReadFile(filepath.Join(home, ".cache", "agent-deck", "quota", usageHelperProfile, "claude.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(cached), "/home/someone/private")
	assert.NotContains(t, string(cached), "session_id")
}

func TestUsageIngestClaudeWithoutRateLimitsIsNotAnError(t *testing.T) {
	// The normal state before the session's first API response.
	home := t.TempDir()
	result := runUsageCLI(t, home, `{"session_id":"abc"}`, "usage", "ingest", "claude", "--", "echo", "my-status")
	require.Equal(t, 0, result.exitCode, result.stderr)
	assert.Equal(t, "my-status\n", result.stdout)
}

func TestUsageHelpIsUsageOnly(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"usage", "--help"},
		{"usage", "-h"},
		{"usage", "help"},
		{"usage", "ingest", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			result := runUsageCLI(t, home, "", args...)
			require.Equal(t, 0, result.exitCode, result.stderr)
			assert.Contains(t, result.stdout, "Usage: agent-deck usage")
		})
	}
}

func TestUsageIsRegisteredAndDispatched(t *testing.T) {
	// extractProfileFlag stops honoring the global -p at the first registry
	// token, so registration is what makes `agent-deck -p work usage` parse.
	assert.True(t, commandRegistry["usage"])
}
