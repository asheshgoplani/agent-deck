//go:build !darwin

package notify

import "os/exec"

// desktopNotify uses notify-send when present (Linux desktops); no-op
// otherwise (headless/WSL without a notifier). Started non-blocking.
func desktopNotify(title, body string) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return
	}
	_ = exec.Command("notify-send", "-a", "agent-hopdeck", title, body).Start()
}
