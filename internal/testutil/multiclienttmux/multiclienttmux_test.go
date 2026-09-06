package multiclienttmux_test

import (
	"math"
	"os/exec"
	"strings"
	"testing"
	"time"

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

func requireWindowSize(t *testing.T, h *multiclienttmux.Harness, wantWidth, wantHeight int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		width, height, err := h.WindowSize()
		if err == nil && width == wantWidth && height == wantHeight {
			return
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("WindowSize: %v", err)
			}
			t.Fatalf("WindowSize=%dx%d; want %dx%d", width, height, wantWidth, wantHeight)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestAggregateSize_FitsCrossedClientDimensions(t *testing.T) {
	skipIfNoTmux(t)

	h := multiclienttmux.New(t, "agg")

	if err := h.AddClient(100, 62); err != nil {
		t.Fatalf("AddClient 100x62: %v", err)
	}
	if err := h.AddClient(189, 62); err != nil {
		t.Fatalf("AddClient 189x62: %v", err)
	}
	requireWindowSize(t, h, 100, 61)

	// Simulate a font-size change making the narrow client taller. Neither
	// client now dominates both axes; with a one-row status line, their usable
	// sizes are 88x70 and 189x61.
	if err := h.ResizeClient(0, 88, 71); err != nil {
		t.Fatalf("ResizeClient 88x71: %v", err)
	}

	// The component-wise minimum is the only shared size both viewers can show
	// completely.
	requireWindowSize(t, h, 88, 61)
}

func TestResizeClient_RejectsInvalidDimensions(t *testing.T) {
	skipIfNoTmux(t)

	h := multiclienttmux.New(t, "invalid-resize")
	if err := h.AddClient(80, 24); err != nil {
		t.Fatalf("AddClient 80x24: %v", err)
	}

	for _, tc := range []struct {
		name string
		cols int
		rows int
	}{
		{name: "zero columns", cols: 0, rows: 24},
		{name: "negative columns", cols: -1, rows: 24},
		{name: "oversized columns", cols: math.MaxUint16 + 1, rows: 24},
		{name: "zero rows", cols: 80, rows: 0},
		{name: "negative rows", cols: 80, rows: -1},
		{name: "oversized rows", cols: 80, rows: math.MaxUint16 + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := h.ResizeClient(0, tc.cols, tc.rows)
			if err == nil || !strings.Contains(err.Error(), "dimensions out of range") {
				t.Fatalf("ResizeClient(0, %d, %d) error = %v; want dimensions-out-of-range error", tc.cols, tc.rows, err)
			}
		})
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
