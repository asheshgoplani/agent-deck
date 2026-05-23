package childenv

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Issue #1163: the filter must strip every TELEGRAM_* var and any inherited
// CLAUDE_CONFIG_DIR, pin the child's own config dir, and pass everything else
// through untouched.
func TestFilterEnv_StripsTelegramAndCCD(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"CLAUDE_CONFIG_DIR=/parent/scratch",
		"TELEGRAM_STATE_DIR=/parent/tg",
		"TELEGRAM_BOT_TOKEN=secret",
		"HOME=/home/u",
	}

	out := FilterEnv(in, "/child/scratch")

	joined := strings.Join(out, "\n")
	assert.NotContains(t, joined, "TELEGRAM_")
	assert.NotContains(t, out, "CLAUDE_CONFIG_DIR=/parent/scratch")
	assert.Contains(t, out, "CLAUDE_CONFIG_DIR=/child/scratch")
	assert.Contains(t, out, "PATH=/usr/bin")
	assert.Contains(t, out, "HOME=/home/u")
}

// Empty childConfigDir drops the inherited CCD without re-adding one.
func TestFilterEnv_EmptyChildDir(t *testing.T) {
	out := FilterEnv([]string{"CLAUDE_CONFIG_DIR=/parent", "PATH=/bin"}, "")
	for _, kv := range out {
		assert.False(t, strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR="))
	}
	assert.Contains(t, out, "PATH=/bin")
}

// The input slice must not be mutated (callers reuse os.Environ()).
func TestFilterEnv_DoesNotMutateInput(t *testing.T) {
	in := []string{"TELEGRAM_X=1", "PATH=/bin"}
	_ = FilterEnv(in, "/child")
	assert.Equal(t, []string{"TELEGRAM_X=1", "PATH=/bin"}, in)
}
