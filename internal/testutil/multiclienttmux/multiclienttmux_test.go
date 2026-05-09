package multiclienttmux_test

import (
	"os/exec"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil/multiclienttmux"
)

func skipIfNoTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
}

func TestNew_BootsIsolatedServer(t *testing.T) {
	skipIfNoTmux(t)

	h := multiclienttmux.New(t, "scratch")

	if h.SocketPath == "" {
		t.Fatal("SocketPath empty — server not booted on isolated socket")
	}
	if h.SessionName != "scratch" {
		t.Fatalf("SessionName=%q want scratch", h.SessionName)
	}

	// The session must exist on the isolated socket.
	out, err := exec.Command("tmux", "-S", h.SocketPath, "list-sessions").CombinedOutput()
	if err != nil {
		t.Fatalf("list-sessions on %s: %v\n%s", h.SocketPath, err, out)
	}
	if len(out) == 0 {
		t.Fatal("no sessions listed on isolated socket")
	}
}

func TestAggregateSize_ReportsLargestClient(t *testing.T) {
	skipIfNoTmux(t)

	h := multiclienttmux.New(t, "agg")

	// Spawn two clients of different sizes; aggregate-size = largest.
	if err := h.AddClient(80, 24); err != nil {
		t.Fatalf("AddClient 80x24: %v", err)
	}
	if err := h.AddClient(120, 40); err != nil {
		t.Fatalf("AddClient 120x40: %v", err)
	}

	// With aggressive-resize=on, the window resizes to the largest client.
	w, hgt, err := h.WindowSize()
	if err != nil {
		t.Fatalf("WindowSize: %v", err)
	}
	if w < 80 || hgt < 24 {
		t.Fatalf("WindowSize=%dx%d; expected at least 80x24", w, hgt)
	}
}

func TestNew_Cleanup(t *testing.T) {
	skipIfNoTmux(t)

	var socketPath string
	t.Run("inner", func(t *testing.T) {
		h := multiclienttmux.New(t, "ephemeral")
		socketPath = h.SocketPath
	})

	// After inner test cleanup, the server must be down.
	out, err := exec.Command("tmux", "-S", socketPath, "list-sessions").CombinedOutput()
	if err == nil && len(out) > 0 {
		t.Fatalf("server still running after cleanup: %s", out)
	}
}
