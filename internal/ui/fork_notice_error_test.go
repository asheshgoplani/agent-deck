package ui

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// noticeError must fold a non-fatal fork degradation notice into any pre-existing
// error so the notice never silently masks a real failure. PR #1299 review
// (CodeRabbit home.go:4327): forceSaveInstances() can setError on a failed
// persist, and an unconditional notice write would overwrite it — making a fork
// that wasn't actually saved look successful until the next reload.
func TestNoticeError_DoesNotMaskExistingError(t *testing.T) {
	assert.NoError(t, noticeError(nil, ""), "no error + no notice -> nil")
	assert.EqualError(t, noticeError(nil, "forked without Docker"), "forked without Docker",
		"notice-only surfaces the notice")
	assert.EqualError(t, noticeError(errors.New("save failed"), ""), "save failed",
		"empty notice leaves the existing error intact")
	assert.EqualError(t, noticeError(errors.New("save failed"), "forked without Docker"),
		"save failed; forked without Docker",
		"a notice must be appended to, not overwrite, an existing error")
}
