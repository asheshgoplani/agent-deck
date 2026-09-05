package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

// Exercise the public CLI with persisted metadata, including legacy slots that
// need not resolve to a configured account. No provider is launched.
func TestCLIStoredAccountVisibility(t *testing.T) {
	home := t.TempDir()
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, channelsCLIBinary(t), args...)
		for _, kv := range cliEnvForIssue1031(home) {
			if !strings.HasPrefix(kv, "XDG_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
			"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
			"XDG_CACHE_HOME="+filepath.Join(home, ".cache"), "AGENTDECK_ACCOUNT=fake-env-slot")
		started := time.Now()
		out, err := cmd.CombinedOutput()
		t.Logf("CLI %v elapsed=%s", args, time.Since(started))
		require.NoError(t, err, "%v: %s", args, out)
		return string(out)
	}
	slots := []string{"", "personal", "work", "legacy-unknown", "  \u65e5\u672c e\u0301 \U0001f680  ", "quote\"slash\\", "\x1b[31m\a\r\n\u0085\u202e", `literal\n`}
	profiles := []string{"ch_support_test", "other-slots"}
	type fixture struct{ id, profile, account string }
	var fixtures []fixture
	for p, profile := range profiles {
		run(t, "-p", profile, "list", "--json")
		db, err := statedb.Open(filepath.Join(home, ".local", "share", "agent-deck", "profiles", profile, "state.db"))
		require.NoError(t, err)
		for i, account := range slots {
			id := fmt.Sprintf("slot%d%03d", p, i)
			require.NoError(t, db.SaveInstance(&statedb.InstanceRow{ID: id, Title: "same-title", ProjectPath: home, Tool: "shell", Status: "idle", Account: account, CreatedAt: time.Now()}))
			fixtures = append(fixtures, fixture{id, profile, account})
		}
		require.NoError(t, db.Close())
	}
	checkJSON := func(t *testing.T, row map[string]any, f fixture) {
		t.Helper()
		value, present := row["account"]
		require.True(t, present, "%s: missing account key (empty must be explicit)", f.id)
		require.IsType(t, "", value)
		require.Equal(t, f.account, value, "%s raw stored account", f.id)
		require.Equal(t, f.profile, row["profile"])
	}
	for _, all := range []bool{false, true} {
		name := "single"
		if all {
			name = "all"
		}
		t.Run(name+"_json", func(t *testing.T) {
			for _, profile := range profiles {
				args := []string{"-p", profile, "list", "--json"}
				if all {
					args = append(args, "--all")
				}
				var rows []map[string]any
				require.NoError(t, json.Unmarshal([]byte(run(t, args...)), &rows))
				byID := map[string]map[string]any{}
				for _, row := range rows {
					byID[row["id"].(string)] = row
				}
				count := 0
				for _, f := range fixtures {
					if all || f.profile == profile {
						count++
						checkJSON(t, byID[f.id], f)
					}
				}
				require.Len(t, rows, count)
				if all {
					break
				}
			}
		})
		t.Run(name+"_human", func(t *testing.T) {
			for _, profile := range profiles {
				args := []string{"-p", profile, "list"}
				if all {
					args = append(args, "--all")
				}
				out := run(t, args...)
				require.Contains(t, out, "ACCOUNT")
				for _, f := range fixtures {
					if !all && f.profile != profile {
						continue
					}
					matched := 0
					for _, line := range strings.Split(out, "\n") {
						if strings.Contains(line, f.id) {
							matched++
							require.True(t, strings.HasSuffix(line, " "+strconv.Quote(f.account)), "unsafe or wrong account row: %q", line)
						}
					}
					require.Equal(t, 1, matched, "one logical row for %s", f.id)
				}
				require.NotContains(t, out, "\x1b[31m")
				require.NotContains(t, out, "\a")
				require.NotContains(t, out, "\r")
				if all {
					break
				}
			}
		})
	}
	t.Run("show_json", func(t *testing.T) {
		for _, f := range fixtures {
			var row map[string]any
			require.NoError(t, json.Unmarshal([]byte(run(t, "-p", f.profile, "session", "show", f.id, "--json")), &row))
			checkJSON(t, row, f)
		}
	})
	t.Run("show_human", func(t *testing.T) {
		for _, f := range fixtures {
			out := run(t, "-p", f.profile, "session", "show", f.id)
			found := 0
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "Account:") {
					found++
					require.Equal(t, strconv.Quote(f.account), strings.TrimSpace(strings.TrimPrefix(line, "Account:")))
				}
			}
			require.Equal(t, 1, found, "missing Account line: %s", out)
		}
	})
	t.Run("metadata_retained", func(t *testing.T) {
		for _, profile := range profiles {
			db, err := statedb.Open(filepath.Join(home, ".local", "share", "agent-deck", "profiles", profile, "state.db"))
			require.NoError(t, err)
			rows, err := db.LoadInstances()
			require.NoError(t, err)
			require.NoError(t, db.Close())
			got := map[string]string{}
			for _, row := range rows {
				got[row.ID] = row.Account
			}
			require.Len(t, got, len(slots))
			for _, f := range fixtures {
				if f.profile == profile {
					require.Equal(t, f.account, got[f.id])
				}
			}
		}
	})
}
