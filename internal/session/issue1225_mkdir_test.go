package session

// Audit B10 — a failure to create the inbox directory (e.g. a permission issue,
// or a file planted where the directory must be) must SURFACE on the producer
// path, not be silently swallowed. A surfaced error makes the commit "transient"
// so the daemon re-observes and retries rather than dropping the completion.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestB10_CommitToInbox_SurfacesDirCreateFailure(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-b10-1777600000"

	// Plant a regular FILE where the inboxes directory must be, so MkdirAll fails.
	if err := os.MkdirAll(filepath.Dir(InboxDir()), 0o755); err != nil {
		t.Fatalf("prep: %v", err)
	}
	if err := os.WriteFile(InboxDir(), []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("plant blocking file: %v", err)
	}

	err := CommitToInbox(parent, TransitionNotificationEvent{
		ChildSessionID: "child-x",
		Profile:        "personal",
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
	})
	if err == nil {
		t.Fatalf("CommitToInbox must surface the dir-create failure, not drop silently")
	}
}
