package ui

import "errors"

// Sentinel errors for insert-mode KeySender bring-up. These let callers
// disambiguate "no target session selected" from "tmux is sick" so the UI
// can decide between a quiet fallback (per-call SendKeys) and a user-facing
// setError.
var (
	errInsertNoTarget       = errors.New("insert mode: no target session")
	errInsertNoTmuxSession  = errors.New("insert mode: target has no tmux pane")
	errInsertNoRemoteConfig = errors.New("insert mode: remote not configured")
)
