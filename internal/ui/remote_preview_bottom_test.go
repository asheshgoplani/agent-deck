package ui

// Owner report 2026-10-18: on the host the Claude status line ("Model X |
// Context .. | 5h .. | 7d ..") is in every pane, but no remote session's
// preview showed it. renderRemotePreview wrote up to remotePreviewMaxLines
// lines of the captured pane under its header, and the layout's
// ensureExactHeight keeps the first rows of the block, so the pane's bottom
// (the status line and footer) was always the part cut. These tests render
// through the layout's own path (renderPreviewPane, then ensureExactHeight).
// Regenerate the golden with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ui/ -run TestRemotePreview_PaneBottomGolden

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const (
	remoteBottomStatus = "Model X | Context 41.0% | 5h 24.0% | 7d 57.0%"
	remoteBottomFooter = "⏵⏵ bypass permissions on · 1 shell"
)

// remoteBottomPane is a captured Claude pane as `session output --pane`
// returns it: a long transcript, the prompt between two rules, the status
// line, the footer, then the blank rows below the cursor.
func remoteBottomPane() string {
	var b strings.Builder
	for i := 1; i <= 120; i++ {
		fmt.Fprintf(&b, "\x1b[39m⏺ transcript line %03d\n", i)
	}
	b.WriteString("\x1b[38;5;244m" + strings.Repeat("─", 180) + " deck-work ─\n")
	b.WriteString("\x1b[38;5;246m❯ \x1b[39m\n")
	b.WriteString("\x1b[38;5;244m" + strings.Repeat("─", 196) + "\n")
	b.WriteString("\x1b[39m  \x1b[38;5;246m" + remoteBottomStatus + "\x1b[39m\n")
	b.WriteString("  \x1b[38;5;211m⏵⏵ bypass permissions on\x1b[38;5;246m · \x1b[38;5;44m1 shell\x1b[39m\n")
	b.WriteString("\n\n\n")
	return b.String()
}

func remoteBottomHome(t *testing.T, width, height int) *Home {
	t.Helper()
	forceTrueColorProfile()
	withTempAgentDeckHome(t, `
[remotes.deckbox]
host = "alice@box.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)
	home := NewHome()
	home.width = width
	home.height = height
	home.initialLoading = false
	remote := session.RemoteSessionInfo{ID: "r1", Title: "worker", Status: "waiting", Tool: "claude", RemoteName: "deckbox"}
	home.remoteSessions = map[string][]session.RemoteSessionInfo{"deckbox": {remote}}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteName: "deckbox", RemoteSession: &remote}}
	home.cursor = 0
	key := remotePreviewCacheKey("deckbox", remote.ID)
	home.previewCacheMu.Lock()
	if home.previewCache == nil {
		home.previewCache = map[string]string{}
	}
	home.previewCache[key] = remoteBottomPane()
	home.previewCacheMu.Unlock()
	return home
}

// remoteBottomFrame is the preview block as the layout shows it: rendered for
// rows lines, then cut to exactly rows (ensureExactHeight).
func remoteBottomFrame(h *Home, width, rows int) string {
	return stripAnsi(ensureExactHeight(h.renderRemotePreview(h.flatItems[0], width, rows), rows))
}

func TestRemotePreview_KeepsThePaneBottom(t *testing.T) {
	for _, size := range []struct{ width, rows int }{{100, 30}, {80, 24}, {60, 18}, {120, 50}} {
		t.Run(fmt.Sprintf("w%d-h%d", size.width, size.rows), func(t *testing.T) {
			h := remoteBottomHome(t, size.width, size.rows)
			frame := remoteBottomFrame(h, size.width, size.rows)
			for _, want := range []string{remoteBottomStatus, remoteBottomFooter, "❯", "Press Enter to attach via SSH"} {
				if !strings.Contains(frame, want) {
					t.Fatalf("the preview lost %q (the pane's bottom):\n%s", want, frame)
				}
			}
			if !strings.Contains(frame, "more lines above") {
				t.Fatalf("a cut pane says what is above it, as the local preview does:\n%s", frame)
			}
			// The newest transcript line sits right above the prompt block (from 24 rows: below that the
			// prompt, the status line and the footer take every row the body has).
			if size.rows >= 24 && !strings.Contains(frame, "transcript line 120") {
				t.Fatalf("the newest line is shown:\n%s", frame)
			}
			for i, line := range strings.Split(frame, "\n") {
				if w := cellWidth(line); w > size.width {
					t.Fatalf("line %d is %d cells wide in a %d-cell pane: %q", i, w, size.width, line)
				}
			}
		})
	}
}

func TestRemotePreview_ShortPaneIsShownWhole(t *testing.T) {
	h := remoteBottomHome(t, 100, 30)
	key := remotePreviewCacheKey("deckbox", "r1")
	h.previewCacheMu.Lock()
	h.previewCache[key] = "❯ \n" + remoteBottomStatus + "\n\n"
	h.previewCacheMu.Unlock()
	frame := remoteBottomFrame(h, 100, 30)
	if !strings.Contains(frame, remoteBottomStatus) || strings.Contains(frame, "more lines above") {
		t.Fatalf("a pane that fits is shown whole, with no cut marker:\n%s", frame)
	}
}

func TestRemotePreview_PaneBottomGolden(t *testing.T) {
	h := remoteBottomHome(t, 100, 30)
	got := strings.TrimRight(remoteBottomFrame(h, 100, 30), "\n") + "\n"
	path := filepath.Join("testdata", "remote_preview", "session-pane-bottom-w100-h30.txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (UPDATE_GOLDEN=1 to create)", path, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s differs.\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}
