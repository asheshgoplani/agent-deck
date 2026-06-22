//go:build !darwin

package terminal

// OpenSessionInNewWindow is a no-op on non-macOS platforms. It returns
// ErrUnsupported so the TUI can render a friendly "not yet supported"
// message instead of treating the case as a hard failure. A future
// implementation for Linux (`gnome-terminal --`, `kitty`, ...) or Windows
// (`wt.exe new-tab`) can replace this stub without changing call sites.
func OpenSessionInNewWindow(_ AttachRequest) error {
	return ErrUnsupported
}

// OpenSessionInSplitPane is a no-op on non-macOS platforms.
func OpenSessionInSplitPane(_ AttachRequest) error {
	return ErrUnsupported
}
