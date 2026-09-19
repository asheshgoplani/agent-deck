package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/charmbracelet/x/ansi"
)

func TestRuntimeHealthWarningGolden(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_HOME", t.TempDir())
	health.ResetStatusPassBreachState()
	h := NewHome()
	h.width, h.height = 100, 30
	h.initialLoading = false
	h.debugMode = false

	// A single spike does not show the warning: it needs a sustained breach.
	h.queueHealthWarning(905*time.Millisecond, 150, 1)
	h.consumeHealthWarning()
	if strings.TrimSpace(h.renderHealthWarning()) != "" {
		t.Fatal("warning shown after a single spike")
	}
	h.queueHealthWarning(30*time.Millisecond, 150, 1)
	h.consumeHealthWarning()
	if strings.TrimSpace(h.renderHealthWarning()) != "" {
		t.Fatal("warning shown after recovery from a single spike")
	}

	// Three consecutive breaches show the warning.
	h.queueHealthWarning(300*time.Millisecond, 100, 1)
	h.consumeHealthWarning()
	h.queueHealthWarning(300*time.Millisecond, 100, 1)
	h.consumeHealthWarning()
	h.queueHealthWarning(300*time.Millisecond, 100, 1)
	h.consumeHealthWarning()
	got := ansi.Strip(h.View()) + "\n"
	if !strings.Contains(got, "Health: status pass exceeds 250 ms budget") {
		t.Fatalf("warning absent from actual frame after three consecutive breaches: %s", got)
	}
	path := filepath.Join("testdata", "runtime_health_warning.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("footer golden mismatch\nwant %q\ngot  %q", want, got)
	}
	h.width = 25
	if ansi.StringWidth(h.renderHealthWarning()) > h.width {
		t.Fatal("warning exceeds narrow footer width")
	}
	h.width = 100

	// Recovery: a single sample back under budget clears it.
	h.queueHealthWarning(30*time.Millisecond, 100, 1)
	h.consumeHealthWarning()
	if strings.TrimSpace(h.renderHealthWarning()) != "" {
		t.Fatal("health warning did not clear after recovery")
	}
}
