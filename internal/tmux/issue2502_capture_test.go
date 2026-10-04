package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// No server is needed: the process boundary reports a live target, but its
// capture fails. The marker proves the failed capture branch was exercised.
func issue2502CaptureFailure(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "capture-attempted")
	script := `#!/bin/sh
for arg in "$@"; do
 case "$arg" in
 has-session) exit 0 ;;
 list-panes) printf '0\n'; exit 0 ;;
 capture-pane) : > "$ISSUE2502_CAPTURE_MARKER"; printf 'capture temporarily unavailable\n' >&2; exit 1 ;;
 esac
done
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ISSUE2502_CAPTURE_MARKER", marker)
	return marker
}

func TestIssue2502Capture_NewerHookCaptureFailure(t *testing.T) {
	marker := issue2502CaptureFailure(t)
	checked := time.Now().Add(-100 * time.Millisecond)
	work := BackgroundWork{Kind: BackgroundKindWorkflow, Task: "pending-workflow"}
	s := &Session{bgWork: work, bgWorkBlocked: true, Name: "issue2502-failed-bg-capture", Command: "claude", detectedTool: "claude", SocketName: DefaultSocketName(), bgWorkForegroundBusy: true, bgWorkCheckedAt: checked}
	gotWork, blocked, busy := s.BackgroundWorkSince(checked.Add(time.Millisecond))
	if gotWork != work || !blocked {
		t.Fatalf("capture failure changed background fallback: %+v blocked=%v", gotWork, blocked)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("capture not attempted: %v", err)
	}
	if busy {
		t.Error("capture failure reused pre-hook live spinner to promote running")
	}
	if !s.bgWorkCheckedAt.Equal(checked) {
		t.Error("capture failure advanced probe cache timestamp")
	}
}

func TestIssue2502Capture_SubstateFailureCannotPromote(t *testing.T) {
	marker := issue2502CaptureFailure(t)
	s := &Session{Name: "issue2502-failed-substate-capture", Command: "claude", detectedTool: "claude", SocketName: DefaultSocketName()}
	frame := "✻ Simmering…\n────────────────────────\n❯\n────────────────────────\nHaiku 4.5\n"
	if got := s.classifyFrameLocked(frame); got != SubstateRunning {
		t.Fatalf("fixture = %q", got)
	}
	sub, busy := s.GetSubstateWithLiveSpinner()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("capture not attempted: %v", err)
	}
	if sub != SubstateNone {
		t.Fatalf("failed current capture substate = %q, want unknown", sub)
	}
	if cached := s.GetSubstate(); cached != SubstateRunning {
		t.Fatalf("legacy display fallback = %q, want cached running", cached)
	}
	if busy {
		t.Error("failed current capture returned stale promotion-qualified spinner")
	}
}
