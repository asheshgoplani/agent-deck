package ui

import (
	"strings"
	"testing"
)

func TestConfirmDialogShowCloseSessionAutoDeleteDetails(t *testing.T) {
	dialog := NewConfirmDialog()
	dialog.SetSize(100, 30)
	dialog.ShowCloseSession("sess-1", "Temporary Session", false, true)

	view := dialog.View()
	if !strings.Contains(view, "Session metadata will be removed") {
		t.Fatalf("close dialog should mention metadata removal for temporary sessions, got %q", view)
	}
}

func TestConfirmDialogShowCloseSessionKeepsMetadataDetails(t *testing.T) {
	dialog := NewConfirmDialog()
	dialog.SetSize(100, 30)
	dialog.ShowCloseSession("sess-2", "Regular Session", false, false)

	view := dialog.View()
	if !strings.Contains(view, "Session metadata will be kept in the list") {
		t.Fatalf("close dialog should mention preserved metadata for regular sessions, got %q", view)
	}
}
