package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	tea "github.com/charmbracelet/bubbletea"
)

// TestPerfFleetUIHarness is an opt-in entry point for tools/bench. Ordinary
// tests keep their existing isolation. This test borrows the runner's private
// fleet only for its lifetime, then restores TestMain's sandbox for cleanup.
// It measures synchronous production model work, not terminal paint or eager
// background worker startup. Returned tea.Cmd values are intentionally not run:
// asynchronous preview/remote work is outside the frame CPU measurement.
func TestPerfFleetUIHarness(t *testing.T) {
	if os.Getenv("AGENTDECK_BENCH_UI") != "1" {
		t.Skip("tools/bench only")
	}
	for _, pair := range [][2]string{{"HOME", "AGENTDECK_BENCH_HOME"}, {"TMUX_TMPDIR", "AGENTDECK_BENCH_TMUX_TMPDIR"}} {
		dir := filepath.Clean(os.Getenv(pair[1]))
		if !(strings.HasPrefix(dir, "/tmp/") || strings.HasPrefix(dir, "/private/tmp/")) {
			t.Fatalf("%s must point inside a throwaway temp directory", pair[1])
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("invalid benchmark directory %q", dir)
		}
		t.Setenv(pair[0], dir)
	}
	socket := os.Getenv("AGENTDECK_BENCH_SOCKET")
	if socket == "" || socket == "default" || strings.ContainsAny(socket, "/\\") {
		t.Fatal("explicit private tmux socket required")
	}
	previousSocket := tmux.DefaultSocketName()
	tmux.SetDefaultSocketName(socket)
	t.Cleanup(func() { tmux.SetDefaultSocketName(previousSocket) })
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	profile := os.Getenv("AGENTDECK_BENCH_PROFILE")
	if profile == "" {
		t.Fatal("explicit benchmark profile required")
	}
	t.Setenv("AGENTDECK_PROFILE", profile)
	runs, err := strconv.Atoi(os.Getenv("AGENTDECK_BENCH_RUNS"))
	if err != nil || runs < 1 {
		t.Fatal("positive AGENTDECK_BENCH_RUNS required")
	}
	if !tmux.IsServerAlive() {
		t.Fatal("private fleet server is not alive")
	}
	metrics := map[string][]float64{}
	record := func(key string, value float64) { metrics[key] = append(metrics[key], value) }
	started := time.Now()
	h := NewHomeWithProfile(profile)
	defer h.cancel()
	if h.storage == nil {
		t.Fatal("missing fleet storage")
	}
	defer h.storage.Close()
	// Use the same load command and result application as startup.
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	loaded := h.loadSessions().(loadSessionsMsg)
	if loaded.err != nil {
		t.Fatal(loaded.err)
	}
	expectedSize, err := strconv.Atoi(os.Getenv("AGENTDECK_BENCH_SIZE"))
	if err != nil || expectedSize < 1 {
		t.Fatal("positive AGENTDECK_BENCH_SIZE required")
	}
	if len(loaded.instances) != expectedSize {
		t.Fatalf("loaded %d sessions, want fleet size %d", len(loaded.instances), expectedSize)
	}
	h.Update(loaded)
	if h.initialLoading || len(h.instances) != len(loaded.instances) {
		t.Fatal("fleet not applied to model")
	}
	if h.hasModalVisible() {
		t.Fatal("benchmark config must disable setup, feedback and telemetry prompts")
	}
	frame := h.View()
	if strings.TrimSpace(frame) == "" {
		t.Fatal("empty populated frame")
	}
	record("tui_first_populated_frame_ms", float64(time.Since(started))/float64(time.Millisecond))
	for i := 0; i < runs; i++ {
		before := benchTmuxCalls(t)
		h.navigationHotUntil.Store(0)
		h.lastFullStatusSweep.Store(0)
		started = time.Now()
		h.backgroundStatusUpdate()
		record("status_pass_ms", float64(time.Since(started))/float64(time.Millisecond))
		if h.lastFullStatusSweep.Load() == 0 {
			t.Fatal("status sweep returned early or recovered a panic")
		}
		after := benchTmuxCalls(t)
		record("tmux_calls", float64(after-before))
	}
	// Alternate complete downward/upward traversals to exercise scrolling and
	// selection changes, rather than repeatedly measuring an end-of-list no-op.
	for i := 0; i < runs*100; i++ {
		key := tea.KeyDown
		if (i/max(1, len(h.flatItems)-1))%2 == 1 {
			key = tea.KeyUp
		}
		started = time.Now()
		h.Update(tea.KeyMsg{Type: key})
		frame = h.View()
		record("tui_key_repeat_frame_ms", float64(time.Since(started))/float64(time.Millisecond))
		if frame == "" {
			t.Fatal("key repeat generated empty frame")
		}
	}
	record("goroutines", float64(runtime.NumGoroutine()))
	benchRuntimeResources(t, record)
	data, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("AGENTDECK_BENCH_UI_OUTPUT"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func benchTmuxCalls(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("AGENTDECK_BENCH_TMUX_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(data, []byte{'\n'})
}

// Sample through the production health collector. These readings include its
// own sampling overhead and the UI test process, not the fleet's child shells.
// Eager Home workers remain disabled, so this cannot establish hook watcher
// descriptor growth or a long-lived TUI footprint.
func benchRuntimeResources(t *testing.T, record func(string, float64)) {
	t.Helper()
	dir := t.TempDir()
	stop := health.Start(dir, "bench-ui", session.GetHooksDir(), "bench-test")
	stop()
	report, err := health.Report(dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Processes) != 1 {
		t.Fatal("production health sampler did not report this process")
	}
	sample := report.Processes[0].Latest
	// Unsupported readings remain absent, matching health's nullable fields.
	if sample.RSSBytes != nil {
		record("rss_bytes", float64(*sample.RSSBytes))
	}
	if sample.OpenFDs != nil {
		record("open_fds", float64(*sample.OpenFDs))
	}
}
