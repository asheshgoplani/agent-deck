package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Remote rows used to carry a private copy of the local status-glyph switch,
// and the copy had drifted. renderRemoteSessionItem rendered waiting as ◉ and
// error as ✗ (U+2717) where a local row renders ◐ and ✕ (U+2715), colored the
// glyph from the raw ANSI palette (lipgloss.Color("2"/"3"/"1"/"8")) instead of
// the theme's SessionStatus* styles, and had no case at all for stopped,
// queued, archived, or the ⚡/🔒 substates. renderRemotePreview was a THIRD
// copy that used ◐ but ✗ — so one waiting remote session showed ◉ in the list
// and ◐ in its own preview.
//
// This is the same class of bug as #1091 (remote tool labels rendered gray
// instead of the brand color), in the same function, fixed the same way: route
// the remote path through the local helper instead of duplicating it.
//
// These tests pin the parity so a future edit to rowStatusGlyph cannot silently
// leave the remote renderers behind again.

// TestRemoteRowStatusGlyph_MatchesLocalRowGlyph is the load-bearing assertion:
// for every status/substate/archived combination the wire can carry, the remote
// helper must return exactly what a local session in the same state returns.
func TestRemoteRowStatusGlyph_MatchesLocalRowGlyph(t *testing.T) {
	forceTrueColorProfile()

	cases := []struct {
		name     string
		status   string
		substate string
		archived bool
	}{
		{"running", "running", "", false},
		{"waiting", "waiting", "", false},
		{"idle", "idle", "", false},
		{"error", "error", "", false},
		{"stopped", "stopped", "", false},
		{"queued", "queued", "", false},
		{"archived overrides live status", "running", "", true},
		{"error + model unavailable", "error", string(session.SubstateModelUnavailable), false},
		{"error + auth 401", "error", string(session.SubstateAuth401), false},
		{"stopped + auth 401", "stopped", string(session.SubstateAuth401), false},
		// An older remote omits the substate/archived keys entirely; they
		// unmarshal to ""/false and must degrade to the coarse-status glyph.
		{"older remote, no substate", "waiting", "", false},
		// A status string this build does not know must not render blank.
		{"unknown status", "banana", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotIcon, gotStyle := remoteRowStatusGlyph(tc.status, tc.substate, tc.archived)
			wantIcon, wantStyle := rowStatusGlyph(
				session.Status(tc.status), session.Substate(tc.substate), tc.archived)

			if gotIcon != wantIcon {
				t.Errorf("glyph = %q, want %q — remote rows must use the local glyph set",
					gotIcon, wantIcon)
			}
			if gotStyle.Render("x") != wantStyle.Render("x") {
				t.Errorf("style = %q, want %q — remote rows must use the theme's SessionStatus* styles",
					gotStyle.Render("x"), wantStyle.Render("x"))
			}
			if gotIcon == "" {
				t.Errorf("glyph is empty for status %q — every state needs a visible indicator", tc.status)
			}
		})
	}
}

// TestRemoteSessionRow_UsesLocalGlyphs renders the actual row and asserts the
// two drifted glyphs are gone. ◉ and ✗ appearing anywhere in the output means
// the private switch is back.
func TestRemoteSessionRow_UsesLocalGlyphs(t *testing.T) {
	forceTrueColorProfile()

	cases := []struct {
		status    string
		wantGlyph string
		bugGlyph  string // what the drifted copy rendered
	}{
		{"waiting", "◐", "◉"},
		{"error", "✕", "✗"},
	}

	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			home := NewHome()
			home.width = 100
			home.height = 30

			remote := session.RemoteSessionInfo{
				ID: "r1", Title: "sess", Tool: "claude",
				Status: tc.status, RemoteName: "dev",
			}
			item := session.Item{
				Type:          session.ItemTypeRemoteSession,
				RemoteSession: &remote,
				RemoteName:    "dev",
			}

			var b strings.Builder
			home.renderRemoteSessionItem(&b, item, false)
			rendered := b.String()

			if !strings.Contains(rendered, tc.wantGlyph) {
				t.Errorf("remote %s row missing local glyph %q; got %q",
					tc.status, tc.wantGlyph, rendered)
			}
			if strings.Contains(rendered, tc.bugGlyph) {
				t.Errorf("remote %s row still renders the drifted glyph %q — "+
					"renderRemoteSessionItem must delegate to rowStatusGlyph; got %q",
					tc.status, tc.bugGlyph, rendered)
			}
		})
	}
}

