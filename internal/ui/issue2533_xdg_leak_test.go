package ui

import (
	"os"
	"testing"
)

// Issue #2533: TestSaveRemoteSessionsCache_ResetsAgeForLiveFetchedOnly set
// HOME and XDG_CONFIG/DATA/CACHE_HOME with os.Setenv but restored only HOME,
// so every later test in this package resolved config, data and cache under
// the removed TempDir instead of the sandbox HOME that TestMain's
// testutil.IsolateHome set up (it leaves XDG_* unset on purpose). Run the test
// as a subtest and require the process env to be exactly as it was before.
func TestIssue2533_RemoteStaleAgeTestRestoresHomeEnv(t *testing.T) {
	keys := []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"}
	type entry struct {
		val string
		set bool
	}
	before := make(map[string]entry, len(keys))
	for _, k := range keys {
		v, ok := os.LookupEnv(k)
		before[k] = entry{v, ok}
	}

	if !t.Run("issue2331", TestSaveRemoteSessionsCache_ResetsAgeForLiveFetchedOnly) {
		t.Fatal("wrapped test failed; env check below would be meaningless")
	}

	for _, k := range keys {
		v, ok := os.LookupEnv(k)
		if got := (entry{v, ok}); got != before[k] {
			t.Errorf("%s leaked: before set=%v %q, after set=%v %q", k, before[k].set, before[k].val, ok, v)
		}
	}
}
