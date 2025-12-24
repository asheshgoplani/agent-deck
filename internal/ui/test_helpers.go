package ui

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Test helpers for width_test.go
// These are only compiled in test builds

// NewTestHome creates a minimal Home instance for testing
func NewTestHome() *Home {
	return &Home{
		helpOverlay:        NewHelpOverlay(),
		search:             NewSearch(),
		globalSearch:       NewGlobalSearch(),
		newDialog:          NewNewDialog(),
		groupDialog:        NewGroupDialog(),
		forkDialog:         NewForkDialog(),
		confirmDialog:      NewConfirmDialog(),
		mcpDialog:          NewMCPDialog(),
		previewCache:       make(map[string]string),
		previewCacheTime:   make(map[string]time.Time),
		launchingSessions:  make(map[string]time.Time),
		resumingSessions:   make(map[string]time.Time),
		mcpLoadingSessions: make(map[string]time.Time),
		forkingSessions:    make(map[string]time.Time),
		instanceByID:       make(map[string]*session.Instance),
	}
}

// SetFlatItemsForTest sets the flatItems field for testing
func (h *Home) SetFlatItemsForTest(items []session.Item) {
	h.flatItems = items
}

// SetCursorForTest sets the cursor field for testing
func (h *Home) SetCursorForTest(cursor int) {
	h.cursor = cursor
}

// RenderPreviewPaneForTest exposes renderPreviewPane for testing
func (h *Home) RenderPreviewPaneForTest(width, height int) string {
	return h.renderPreviewPane(width, height)
}

// RenderSessionListForTest exposes renderSessionList for testing
func (h *Home) RenderSessionListForTest(width, height int) string {
	return h.renderSessionList(width, height)
}

// SetSizeForTest sets the width and height fields for testing
func (h *Home) SetSizeForTest(width, height int) {
	h.width = width
	h.height = height
}

// RenderPanelTitleForTest exposes renderPanelTitle for testing
func (h *Home) RenderPanelTitleForTest(title string, width int) string {
	return h.renderPanelTitle(title, width)
}
