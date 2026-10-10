package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrackedCommandExitReceiptClearsStaleCode(t *testing.T) {
	i := NewInstance("receipt-exit-three", t.TempDir())
	i.Tool, i.Command, i.TrackCommandExit = "shell", "exit 3", true
	path := commandExitReceiptPath(i.ID)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("7\n"), 0o600))

	wrapped, err := i.wrapTrackedCommandExit(i.Command)
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "prior spawn's code must be cleared before launch")

	err = exec.Command("bash", "-c", wrapped).Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 3, exit.ExitCode())
	code, known := i.trackedCommandExitReceipt()
	require.True(t, known)
	require.Equal(t, 3, code)

	require.NoError(t, os.WriteFile(path, []byte("invalid\n"), 0o600))
	_, known = i.trackedCommandExitReceipt()
	require.False(t, known)
}
