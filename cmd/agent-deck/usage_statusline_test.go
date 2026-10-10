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

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

const statuslineFixture = `{"session_id":"claude-native","model":{"id":"claude-opus-4-6","display_name":"Opus 4.6","secret":"DO-NOT-STORE"},"cwd":"/project","context_window":{"used_percentage":37.5,"context_window_size":200000,"total_input_tokens":75000,"total_output_tokens":1234,"prompt":"DO-NOT-STORE"},"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1790860000},"seven_day":{"used_percentage":48,"resets_at":1791400000},"spend_limit":{"used_percentage":101}},"prompt":"DO-NOT-STORE","transcript_path":"DO-NOT-STORE"}`

func TestUsageStatuslineRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	// Match the subprocess fixture's XDG roots.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_EVENTS_BUS", "1")
	const profile = "status-test"
	storage, err := session.NewStorageWithProfile(profile)
	require.NoError(t, err)
	defer storage.Close()
	require.NoError(t, storage.GetDB().SaveInstance(&statedb.InstanceRow{ID: "deck-id", Title: "My Claude", ProjectPath: "/project", Tool: "claude", CreatedAt: time.Now(), ToolData: json.RawMessage(`{"claude_session_id":"claude-native"}`)}))
	bus := events.OpenProfile(profile)
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sub, err := bus.Subscribe(ctx, 0)
	require.NoError(t, err)
	result := runUsageCLI(t, home, statuslineFixture, "-p", profile, "usage", "ingest", "claude")
	require.Equal(t, 0, result.exitCode, result.stderr)
	require.Empty(t, result.stdout)
	require.NotContains(t, result.stderr, "agent-deck:")
	var frame events.Frame
	select {
	case frame = <-sub.Frames():
	case <-ctx.Done():
		t.Fatal("no usage.statusline frame")
	}
	require.Equal(t, "usage.statusline", frame.Kind)
	require.Equal(t, "deck-id", frame.SessionID)
	require.NotContains(t, string(frame.Data), "DO-NOT-STORE")
	require.NotContains(t, string(frame.Data), "spend_limit")
	var record statedb.ClaudeStatusline
	require.NoError(t, json.Unmarshal(frame.Data, &record))
	require.Equal(t, "Opus 4.6", record.Model.DisplayName)
	require.Equal(t, 37.5, *record.ContextWindow.UsedPercentage)
	require.Equal(t, int64(200000), *record.ContextWindow.ContextWindowSize)
	require.Equal(t, int64(75000), *record.ContextWindow.TotalInputTokens)
	require.Equal(t, int64(1234), *record.ContextWindow.TotalOutputTokens)
	require.Equal(t, "/project", record.Cwd)
	require.Equal(t, 48.0, *record.RateLimits.SevenDay.UsedPercentage)
	require.Equal(t, int64(1790860000), *record.RateLimits.FiveHour.ResetsAt)
	_, err = time.Parse("2006-01-02T15:04:05.000Z", record.CapturedAt)
	require.NoError(t, err)
	for _, target := range []string{"deck-id", "My Claude"} {
		result = runUsageCLI(t, home, "", "-p", profile, "usage", "statusline", "--session", target, "--json")
		require.Equal(t, 0, result.exitCode, result.stderr)
		require.JSONEq(t, string(frame.Data), result.stdout)
	}
	counts, err := bus.KindCounts()
	require.NoError(t, err)
	require.Equal(t, uint64(1), counts["usage.statusline"])
	// The existing quota parser remains authoritative, including spend_limit.
	store, err := quota.NewStore(profile)
	require.NoError(t, err)
	snapshots, err := store.Load()
	require.NoError(t, err)
	expected, _, err := quota.ParseStatusLine(strings.NewReader(statuslineFixture))
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	require.Equal(t, expected.Windows, snapshots[0].Windows)
	// Unknown IDs still push an unlinked frame and preserve rate-limit caching.
	result = runUsageCLI(t, home, strings.ReplaceAll(statuslineFixture, "claude-native", "unknown-native"), "-p", profile, "usage", "ingest", "claude")
	require.Equal(t, 0, result.exitCode, result.stderr)
	select {
	case frame = <-sub.Frames():
	case <-ctx.Done():
		t.Fatal("no unknown-ID frame")
	}
	require.Empty(t, frame.SessionID)
	require.Contains(t, string(frame.Data), "unknown-native")
	// An account feed must publish to the owning session's profile, while
	// its quota cache stays under the account name.
	result = runUsageCLI(t, home, statuslineFixture, "-p", "account-feed", "usage", "ingest", "claude")
	require.Equal(t, 0, result.exitCode, result.stderr)
	select {
	case frame = <-sub.Frames():
	case <-ctx.Done():
		t.Fatal("no cross-profile frame")
	}
	require.Equal(t, "deck-id", frame.SessionID)
	accountStore, err := quota.NewStore("account-feed")
	require.NoError(t, err)
	accountSnapshots, err := accountStore.Load()
	require.NoError(t, err)
	require.Len(t, accountSnapshots, 1)
	require.Equal(t, expected.Windows, accountSnapshots[0].Windows)
	// A later payload without quota still refreshes model/context.
	result = runUsageCLI(t, home, `{"session_id":"claude-native","model":{"id":"next","display_name":"Next"}}`, "-p", profile, "usage", "ingest", "claude")
	require.Equal(t, 0, result.exitCode, result.stderr)
	result = runUsageCLI(t, home, "", "-p", profile, "usage", "statusline", "--session", "deck-id", "--json")
	require.Contains(t, result.stdout, `"display_name":"Next"`)
	snapshots, err = store.Load()
	require.NoError(t, err)
	require.Equal(t, expected.Windows, snapshots[0].Windows)
	require.NoError(t, storage.DeleteInstance("deck-id"))
	result = runUsageCLI(t, home, "", "-p", profile, "usage", "statusline", "--session", "deck-id", "--json")
	require.Equal(t, 1, result.exitCode)
	require.JSONEq(t, `{"error":"no statusline record"}`, result.stdout)
	var n int
	require.NoError(t, storage.GetDB().DB().QueryRow(`SELECT count(*) FROM claude_statuslines WHERE claude_session_id = 'claude-native'`).Scan(&n))
	require.Zero(t, n)
	// No private fixture text may reach SQLite or its WAL.
	files, err := filepath.Glob(storage.Path() + "*")
	require.NoError(t, err)
	for _, path := range files {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NotContains(t, string(data), "DO-NOT-STORE")
	}
}

