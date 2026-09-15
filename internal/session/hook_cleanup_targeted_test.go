package session

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHookCleanupDeleteDoesNotSweepUnrelatedOrphans(t *testing.T) {
	s := hookCleanupStorage(t)
	root := GetHooksDir()
	require.NoError(t, os.MkdirAll(root, 0700))
	for _, name := range []string{"gone.json", "unrelated.json"} {
		path := filepath.Join(root, name)
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
		old := time.Now().Add(-48 * time.Hour)
		require.NoError(t, os.Chtimes(path, old, old))
	}
	require.NoError(t, s.DeleteInstance("gone"))
	require.NoFileExists(t, filepath.Join(root, "gone.json"))
	require.FileExists(t, filepath.Join(root, "unrelated.json"), "targeted deletion must not sweep unrelated orphans")
}
