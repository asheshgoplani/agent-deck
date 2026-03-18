package ui

import (
	"math"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
)

func TestGridLayout(t *testing.T) {
	tests := []struct {
		n    int
		cols int
		rows int
	}{
		{0, 1, 1},
		{1, 1, 1},
		{2, 2, 1},
		{3, 2, 2},
		{4, 2, 2},
		{5, 3, 2},
		{6, 3, 2},
		{7, 3, 3},
		{9, 3, 3},
		{12, 3, 4},
	}
	for _, tt := range tests {
		cols, rows := gridLayout(tt.n)
		if cols != tt.cols || rows != tt.rows {
			t.Errorf("gridLayout(%d) = (%d, %d), want (%d, %d)", tt.n, cols, rows, tt.cols, tt.rows)
		}
	}
}

func TestNormalizeProportions(t *testing.T) {
	props := []float64{0.3, 0.3, 0.3}
	normalizeProportions(props)
	sum := 0.0
	for _, p := range props {
		sum += p
	}
	if math.Abs(sum-1.0) > 0.001 {
		t.Errorf("expected sum ~1.0, got %f", sum)
	}
}

func TestNormalizeProportions_ZeroSum(t *testing.T) {
	props := []float64{0.0, 0.0}
	normalizeProportions(props)
	if props[0] != 0.5 || props[1] != 0.5 {
		t.Errorf("expected equal split, got %v", props)
	}
}

func makeTestCells(n int) []GridCell {
	cells := make([]GridCell, n)
	for i := range cells {
		ta := textarea.New()
		ta.SetHeight(1)
		ta.SetWidth(20)
		cells[i] = GridCell{input: ta}
	}
	return cells
}

func TestResizeColumn_Clamp(t *testing.T) {
	InitTheme("dark")

	g := &GridView{
		width:      120,
		height:     40,
		cells:      makeTestCells(2),
		colWidths:  []float64{0.85, 0.15},
		rowHeights: []float64{1.0},
	}

	g.focusIndex = 0
	g.resizeColumn(gridResizeStep)

	if g.colWidths[1] < gridMinProp-0.01 {
		t.Errorf("column 1 went significantly below minimum: %f (min %f)", g.colWidths[1], gridMinProp)
	}

	// Sum should still be ~1.0
	sum := g.colWidths[0] + g.colWidths[1]
	if math.Abs(sum-1.0) > 0.001 {
		t.Errorf("proportions don't sum to 1.0: %f", sum)
	}
}

func TestResizeColumn_SingleColumn_Noop(t *testing.T) {
	InitTheme("dark")

	g := &GridView{
		width:  120,
		height: 40,
		cells:  makeTestCells(1), // 1 cell = 1 column
	}

	g.resizeColumn(gridResizeStep)
	// Should not panic, colWidths should remain nil (no-op)
	if g.colWidths != nil {
		t.Errorf("expected nil colWidths for single column, got %v", g.colWidths)
	}
}
