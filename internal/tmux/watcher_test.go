package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLogWatcher(t *testing.T) {
	// Create temp log directory
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "test_session.log")

	// Track events
	events := make(chan string, 10)

	// Create watcher
	watcher, err := NewLogWatcher(logDir, func(sessionName string) {
		events <- sessionName
	})
	assert.NoError(t, err)
	defer watcher.Close()

	// Start watching
	go watcher.Start()
	time.Sleep(100 * time.Millisecond)

	// Create and write to log file
	f, err := os.Create(logFile)
	assert.NoError(t, err)
	_, _ = f.WriteString("test output\n")
	f.Close()

	// Wait for event
	select {
	case name := <-events:
		assert.Equal(t, "test_session", name)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for file event")
	}
}

func TestLogWatcher_RateLimiting(t *testing.T) {
	// Create temp log directory
	logDir := t.TempDir()
	logFile := filepath.Join(logDir, "burst_session.log")

	// Track events
	eventCount := 0
	events := make(chan bool, 100)

	// Create watcher
	watcher, err := NewLogWatcher(logDir, func(sessionName string) {
		eventCount++
		events <- true
	})
	assert.NoError(t, err)
	defer watcher.Close()

	// Start watching
	go watcher.Start()
	time.Sleep(100 * time.Millisecond)

	// Simulate high-frequency log writes
	// Write 10 times rapidly
	for i := 0; i < 10; i++ {
		err := os.WriteFile(logFile, []byte(fmt.Sprintf("log line %d\n", i)), 0644)
		assert.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
	}

	// Give it a moment to process
	time.Sleep(200 * time.Millisecond)

	// Drain events
	finalCount := 0
Loop:
	for {
		select {
		case <-events:
			finalCount++
		default:
			break Loop
		}
	}

	// We wrote 10 times over 50ms.
	// With 20 events/sec (50ms interval), we should see at most 2 events (first one immediate, second one potentially if timing overlaps)
	// Definitely should be much less than 10.
	assert.LessOrEqual(t, finalCount, 2, "Expected events to be rate limited/coalesced")
}
