package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// TestOrphanedBinaryReason pins when a running deck is told it can no
// longer update or restart itself: its executable path is gone (deleted,
// or Linux's "/proc/self/exe (deleted)") or sits in a Trash folder. A
// binary that was merely replaced in place is not orphaned.
func TestOrphanedBinaryReason(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := orphanedBinaryReason(exe); got != "" {
		t.Fatalf("existing binary: reason = %q, want none", got)
	}
	if got := orphanedBinaryReason(filepath.Join(dir, "gone")); !strings.Contains(got, "no longer exists") {
		t.Fatalf("deleted binary: reason = %q", got)
	}
	if got := orphanedBinaryReason(exe + " (deleted)"); !strings.Contains(got, "no longer exists") {
		t.Fatalf("linux deleted marker: reason = %q", got)
	}
	trash := filepath.Join(dir, ".Trash")
	if err := os.MkdirAll(trash, 0o755); err != nil {
		t.Fatal(err)
	}
	inTrash := filepath.Join(trash, "agent-deck")
	if err := os.WriteFile(inTrash, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := orphanedBinaryReason(inTrash); !strings.Contains(got, "Trash") {
		t.Fatalf("binary in the Trash: reason = %q", got)
	}
	if got := orphanedBinaryReason(""); got != "" {
		t.Fatalf("unknown path is not the orphan case: %q", got)
	}
}

// TestOrphanedBinary_NoticeAndNoAutoPaths pins what the deck does about
// it: the banner says so and tells the user to start agent-deck again,
// the unattended install is skipped with that reason, and the restart key
// refuses with it, so nothing silently installs into or re-execs from a
// dead path.
func TestOrphanedBinary_NoticeAndNoAutoPaths(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	t.Setenv(update.SkipUpdateCheckEnv, "")
	h := newInstallTestHome(t)
	if h.shouldRenderUpdateBanner() && strings.Contains(h.renderUpdateBannerText(), "start agent-deck again") {
		t.Fatal("a healthy deck must not show the orphan notice")
	}

	h.binaryOrphanReason = "its executable no longer exists at /bin/agent-deck"
	if !h.shouldRenderUpdateBanner() {
		t.Fatal("orphaned binary must show the banner")
	}
	text := h.renderUpdateBannerText()
	for _, want := range []string{"cannot update or restart itself", "no longer exists", "Quit and start agent-deck again to get v1.16.1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("banner %q missing %q", text, want)
		}
	}
	if reason := h.autoInstallSkipReason(h.updateInfo); !strings.Contains(reason, "no longer exists") {
		t.Fatalf("auto install skip reason = %q, want the orphan reason", reason)
	}
	if cmd := h.maybeAutoInstall(h.updateInfo); cmd != nil {
		t.Fatal("orphaned binary must not start an unattended install")
	}
	assertRestartBlocked(t, h, "no longer exists")

	// Once the path is back (reinstalled by hand) the notice clears.
	h.binaryOrphanReason = ""
	if strings.Contains(h.renderUpdateBannerText(), "start agent-deck again") {
		t.Fatal("notice must clear with the reason")
	}
}

// TestPollBinaryChange_TracksOrphan pins that the per-tick stat keeps
// binaryOrphanReason current: set when the file vanishes, cleared when it
// is back.
func TestPollBinaryChange_TracksOrphan(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fp, err := update.StatBinary(exe)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHome()
	h.binaryWatch = newBinaryWatch(exe, "1.16.0", fp)
	if cmd := h.pollBinaryChange(); cmd != nil || h.binaryOrphanReason != "" {
		t.Fatalf("unchanged file: cmd=%v reason=%q", cmd, h.binaryOrphanReason)
	}
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if cmd := h.pollBinaryChange(); cmd != nil || !strings.Contains(h.binaryOrphanReason, "no longer exists") {
		t.Fatalf("deleted file: cmd=%v reason=%q", cmd, h.binaryOrphanReason)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.pollBinaryChange()
	if h.binaryOrphanReason != "" {
		t.Fatalf("file back: reason = %q, want cleared", h.binaryOrphanReason)
	}
}
