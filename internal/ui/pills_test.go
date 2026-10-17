package ui

import (
	"strings"
	"testing"
)

func TestRenderPillsEmpty(t *testing.T) {
	if got := renderPills(nil, 0, "  ", 20); got != "" {
		t.Fatalf("empty options rendered %q", got)
	}
}

func TestRenderPillsWrappedRows(t *testing.T) {
	forceTrueColorProfile()
	useThemeForTest(t, "dark")

	labels := []string{"shell", "claude", "cursor agent"}
	rows := strings.Split(renderPills(labels, 1, "  ", 24), "\n")
	if len(rows) < 2 {
		t.Fatal("fixture must wrap onto multiple rows")
	}
	for _, row := range rows {
		if cellWidth(row) > 24 {
			t.Errorf("row exceeds width: %q", row)
		}
		if !strings.HasPrefix(stripAnsi(row), "  ") {
			t.Errorf("continuation lost its indent: %q", row)
		}
		// The indent inherits the surrounding dialog's surface; all cells
		// from the first pill onward belong to this renderer.
		if holes := backgroundHoles("│" + row[2:] + "│"); len(holes) > 0 {
			t.Errorf("wrapped pills expose terminal background: %q", holes)
		}
	}
	if !strings.Contains(stripAnsi(strings.Join(rows, "\n")), "cursor agent") {
		t.Fatal("multi-word tool name was split")
	}
}
