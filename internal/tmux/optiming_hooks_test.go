package tmux

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/optiming"
)

// TestFirstByteTimer_RecordsAttachOnceAndPassesBytes covers the tui.attach
// hook in the attach output pump: one record on the first byte, output intact.
func TestFirstByteTimer_RecordsAttachOnceAndPassesBytes(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(optiming.SetForTest(dir, true))

	src := &firstByteTimer{r: io.LimitReader(strings.NewReader(strings.Repeat("x", 100)), 100), op: "tui.attach", start: time.Now().Add(-5 * time.Millisecond)}
	var out strings.Builder
	buf := make([]byte, 7) // several reads
	if _, err := io.CopyBuffer(&out, src, buf); err != nil || out.Len() != 100 {
		t.Fatalf("copied %d bytes err=%v, want 100", out.Len(), err)
	}
	entries, err := optiming.ReadFile(filepath.Join(dir, optiming.FileName), time.Time{})
	if err != nil || len(entries) != 1 || entries[0].Op != "tui.attach" || entries[0].MS < 5 {
		t.Fatalf("entries = %+v err=%v, want one tui.attach >= 5ms", entries, err)
	}
}
