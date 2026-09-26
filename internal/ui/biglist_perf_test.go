package ui

import (
	"fmt"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func bigListViewport(n int) *Home {
	items := make([]session.Item, n)
	for i := range items {
		items[i] = session.Item{Type: session.ItemTypeSession, Session: &session.Instance{ID: fmt.Sprintf("s%d", i)}}
	}
	h := newTestHomeWithItems(120, 40, items)
	h.embeddedLayout = true
	h.sidebarDensity = session.SidebarDensityFull
	h.cursor = n - 1
	return h
}

func TestBigListViewportLastRow(t *testing.T) {
	h := bigListViewport(600)
	h.syncViewport()
	if h.viewOffset > h.cursor || h.viewOffset < h.cursor-40 {
		t.Fatalf("cursor=%d offset=%d", h.cursor, h.viewOffset)
	}
	last := h.viewOffset
	h.cursor--
	h.syncViewport()
	if h.viewOffset != last {
		t.Fatalf("offset moved on one-row navigation: %d -> %d", last, h.viewOffset)
	}
}

func BenchmarkBigListJump(b *testing.B) {
	for _, n := range []int{50, 150, 300, 600} {
		b.Run(fmt.Sprintf("sessions=%d", n), func(b *testing.B) {
			h := bigListViewport(n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.viewOffset = 0
				h.syncViewport()
			}
		})
	}
}