func TestUsageStatuslineMissingRecord(t *testing.T) {
	result := runUsageCLI(t, t.TempDir(), "", "usage", "statusline", "--session", "missing", "--json")
	require.Equal(t, 1, result.exitCode)
	require.JSONEq(t, `{"error":"no statusline record"}`, result.stdout)
	// The app follows "usage." alongside its other kinds.
	require.True(t, eventMatches(events.Frame{Kind: "usage.statusline", SessionID: "deck-id"}, []string{"usage."}, "deck-id"))
}

// Pin the existing public limits shape after the richer ingest. Only its
// wall-clock capture timestamp is normalized, not the quota windows or source.
func TestUsageStatuslineLimitsGolden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, "absent-codex"))
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n[profiles.work.claude]\nconfig_dir = '"+filepath.Join(home, "claude-work")+"'\n")
	result := runUsageCLI(t, home, statuslineFixture, "-p", "work", "usage", "ingest", "claude")
	require.Equal(t, 0, result.exitCode, result.stderr)
	result = runUsageCLI(t, home, "", "limits", "--json")
	require.Equal(t, 0, result.exitCode, result.stderr)
	var report struct {
		Accounts []limitAccountJSON `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.stdout), &report))
	require.Len(t, report.Accounts, 1)
	require.NotEmpty(t, report.Accounts[0].UpdatedAt)
	report.Accounts[0].UpdatedAt = "CAPTURED_AT"
	normalized, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	golden, err := os.ReadFile("testdata/goldens/statusline_limits_json.golden")
	require.NoError(t, err)
	require.Equal(t, string(golden), string(normalized)+"\n")
}

func TestUsageStatuslineWrapDefaultAndPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		wrapped           []string
		code              int
	}{
		{name: "default", input: statuslineFixture, want: "Opus 4.6 | Context 37.5% | 5h 23.5% | 7d 48.0%\n"},
		{name: "missing fields", input: `{"session_id":"native"}`, want: "Claude\n"},
		{name: "invalid payload", input: "{invalid", want: "Claude\n"},
		{name: "child help", input: statuslineFixture, want: "--help", wrapped: []string{"--", "sh", "-c", `printf "%s" "$1"`, "child", "--help"}},
		{name: "passthrough", input: statuslineFixture, want: statuslineFixture, wrapped: []string{"--", "cat"}},
		{name: "exit status", input: statuslineFixture, want: "my-status", wrapped: []string{"--", "sh", "-c", "printf my-status; exit 3"}, code: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runUsageCLI(t, t.TempDir(), tc.input, append([]string{"usage", "statusline-wrap"}, tc.wrapped...)...)
			require.Equal(t, tc.code, result.exitCode, result.stderr)
			require.Equal(t, tc.want, result.stdout)
		})
	}
}

func TestUsageStatuslineHooksDefaultInstallRoundTrip(t *testing.T) {
	for _, original := range []string{"", `cat; printf '\nCUSTOM:%s' "$HOME"`, `printf '%s' "$(cat)"`} {
		t.Run(original, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".claude")
			binDir := filepath.Join(home, ".local", "bin")
			require.NoError(t, os.MkdirAll(dir, 0700))
			require.NoError(t, os.MkdirAll(binDir, 0700))
			require.NoError(t, os.Symlink(channelsCLIBinary(t), filepath.Join(binDir, "agent-deck")))
			settings := map[string]any{"model": "opus"}
			if original != "" {
				settings["statusLine"] = map[string]any{"command": original, "padding": 0}
			}
			data, err := json.Marshal(settings)
			require.NoError(t, err)
			path := filepath.Join(dir, "settings.json")
			require.NoError(t, os.WriteFile(path, data, 0600))
			// The record needs an existing profile store; ingest never creates one.
			_, stderr, code := runAgentDeck(t, home, "profile", "create", "default")
			require.Equal(t, 0, code, stderr)
			_, stderr, code = runAgentDeck(t, home, "hooks", "install")
			require.Equal(t, 0, code, stderr)
			installed, err := os.ReadFile(path)
			require.NoError(t, err)
			var root map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(installed, &root))
			var line struct {
				Command string `json:"command"`
			}
			require.NoError(t, json.Unmarshal(root["statusLine"], &line))
			require.Contains(t, line.Command, "usage statusline-wrap")
			require.Contains(t, line.Command, "-p default")
			_, stderr, code = runAgentDeck(t, home, "hooks", "install")
			require.Equal(t, 0, code, stderr)
			again, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, string(installed), string(again), "install must be idempotent")
			runLine := func(command string) string {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "sh", "-c", command)
				cmd.Env = agentDeckTestEnv(home, []string{"PATH=" + binDir + ":" + os.Getenv("PATH")})
				cmd.Stdin = strings.NewReader(statuslineFixture)
				output, err := cmd.Output()
				require.NoError(t, err)
				return string(output)
			}
			expected := "Opus 4.6 | Context 37.5% | 5h 23.5% | 7d 48.0%\n"
			if original != "" {
				expected = runLine(original)
			}
			require.Equal(t, expected, runLine(line.Command))
			stats, stderr, code := runAgentDeck(t, home, "-p", "default", "events", "stats", "--json")
			require.Equal(t, 0, code, stderr)
			require.Contains(t, stats, `"usage.statusline": 1`)
			_, stderr, code = runAgentDeck(t, home, "hooks", "uninstall")
			require.Equal(t, 0, code, stderr)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			var restored map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(after, &restored))
			if original == "" {
				require.NotContains(t, restored, "statusLine")
			} else {
				want, _ := json.Marshal(settings["statusLine"])
				require.JSONEq(t, string(want), string(restored["statusLine"]))
			}
			require.Equal(t, `"opus"`, string(restored["model"]))
		})
	}
}

// profileStores lists every profile directory under the isolated home's data
// roots, so a test can assert that no store was materialised.
func profileStores(home string) []string {
	var out []string
	for _, root := range []string{
		filepath.Join(home, ".agent-deck", "profiles"),
		filepath.Join(home, ".local", "share", "agent-deck", "profiles"),
		filepath.Join(home, ".config", "agent-deck", "profiles"),
	} {
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			out = append(out, filepath.Join(root, entry.Name()))
		}
	}
	return out
}

// An account slot name is a Claude account, not consent to create an
// agent-deck profile (#1790): ingest for a session no profile owns keeps the
// quota cache and creates no profile store.
func TestUsageStatuslineSlotIngestCreatesNoProfileStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, "absent-codex"))
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n[profiles.work.claude]\nconfig_dir = '"+filepath.Join(home, "claude-work")+"'\n")
	result := runUsageCLI(t, home, statuslineFixture, "-p", "work", "usage", "ingest", "claude")
	require.Equal(t, 0, result.exitCode, result.stderr)
	require.Empty(t, result.stdout)
	require.Empty(t, profileStores(home), "ingest must not create a profile store")
	result = runUsageCLI(t, home, "", "limits", "--json")
	require.Equal(t, 0, result.exitCode, result.stderr)
	require.Contains(t, result.stdout, `"work"`, "the quota cache is still written")
}

// hooks install pins ~/.claude to a collision slot name when a named slot
// already uses "default"; running that wrapper for an unowned Claude session
// must not create a store for the slot name.
func TestUsageStatuslineCollisionSlotCreatesNoProfileStore(t *testing.T) {
	home := t.TempDir()
	alt := filepath.Join(home, "claude-alt")
	dir := filepath.Join(home, ".claude")
	binDir := filepath.Join(home, ".local", "bin")
	for _, d := range []string{alt, dir, binDir} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	require.NoError(t, os.Symlink(channelsCLIBinary(t), filepath.Join(binDir, "agent-deck")))
	writeMacappConfig(t, home, "[profiles.default.claude]\nconfig_dir = '"+alt+"'\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{}`), 0o600))
	_, stderr, code := runAgentDeck(t, home, "hooks", "install")
	require.Equal(t, 0, code, stderr)
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	require.NoError(t, err)
	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &root))
	var line struct{ Command string }
	require.NoError(t, json.Unmarshal(root["statusLine"], &line))
	require.Contains(t, line.Command, "default-claude-1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", line.Command)
	cmd.Env = agentDeckTestEnv(home, []string{"PATH=" + binDir + ":" + os.Getenv("PATH")})
	cmd.Stdin = strings.NewReader(`{"session_id":"plain-claude-run","model":{"display_name":"X"}}`)
	output, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "X\n", string(output))
	require.Empty(t, profileStores(home), "wrapper must not create a profile store")
}

