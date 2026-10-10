//go:build fswatchpoll

package fswatch

// The fswatchpoll build tag gives every platform the polling watcher kqueue
// platforms use, so Linux tests can run the macOS path end to end.
func init() { forcePolling = true }
