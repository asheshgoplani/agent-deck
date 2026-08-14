package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/asheshgoplani/agent-deck/internal/desktopnotify"
)

// desktopNotificationSender is a narrow daemon seam. Production uses the
// macOS transport; tests use it to prove lifecycle edges are forwarded without
// needing a GUI user session.
var desktopNotificationSender = SendDesktopNotification

// DesktopNotificationSocketPath is intentionally short: macOS limits Unix
// socket pathnames to about 104 bytes, while an XDG data directory can exceed
// that on managed machines. The UID-scoped socket is mode 0600 and /tmp's
// sticky-bit directory prevents another user from replacing it.
func DesktopNotificationSocketPath() (string, error) {
	return filepath.Join("/tmp", "agent-deck-desktop-notifications-"+strconv.Itoa(os.Getuid())+".sock"), nil
}

func DesktopNotificationStatePath() (string, error) {
	return dataPath("desktop-notifications-state.json", "desktop-notifications-state.json")
}

// SendDesktopNotification hands an already-normalized lifecycle event to the
// macOS GUI helper. The daemon deliberately treats an unavailable helper as a
// best-effort miss: session status tracking must never stall on desktop UI.
func SendDesktopNotification(source desktopnotify.SourceEvent) error {
	if runtime.GOOS != "darwin" || !GetDesktopNotificationsSettings().Enabled {
		return nil
	}
	event, ok := desktopnotify.Normalize(source)
	if !ok {
		return nil
	}
	if executable := os.Getenv("AGENT_DECK_DESKTOP_ROUTER"); executable != "" {
		event.BinaryPath = executable
	} else if executable, err := os.Executable(); err == nil {
		event.BinaryPath = executable
	}
	socket, err := DesktopNotificationSocketPath()
	if err != nil {
		return err
	}
	return desktopnotify.Send(socket, event)
}