// With default_profile = personal and no "default" store, the active
// config's `-p default` wrapper stores an unowned record in the existing
// configured default profile, never in a new "default" store.
func TestUsageStatuslineUnownedFallsBackToConfiguredDefault(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{{"profile", "create", "personal"}, {"profile", "default", "personal"}} {
		_, stderr, code := runAgentDeck(t, home, args...)
		require.Equal(t, 0, code, stderr)
	}
	before := profileStores(home)
	require.Len(t, before, 1)
	result := runUsageCLI(t, home, `{"session_id":"plain-claude-run","model":{"display_name":"X"}}`, "-p", "default", "usage", "statusline-wrap")
	require.Equal(t, 0, result.exitCode, result.stderr)
	require.Equal(t, "X\n", result.stdout)
	require.Equal(t, before, profileStores(home), "no default store may be created")
	stats, stderr, code := runAgentDeck(t, home, "-p", "personal", "events", "stats", "--json")
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stats, `"usage.statusline": 1`)
}

// With the opt-out set, hooks status reports the feed as disabled instead of
// asking for hooks install.
func TestUsageStatuslineOptOutStatus(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	writeMacappConfig(t, home, "[claude]\nstatusline_feed = false\n")
	stdout, stderr, code := runAgentDeck(t, home, "hooks", "status")
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "disabled by [claude] statusline_feed = false")
	require.NotContains(t, stdout, "to wire the usage feed")
}
