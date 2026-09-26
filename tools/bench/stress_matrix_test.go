//go:build stressmatrix

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestStressMatrixG14 runs the existing real-fleet harness inside the isolated
// G14 test container. It reports the mixed-profile size sweep; the full state,
// group, and remote Cartesian matrix requires separate fixtures.
func TestStressMatrixG14(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	build := func(output string, args ...string) {
		t.Helper()
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
		if _, err := os.Stat(output); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(dir, "agent-deck")
	harness := filepath.Join(dir, "ui.test")
	bench := filepath.Join(dir, "bench")
	build(cli, "build", "-o", cli, "./cmd/agent-deck")
	build(harness, "test", "-c", "-o", harness, "./internal/ui")
	build(bench, "build", "-o", bench, "./tools/bench")
	output := filepath.Join(dir, "mixed.json")
	cmd := exec.Command(bench, "-binary", cli, "-ui-harness", harness, "-out", output,
		"-sizes", "50,150,300,600", "-runs", "3", "-machine", "g14", "-revision", "candidate")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TMPDIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fleet benchmark: %v\n%s", err, out)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var result report
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	for _, metric := range result.Metrics {
		switch metric.Name {
		case "tui_key_repeat_frame_ms", "status_pass_ms", "tmux_calls", "rss_bytes":
			t.Logf("mixed size=%d metric=%s p50=%.3f p95=%.3f", metric.Size, metric.Name, metric.P50, metric.P95)
		}
	}
}
