package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2972 (1): the text output of `remote sessions` leaves out the
// remote's archived rows; the JSON output keeps them with the flag.
func TestRemoteSessionsText_LeavesOutArchivedRows(t *testing.T) {
	t.Parallel()
	sessions := []session.RemoteSessionInfo{
		{ID: "live-0001-aaaa", Title: "live-worker", Tool: "claude", Status: "idle"},
		{ID: "arch-0002-bbbb", Title: "archived-worker", Tool: "claude", Status: "idle", Archived: true},
	}
	var buf bytes.Buffer
	writeRemoteSessionsText(&buf, sessions)
	out := buf.String()
	if !strings.Contains(out, "live-worker") {
		t.Fatalf("live row missing from text output:\n%s", out)
	}
	if strings.Contains(out, "archived-worker") || strings.Contains(out, "arch-000") {
		t.Fatalf("archived row listed in text output:\n%s", out)
	}
	if !strings.Contains(out, "1 archived not shown") {
		t.Fatalf("text output does not say an archived row was left out:\n%s", out)
	}

	// JSON is unchanged: both rows, archived flag kept.
	encoded, err := json.Marshal(sessions)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"title":"archived-worker"`) || !strings.Contains(string(encoded), `"archived":true`) {
		t.Fatalf("JSON lost the archived row or its flag: %s", encoded)
	}
}

func TestRemoteSessionsText_OnlyArchivedRowsSaysNoSessions(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	writeRemoteSessionsText(&buf, []session.RemoteSessionInfo{
		{ID: "arch-0002-bbbb", Title: "archived-worker", Tool: "claude", Status: "idle", Archived: true},
	})
	out := buf.String()
	if !strings.Contains(out, "No sessions found.") || strings.Contains(out, "archived-worker") || strings.Contains(out, "TITLE") {
		t.Fatalf("all-archived remote should print no table:\n%s", out)
	}
}

// Issue #2972 (2): titles are cut by display width, never mid-rune.
func TestRemoteSessionsText_CutsTitlesByDisplayWidth(t *testing.T) {
	t.Parallel()
	titles := []string{
		"abcdefghijklmnoé-and-more",    // byte 17 falls inside "é"
		"日本語のセッションタイトルです",              // 15 wide runes, 30 columns
		"Привет-это-очень-длинное-имя", // two-byte Cyrillic runes
	}
	for _, title := range titles {
		var buf bytes.Buffer
		writeRemoteSessionsText(&buf, []session.RemoteSessionInfo{{ID: "id-1", Title: title, Tool: "claude", Status: "idle"}})
		out := buf.String()
		if !utf8.ValidString(out) {
			t.Fatalf("title %q was cut mid-rune: %q", title, out)
		}
		var row string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "claude") {
				row = strings.TrimPrefix(line, "  ")
			}
		}
		cell := strings.SplitN(row, " claude", 2)[0]
		if w := runewidth.StringWidth(strings.TrimRight(cell, " ")); w > 20 {
			t.Fatalf("title %q renders %d columns wide (max 20): %q", title, w, cell)
		}
		if !strings.Contains(cell, "...") {
			t.Fatalf("long title %q not marked as cut: %q", title, cell)
		}
	}
}
