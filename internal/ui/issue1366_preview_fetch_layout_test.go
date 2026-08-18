package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// armHomeOneSessionForPreview builds a Home with a single selected session,
// suitable for exercising the preview-fetch path. No tmux is started.
func armHomeOneSessionForPreview(t *testing.T) *Home {
	t.Helper()
	h := NewHome()
	h.height = 40
	h.initialLoading = false

	inst := session.NewInstanceWithTool("preview-sess", "/tmp/grp/proj", "claude")
	h.instancesMu.Lock()
	h.instances = []*session.Instance{inst}
	h.instanceByID = map[string]*session.Instance{inst.ID: inst}
	h.instancesMu.Unlock()
	h.groupTree = session.NewGroupTree(h.instances)
	h.rebuildFlatItems()
	for i, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == inst.ID {
			h.cursor = i
			break
		}
	}
	return h
}

// Issue #1366: navigating in a layout with no preview pane must NOT schedule a
// `tmux capture-pane` for a preview nobody renders.
func TestIssue1366_NoPreviewFetchInSingleColumnLayout(t *testing.T) {
	h := armHomeOneSessionForPreview(t)
	h.width = 45
	if cmd := h.fetchSelectedPreview(); cmd != nil {
		t.Fatal("hidden Preview scheduled a fetch")
	}
}

func TestIssue1366_HiddenPreviewFetchClearsInFlightMarker(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		h := armHomeOneSessionForPreview(t)
		h.width = 45
		inst, key, _ := h.selectedPreviewTarget()
		if inst == nil || key == "" {
			t.Fatal("setup: expected a selected local preview target")
		}

		h.previewCacheMu.Lock()
		h.previewFetchingID = key
		h.previewCacheMu.Unlock()

		if cmd := h.fetchPreview(inst, key, -1); cmd != nil {
			t.Fatal("hidden Preview must not return a fetch command")
		}

		h.previewCacheMu.RLock()
		defer h.previewCacheMu.RUnlock()
		if h.previewFetchingID != "" {
			t.Fatalf("hidden Preview left previewFetchingID = %q, want empty", h.previewFetchingID)
		}
	})

	t.Run("remote", func(t *testing.T) {
		h := NewHome()
		h.width = 45
		key := "remote:example:session"

		h.previewCacheMu.Lock()
		h.previewFetchingID = key
		h.previewCacheMu.Unlock()

		if cmd := h.fetchRemotePreview("example", "session", key); cmd != nil {
			t.Fatal("hidden remote Preview must not return a fetch command")
		}

		h.previewCacheMu.RLock()
		defer h.previewCacheMu.RUnlock()
		if h.previewFetchingID != "" {
			t.Fatalf("hidden remote Preview left previewFetchingID = %q, want empty", h.previewFetchingID)
		}
	})
}

// Regression guard: navigation schedules a fetch in both visible layouts.
func TestIssue1366_PreviewFetchInVisibleLayouts(t *testing.T) {
	for _, width := range []int{65, 120} {
		h := armHomeOneSessionForPreview(t)
		h.width = width
		if cmd := h.fetchSelectedPreview(); cmd == nil {
			t.Fatalf("width=%d: visible Preview did not schedule a fetch", width)
		}
	}
}
