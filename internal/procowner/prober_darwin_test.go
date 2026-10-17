//go:build darwin

package procowner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDarwinProber_CommandsSeesThisProcess(t *testing.T) {
	table, err := DarwinProber{}.Commands()
	require.NoError(t, err)
	self, ok := table[os.Getpid()]
	require.True(t, ok, "the snapshot must include the test binary")
	assert.Equal(t, os.Getppid(), self.PPID)
	require.NotEmpty(t, self.Args)
	assert.Equal(t, filepath.Base(os.Args[0]), filepath.Base(self.Args[0]))
}
