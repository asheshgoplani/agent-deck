package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

func TestAccountReviewRegistrationRejectsBeforePersistence(t *testing.T) {
	for _, command := range []string{"add", "launch"} {
		for _, account := range []string{"missing", "incompatible"} {
			t.Run(command+"/"+account, func(t *testing.T) {
				home := t.TempDir()
				configDir := filepath.Join(home, ".config", "agent-deck")
				require.NoError(t, os.MkdirAll(configDir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[profiles.incompatible]\n"), 0600))
				args := []string{command, home, "--title", "invalid-account", "--no-parent", "--account", account, "-c", "claude", "--json"}
				if command == "launch" {
					args = append(args, "-m", "hello")
				}
				stdout, stderr, code := runAgentDeck(t, home, args...)
				require.NotZero(t, code, "%s %s", stdout, stderr)
				require.Contains(t, stdout+stderr, "account")
				db, err := statedb.Open(filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "state.db"))
				require.NoError(t, err)
				defer db.Close()
				rows, err := db.LoadInstances()
				require.NoError(t, err)
				require.Empty(t, rows, "account failure must not leave a registered session")
			})
		}
	}
}
