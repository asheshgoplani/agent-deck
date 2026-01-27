package tmux

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimiter(t *testing.T) {
	// 10 events per second (1 every 100ms)
	rl := NewRateLimiter(10)

	// 1. First event should be allowed
	assert.True(t, rl.Allow())

	// 2. Immediate second event should be denied
	assert.False(t, rl.Allow())

	// 3. Wait 150ms (more than the 100ms interval)
	time.Sleep(150 * time.Millisecond)
	assert.True(t, rl.Allow())
	
	// 4. Denied again immediately after
	assert.False(t, rl.Allow())
}

func TestRateLimiter_Coalesce(t *testing.T) {
	// 2 events per second (1 every 500ms)
	rl := NewRateLimiter(2)
	
	count := 0
	callback := func() {
		count++
	}

	// First call should execute immediately
	rl.Coalesce(callback)
	assert.Equal(t, 1, count)

	// Second call within 500ms window should NOT execute
	rl.Coalesce(callback)
	assert.Equal(t, 1, count)

	// Wait for cooldown
	time.Sleep(600 * time.Millisecond)
	
	// Now it should execute
	rl.Coalesce(callback)
	assert.Equal(t, 2, count)
}
