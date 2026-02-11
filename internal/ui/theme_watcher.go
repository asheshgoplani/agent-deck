package ui

import (
	"context"
	"log/slog"
	"sync"

	dark "github.com/thiagokokada/dark-mode-go"
)

// ThemeWatcher monitors OS dark mode changes and signals the UI to update.
// Follows the same pattern as StorageWatcher: goroutine + buffered channel + Close().
type ThemeWatcher struct {
	changeCh  chan bool     // true=dark, false=light (buffered, non-blocking send)
	closeCh   chan struct{} // signals the watch goroutine to stop
	closeOnce sync.Once
}

// NewThemeWatcher creates and starts a theme watcher.
// Returns nil if WatchDarkMode fails (caller should fall back gracefully).
func NewThemeWatcher(parentCtx context.Context) *ThemeWatcher {
	ctx, cancel := context.WithCancel(parentCtx)

	events, errs, err := dark.WatchDarkMode(ctx)
	if err != nil {
		cancel()
		uiLog.Warn("theme_watcher_init_failed", slog.String("error", err.Error()))
		return nil
	}

	tw := &ThemeWatcher{
		changeCh: make(chan bool, 1),
		closeCh:  make(chan struct{}),
	}

	go tw.watchLoop(ctx, cancel, events, errs)
	return tw
}

func (tw *ThemeWatcher) watchLoop(ctx context.Context, cancel context.CancelFunc, events <-chan bool, errs <-chan error) {
	defer cancel()
	for {
		select {
		case <-tw.closeCh:
			return
		case isDark, ok := <-events:
			if !ok {
				return
			}
			// Non-blocking send (drop if consumer hasn't read yet)
			select {
			case tw.changeCh <- isDark:
			default:
			}
		case err, ok := <-errs:
			if ok && err != nil {
				uiLog.Warn("theme_watcher_error", slog.String("error", err.Error()))
			}
		}
	}
}

// ChangeChannel returns the channel that receives dark mode changes.
func (tw *ThemeWatcher) ChangeChannel() <-chan bool {
	return tw.changeCh
}

// Close stops the watcher goroutine. Safe to call multiple times.
func (tw *ThemeWatcher) Close() {
	tw.closeOnce.Do(func() {
		close(tw.closeCh)
	})
}