// TestRemoteSessionRow_GlyphUsesThemeStyle guards the color half of the fix.
// The old code hardcoded lipgloss.Color("3") for waiting, which does not track
// a theme switch the way SessionStatusWaiting does.
func TestRemoteSessionRow_GlyphUsesThemeStyle(t *testing.T) {
	forceTrueColorProfile()

	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{
		ID: "r1", Title: "sess", Status: "waiting", RemoteName: "dev",
	}
	item := session.Item{
		Type:          session.ItemTypeRemoteSession,
		RemoteSession: &remote,
		RemoteName:    "dev",
	}

	var b strings.Builder
	home.renderRemoteSessionItem(&b, item, false)
	rendered := b.String()

	want := SessionStatusWaiting.Render("◐")
	if !strings.Contains(rendered, want) {
		t.Errorf("remote waiting glyph must be styled with SessionStatusWaiting "+
			"(theme-aware) rather than a raw ANSI palette index.\nwant substring: %q\ngot: %q",
			want, rendered)
	}
}

// TestRemoteSessionRow_ArchivedAndSubstateGlyphs covers the states the old
// remote switch had no case for. Substate and Archived now come over the wire
// in RemoteSessionInfo — `list --json` on the remote had always emitted them.
func TestRemoteSessionRow_ArchivedAndSubstateGlyphs(t *testing.T) {
	forceTrueColorProfile()

	cases := []struct {
		name      string
		remote    session.RemoteSessionInfo
		wantGlyph string
	}{
		{
			// The old code fell through to ○ gray, so an archived remote
			// looked like a live idle session.
			name: "archived beats a stale running status",
			remote: session.RemoteSessionInfo{
				Status: "running", Archived: true,
			},
			wantGlyph: "■",
		},
		{
			name: "stopped",
			remote: session.RemoteSessionInfo{
				Status: "stopped",
			},
			wantGlyph: "■",
		},
		{
			name: "error + model unavailable",
			remote: session.RemoteSessionInfo{
				Status: "error", Substate: string(session.SubstateModelUnavailable),
			},
			wantGlyph: "⚡",
		},
		{
			name: "error + auth 401",
			remote: session.RemoteSessionInfo{
				Status: "error", Substate: string(session.SubstateAuth401),
			},
			wantGlyph: "🔒",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := NewHome()
			home.width = 100
			home.height = 30

			rs := tc.remote
			rs.ID, rs.Title, rs.RemoteName = "r1", "sess", "dev"
			item := session.Item{
				Type:          session.ItemTypeRemoteSession,
				RemoteSession: &rs,
				RemoteName:    "dev",
			}

			var b strings.Builder
			home.renderRemoteSessionItem(&b, item, false)
			rendered := b.String()

			if !strings.Contains(rendered, tc.wantGlyph) {
				t.Errorf("want glyph %q in remote row; got %q", tc.wantGlyph, rendered)
			}
		})
	}
}

// TestRemoteSession_RowAndPreviewAgree closes the third-copy gap: the list row
// and the preview pane for the SAME session must show the SAME glyph. Before
// the fix a waiting remote was ◉ in the row and ◐ in the preview.
func TestRemoteSession_RowAndPreviewAgree(t *testing.T) {
	forceTrueColorProfile()

	for _, status := range []string{"running", "waiting", "idle", "error", "stopped"} {
		t.Run(status, func(t *testing.T) {
			home := NewHome()
			home.width = 100
			home.height = 30

			remote := session.RemoteSessionInfo{
				ID: "r1", Title: "sess", Tool: "claude",
				Status: status, Path: "/tmp/p", RemoteName: "dev",
			}
			item := session.Item{
				Type:          session.ItemTypeRemoteSession,
				RemoteSession: &remote,
				RemoteName:    "dev",
			}

			wantGlyph, _ := rowStatusGlyph(session.Status(status), "", false)

			var b strings.Builder
			home.renderRemoteSessionItem(&b, item, false)
			if !strings.Contains(b.String(), wantGlyph) {
				t.Errorf("row for %q missing glyph %q; got %q", status, wantGlyph, b.String())
			}

			preview := home.renderRemotePreview(item, 60, 20)
			if !strings.Contains(preview, wantGlyph) {
				t.Errorf("preview for %q missing glyph %q — row and preview must agree; got %q",
					status, wantGlyph, preview)
			}
		})
	}
}

// TestRemoteSessionPreview_ArchivedLabelFollowsGlyph pins the label fix: the
// archived override forces ■, so the text beside it must not still read
// "running".
func TestRemoteSessionPreview_ArchivedLabelFollowsGlyph(t *testing.T) {
	forceTrueColorProfile()

	home := NewHome()
	home.width = 100
	home.height = 30

	remote := session.RemoteSessionInfo{
		ID: "r1", Title: "sess", Status: "running", Archived: true,
		Path: "/tmp/p", RemoteName: "dev",
	}
	item := session.Item{
		Type:          session.ItemTypeRemoteSession,
		RemoteSession: &remote,
		RemoteName:    "dev",
	}

	preview := home.renderRemotePreview(item, 60, 20)
	if !strings.Contains(preview, "archived") {
		t.Errorf("archived remote preview must label itself archived; got %q", preview)
	}
	if strings.Contains(preview, "■ running") {
		t.Errorf("archived remote preview shows the ■ glyph next to a stale "+
			"\"running\" label; got %q", preview)
	}
}
